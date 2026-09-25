package ui

import (
	"context"
	"fmt"
	"image"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"gioui.org/app"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/chromafish/check/internal/clone"
	"github.com/chromafish/check/internal/jev"
	"github.com/chromafish/check/internal/llm"
	"github.com/chromafish/check/internal/signals"
	"github.com/chromafish/check/internal/state"
	"github.com/chromafish/check/internal/vcs"

	"github.com/chromafish/check/reef"
)

// Pane identifies which of the three columns has keyboard focus. They are
// declared in the order the columns are drawn, so a Pane is also the column
// index the splits know it by.
type Pane int

const (
	PaneRevs Pane = iota
	PaneFiles
	PaneDiff
	numPanes
)

// App is the whole application state. Everything the interface draws is read
// from here on the main goroutine; background work posts closures back through
// apply rather than writing fields directly.
type App struct {
	win *app.Window
	// ui is the design system: the palette, the type and the controls, plus
	// which control the pointer is over. Everything drawn goes through it.
	ui    *reef.UI
	repo  vcs.Repo
	store *state.Store

	// dir is the directory Check was opened on, which may be below the
	// repository's root. What is declared there is what the review runs.
	dir      string
	repoName string
	backend  vcs.Info

	// Preferences, and the sheet that sets them: the colour schemes and
	// typefaces found on this machine, and where the cursor is in each of the
	// two lists.
	settings      state.Settings
	settingsOpen  bool
	settingsList  int
	families      []string
	familySel     int
	familyFirst   int
	familiesAsked bool
	familiesDone  bool
	schemes       []reef.Scheme
	schemesAsked  bool
	schemeSel     int
	schemeFirst   int
	schemesDone   bool

	// The open-a-repository screen, shown whenever repo is nil.
	recent     []state.Recent
	recentSel  int
	openErr    string
	openButton int  // pointer tag for the repository name in the header
	picking    bool // a directory chooser is up

	// The column widths the last frame actually drew. State can change part
	// way through a frame, so these record what ended up on screen rather than
	// what was intended.
	drawnRevsW  int
	drawnFilesW int

	// Where each diff row was drawn in the last frame, in window coordinates.
	// Rows are not all one height — a file heading, a gap and a note each take
	// their own — so hit testing asks what was drawn rather than recomputing
	// it and drifting.
	rowHeights map[int]int
	rowTops    map[int]int
	nextRows   map[int]int // filled as the frame is drawn, adopted at its end

	// pinFile is the file a jump has just put on screen, and lastFirst the
	// scroll position the previous frame ended at. Together they tell a jump
	// apart from a scroll, so the two never fight over the selection.
	pinFile   int
	lastFirst int

	// The run of code swept out with the pointer or the keyboard.
	sel     Sel
	selDrag bool
	selRow  int // the row a pointer sweep started on

	// Revision list.
	revsetInput *reef.Field
	revset      string
	revs        []vcs.Revision
	revSel      int
	revList     layout.List
	graphLanes  int

	// The diff currently framed, and the revision it belongs to.
	spec vcs.DiffSpec
	rev  vcs.Revision
	desc string

	// File manifest.
	files    []FileRow
	fileSel  int
	fileList layout.List

	// Diff body.
	diff       *DiffDoc
	diffList   layout.List // indexed by row; the position both views agree on
	pairList   layout.List // and by two-column line, for the side by side view
	diffX      int         // horizontal scroll, in character cells
	sideBySide bool
	noWrap     bool // soft wrapping off; zero means wrapped, which is the default
	draft      *draft
	help       bool
	hoverRow   int // diff row under the pointer, -1 when none

	// In-diff find.
	//
	// findOpen is whether the bar is on screen, and is not the same as the
	// field having the caret. Gio drops the focus of a tag that was not drawn
	// in the last frame, so a bar laid out only while focused could never
	// take focus in the first place: it has to be drawn because it is open,
	// and it is open because something opened it.
	findField *reef.Field
	findOpen  bool
	askMode   bool  // the bar takes a question, not a string to match
	findHits  []int // rows matching the query
	findAt    int   // index into findHits, -1 when none

	// Semantic find. jev is nil when no key is set or the preference is off,
	// which is the ordinary state: find is then string matching and nothing
	// leaves the machine.
	jev        *jev.Client
	semRunning bool
	keyField   *reef.Field // the API key, in the settings sheet

	// The model plans are written by, nil when none is named, and the
	// settings sheet's fields for it.
	model          *llm.Client
	modelURLField  *reef.Field
	modelNameField *reef.Field
	modelKeyField  *reef.Field

	// The plan view: the repository's detectors, the inventory and plan of
	// the change on screen, and where the view's cursor is. drilled is set
	// while the diff is open from the plan, which B or Esc comes back from.
	detectors     []signals.Detector
	detectorsRoot string
	detectorsErr  string
	inv           *invState
	pl            *planState
	planList      layout.List
	planSel       int
	planCols      int
	drilled       bool
	// What the last question found, as places in the document it was asked
	// of rather than rows, which a note or an opened gap renumbers. askGen
	// counts questions, so that the answer to one since closed or replaced
	// is recognised when it arrives and dropped.
	askDoc   *DiffDoc
	askSpots []lineSpot
	askGen   int

	// The review column: every branch, the one open with its commits, and
	// which row is under review, named so that a reload can find it again.
	branches   []string
	current    string                 // the branch the working copy is on
	details    map[string]*vcs.Branch // branches read so far
	opened     map[string]bool        // branches open in the column
	branchGen  int
	reviewKey  string
	reviewList layout.List
	// Cloning from a pasted link: the field on the open screen, the folder
	// clones go in (in the settings sheet), and the clone in flight.
	urlField   *reef.Field
	cloneField *reef.Field
	cloning    *cloneJob
	console    *console // what git printed for the last clone, until it opens
	urlAsked   bool     // the field has been given the caret once already

	// otherFractions is the split of the view not on screen.
	otherFractions [2]float32

	// Manifest filter, open on the same terms as find.
	fileFilter *reef.Field
	filterOpen bool

	// Where the panes ended up in the last frame. Recorded during layout
	// rather than recomputed, so hit testing and tests cannot drift out of
	// step with the drawing code.
	bodyTop    int
	panelHeadH int
	filesColX  int
	viewedBoxX int
	viewedBoxW int
	diffColX   int
	diffBodyY  int

	focus  Pane
	splits *reef.Splits

	status   string
	statusAt time.Time
	failure  string
	busy     int

	// Text waiting to be put on the clipboard; written during the next frame,
	// since clipboard writes are frame commands.
	clipboard string
	notesOpen bool

	// Background results are queued here and applied at the start of a frame.
	mu      sync.Mutex
	pending []func()

	// generation is bumped whenever the selection moves, so results from a
	// superseded request are dropped instead of overwriting fresher ones.
	generation int
	stopDiff   context.CancelFunc // stops the diff still streaming, if any
}

// FileRow is one entry in the manifest, joining the diff's view of a file with
// the notes recorded against it.
type FileRow struct {
	vcs.FileChange
	Viewed   bool
	Stale    bool
	Comments int
	Open     int
}

// New builds the application around an open repository. dir is the
// directory it was opened on.
func New(repo vcs.Repo, dir string, store *state.Store, revset string) *App {
	a := newApp(repo, dir, store, revset)
	a.win = new(app.Window)
	a.win.Option(
		app.Size(unit.Dp(1280), unit.Dp(820)),
		app.MinSize(unit.Dp(720), unit.Dp(420)),
	)
	a.setTitle()
	return a
}

// NewOffscreen builds an application with no window, for laying frames out in
// tests.
func NewOffscreen(repo vcs.Repo, dir string, store *state.Store, revset string) *App {
	return newApp(repo, dir, store, revset)
}

func newApp(repo vcs.Repo, dir string, store *state.Store, revset string) *App {
	a := &App{
		ui:       reef.New(),
		repo:     repo,
		dir:      absDir(dir, repo),
		store:    store,
		revset:   revset,
		splits:   reef.NewSplits(classicFractions[0], classicFractions[1]),
		recent:   state.LoadRecent(),
		settings: state.LoadSettings(),
		pinFile:  -1,
	}
	a.applySettings()
	a.noWrap = a.settings.NoWrap
	if !a.classic() {
		a.splits.SetFraction(0, briefFractions[0])
		a.splits.SetFraction(1, briefFractions[1])
	}
	if repo != nil {
		a.repoName = filepath.Base(repo.Root())
		a.recent = state.RememberRecent(repo.Root())
	}
	a.revsetInput = reef.NewField(revset)
	a.findField = reef.NewField("")
	a.findField.Placeholder = findPlaceholder
	a.keyField = reef.NewField(a.settings.TypeSafeKey)
	a.keyField.Placeholder = "paste a TypeSafe key"
	// A key is a secret read off a screen someone else may be looking at.
	a.keyField.Editor().Mask = '•'
	if !a.settings.NoSemanticFind {
		a.jev = jev.Resolve(a.settings.TypeSafeKey)
	}
	a.modelURLField = reef.NewField(a.settings.ModelURL)
	a.modelURLField.Placeholder = llm.DefaultURL
	a.modelNameField = reef.NewField(a.settings.ModelName)
	a.modelNameField.Placeholder = "model name, e.g. qwen2.5-coder:7b"
	a.modelKeyField = reef.NewField(a.settings.ModelKey)
	a.modelKeyField.Placeholder = "API key, if the server wants one"
	a.modelKeyField.Editor().Mask = '•'
	a.model = modelFrom(a.settings)
	a.urlField = reef.NewField("")
	a.urlField.Placeholder = "paste a repository link — a branch link checks the branch out"
	a.cloneField = reef.NewField(a.settings.CloneDir)
	a.cloneField.Placeholder = shortenHome(clone.DefaultRoot())
	a.fileFilter = reef.NewField("")
	a.fileFilter.Placeholder = "filter files"
	a.findAt = -1
	if repo != nil {
		a.adoptBackend(repo)
	}
	a.hoverRow = -1
	a.revList.Axis = layout.Vertical
	a.fileList.Axis = layout.Vertical
	a.reviewList.Axis = layout.Vertical
	a.planList.Axis = layout.Vertical
	a.diffList.Axis = layout.Vertical
	a.pairList.Axis = layout.Vertical
	return a
}

// absDir is the opened directory as an absolute path with symbolic links
// resolved, which is how a tool reports its root, so the two can be compared.
// It falls back to the root when no directory was given.
func absDir(dir string, repo vcs.Repo) string {
	if dir == "" && repo != nil {
		return repo.Root()
	}
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		return real
	}
	return dir
}

// adoptBackend takes the labels and the placeholder from whichever backend was
// opened. The two query languages have nothing in common, so the field says
// which one it is taking rather than leaving a person to guess.
func (a *App) adoptBackend(r vcs.Repo) {
	a.backend = r.Info()
	a.revsetInput.Placeholder = a.backend.QueryHint
}

// Layout draws one frame. Exported so frames can be rendered without a window.
func (a *App) Layout(gtx layout.Context) layout.Dimensions {
	a.applyPending()
	return a.layout(gtx)
}

// Idle reports whether all background work has finished. A result is applied
// before the work it came from is counted done, so an idle application has
// everything it fetched on screen.
func (a *App) Idle() bool { return a.busy == 0 }

// Start kicks off the initial load without running an event loop.
func (a *App) Start() {
	if a.repo == nil {
		return
	}
	a.reload(true)
}

// setTitle names the window after whatever is open.
func (a *App) setTitle() {
	if a.win == nil {
		return
	}
	if a.repoName == "" {
		a.win.Option(app.Title("Check"))
		return
	}
	a.win.Option(app.Title("Check — " + a.repoName))
}

// Run drives the window until it closes.
func (a *App) Run() error {
	startTrace()
	a.Start()

	var ops op.Ops
	for {
		switch e := a.win.Event().(type) {
		case app.DestroyEvent:
			return e.Err
		case app.FrameEvent:
			done := traceFrame()
			began := time.Now()
			gtx := app.NewContext(&ops, e)
			a.applyPending()
			a.layout(gtx)
			laid := time.Since(began)
			e.Frame(gtx.Ops)
			done(laid)
		}
	}
}

func (a *App) background(work func(context.Context) func()) {
	a.backgroundIn(context.Background(), 30*time.Second, work)
}

// backgroundIn runs work off the main goroutine and queues the closure it
// returns, which applies the result during the next frame.
//
// The limit exists for a query that has not answered in half a minute, which
// is wedged rather than slow. A limit of zero leaves the work to guard itself,
// and the parent context is how a caller stops work it has superseded.
func (a *App) backgroundIn(parent context.Context, limit time.Duration, work func(context.Context) func()) {
	a.busy++
	go func() {
		ctx := parent
		if limit > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(parent, limit)
			defer cancel()
		}
		apply := work(ctx)
		a.after(func() {
			a.busy--
			if apply != nil {
				apply()
			}
		})
	}()
}

// supersede marks everything in flight as stale. A diff still streaming is
// stopped rather than left to run: it would otherwise read a change to its end
// for a revision that is no longer on screen, holding the tool open and every
// line it has parsed alive.
func (a *App) supersede() {
	a.generation++
	if a.stopDiff != nil {
		a.stopDiff()
		a.stopDiff = nil
	}
}

func (a *App) applyPending() {
	a.mu.Lock()
	pending := a.pending
	a.pending = nil
	a.mu.Unlock()
	for _, f := range pending {
		f()
	}
}

// fail records an error for the status bar. Errors from jj are usually a bad
// revset, which the user fixes by typing, so they belong in the chrome rather
// than in a dialog. Every error passes here, so this is where each one
// reaches the behaviour log; the status bar forgets it in seconds.
func (a *App) fail(err error) {
	if err != nil {
		a.failure = err.Error()
		a.statusAt = time.Now()
		slog.Error(a.failure)
	}
}

// note puts a message in the status bar, and in the log for the same reason
// fail does.
func (a *App) note(format string, args ...any) {
	a.status = fmt.Sprintf(format, args...)
	a.failure = ""
	a.statusAt = time.Now()
	slog.Info(a.status)
}

// reload re-evaluates the revset. When snapshot is set, jj is allowed to
// record the working copy first, so edits on disk show up.
func (a *App) reload(snapshot bool) {
	if a.repo == nil {
		return
	}
	revset := a.revset
	if revset == "" {
		revset = a.backend.DefaultQuery
	}
	keep := ""
	if a.revSel < len(a.revs) {
		keep = a.revs[a.revSel].ChangeID
	}
	a.background(func(ctx context.Context) func() {
		if snapshot {
			if err := a.repo.Snapshot(ctx); err != nil {
				return func() { a.fail(err) }
			}
		}
		revs, err := a.repo.Log(ctx, revset, 500)
		if err != nil {
			return func() { a.fail(err) }
		}
		vcs.BuildGraph(revs)
		// A repository with no branches, or a tool that will not list them,
		// still has its working copy to review.
		branches, _ := a.repo.Branches(ctx)
		current, _ := a.repo.CurrentBranch(ctx)
		return func() {
			slog.Info("revset evaluated", "revset", revset, "revisions", len(revs))
			a.failure = ""
			a.revs = revs
			a.branches = branches
			if current != a.current {
				delete(a.opened, a.current)
			}
			a.current = current
			if current != "" {
				if a.opened == nil {
					a.opened = map[string]bool{}
				}
				a.opened[current] = true
			}
			a.revSel = 0
			for i, r := range revs {
				if r.ChangeID == keep {
					a.revSel = i
					break
				}
			}
			if !a.classic() {
				a.restoreReview()
				return
			}
			a.selectRev(a.revSel)
		}
	})
}

// selectRev frames a revision's diff and loads its file manifest.
func (a *App) selectRev(i int) {
	if i < 0 || i >= len(a.revs) {
		a.revs = a.revs[:0]
		a.files = nil
		a.diff = nil
		return
	}
	a.revSel = i
	a.review(a.revs[i], vcs.DiffSpec{Kind: vcs.DiffChange, Rev: a.revs[i].ChangeID})
}

func (a *App) loadFiles() {
	if a.repo == nil {
		return
	}
	a.supersede()
	gen := a.generation
	spec := a.spec
	rev := a.rev
	a.files = nil
	a.diff = nil
	a.fileSel = 0
	a.dropPlan()

	a.background(func(ctx context.Context) func() {
		files, err := a.repo.Files(ctx, spec)
		desc, _ := a.repo.Description(ctx, rev.ChangeID)
		return func() {
			if gen != a.generation {
				return
			}
			if err != nil {
				a.fail(err)
				return
			}
			slog.Info("revision selected", "change", rev.ChangeID, "files", len(files))
			a.failure = ""
			a.desc = desc
			a.files = a.decorate(rev, files)
			a.fileList.Position.First = 0
			a.fileList.Position.Offset = 0
			a.fileSel = 0
			a.loadDiff()
		}
	})
}

// after queues work to run at the start of the next frame.
//
// A control drawn inside the diff list cannot restructure that list while it is
// being laid out — the list is part way through walking rows it has already
// counted — so anything that adds or removes rows goes through here. It is also
// how background work hands over a result, including one that arrives in
// pieces: a diff is posted file by file as it is read.
func (a *App) after(f func()) {
	a.mu.Lock()
	a.pending = append(a.pending, f)
	a.mu.Unlock()
	if a.win != nil {
		a.win.Invalidate()
	}
}

// decorate joins each changed file with what has been recorded about it.
func (a *App) decorate(rev vcs.Revision, files []vcs.FileChange) []FileRow {
	rows := make([]FileRow, len(files))
	for i, f := range files {
		viewed, stale := a.store.Viewed(rev.ChangeIDFull, f.Path, rev.CommitIDFull)
		total, open := a.store.CommentCount(rev.ChangeIDFull, f.Path)
		rows[i] = FileRow{FileChange: f, Viewed: viewed, Stale: stale, Comments: total, Open: open}
	}
	return rows
}

func (a *App) refreshRow(i int) {
	if i < 0 || i >= len(a.files) {
		return
	}
	f := &a.files[i]
	f.Viewed, f.Stale = a.store.Viewed(a.rev.ChangeIDFull, f.Path, a.rev.CommitIDFull)
	f.Comments, f.Open = a.store.CommentCount(a.rev.ChangeIDFull, f.Path)
}

func (a *App) layout(gtx layout.Context) layout.Dimensions {
	// The chosen body size is applied once, here, by scaling the frame's own
	// point size: from this line down every size in the design system's type
	// scale is the chosen size rather than the token's.
	gtx = a.ui.Sized(gtx)

	a.handleKeys(gtx)
	reef.Fill(gtx, gtx.Constraints.Max, a.ui.P.Bg)

	size := gtx.Constraints.Max
	body := a.layoutBody
	switch {
	case a.repo == nil:
		body = a.layoutOpen
	}

	// The two strips are fixed in device pixels, so the chrome holds still as
	// the type is set larger — up to the point where a strip would clip its own
	// label, which is worse than a strip a few pixels taller.
	headerH := a.ui.MastheadHeight(gtx)
	statusH := a.ui.StripHeight(gtx)
	bodyH := size.Y - headerH - statusH
	a.bodyTop = headerH

	a.layoutHeader(gtx, size.X, headerH)

	if bodyH > 0 {
		fill(gtx, image.Pt(0, headerH), image.Pt(size.X, bodyH), body)
	}
	fill(gtx, image.Pt(0, size.Y-statusH), image.Pt(size.X, statusH), a.layoutStatus)

	a.layoutNotes(gtx)
	a.layoutHelp(gtx)
	a.layoutSettings(gtx)
	a.ui.CornerTicks(gtx, size)
	a.flushClipboard(gtx)
	return layout.Dimensions{Size: size}
}

// layoutBody draws the three columns and the draggable rules between them.
func (a *App) layoutBody(gtx layout.Context) {
	size := gtx.Constraints.Max
	widths := a.splits.Widths(gtx, size.X)
	revsW, filesW := widths[0], widths[1]
	a.drawnRevsW, a.drawnFilesW = revsW, filesW

	// Each column is drawn into its own exact space, so a panel never has to
	// know where in the window it is.
	at := func(x, w int, draw func(layout.Context)) {
		if w <= 0 {
			return
		}
		fill(gtx, image.Pt(x, 0), image.Pt(w, size.Y), draw)
	}
	column := func(x, w int, pane Pane, title string, controls, body func(layout.Context)) {
		at(x, w, func(gtx layout.Context) { a.panel(gtx, title, pane, controls, body) })
	}
	rail := func(x, w int, pane Pane, letter string, count int) {
		at(x, w, func(gtx layout.Context) {
			a.ui.Rail(gtx, railTag{pane}, letter, count, func() { a.showPane(pane) })
		})
	}

	// The plan view is two columns: what there is to review, and the plan
	// of the change under review. The diff is a drill-down from it.
	if !a.classic() {
		if a.splits.Hidden(int(PaneRevs)) {
			rail(0, revsW, PaneRevs, "R", len(a.reviewRows()))
		} else {
			column(0, revsW, PaneRevs, "REVIEW", nil, a.layoutReview)
		}
		a.filesColX, a.drawnFilesW = revsW+1, 0
		a.diffColX = revsW + 1
		column(a.diffColX, size.X-a.diffColX, PaneDiff, a.planTitle(), a.planControls, a.layoutPlan)
		reef.VLine(gtx, revsW, size.Y, a.ui.P.Rule)
		a.splits.Handles(gtx, size, widths[:1])
		return
	}

	if a.splits.Hidden(int(PaneRevs)) {
		rail(0, revsW, PaneRevs, "R", len(a.revs))
	} else {
		column(0, revsW, PaneRevs, "REVISIONS", nil, a.layoutRevs)
	}
	a.filesColX = revsW + 1
	if a.splits.Hidden(int(PaneFiles)) {
		rail(a.filesColX, filesW, PaneFiles, "F", len(a.files))
	} else {
		column(a.filesColX, filesW, PaneFiles, a.manifestTitle(), nil, a.layoutFiles)
	}
	a.diffColX = revsW + filesW + 2
	column(a.diffColX, size.X-a.diffColX, PaneDiff, a.diffTitle(), a.diffControls, a.layoutDiff)

	reef.VLine(gtx, revsW, size.Y, a.ui.P.Rule)
	reef.VLine(gtx, revsW+filesW+1, size.Y, a.ui.P.Rule)
	a.splits.Handles(gtx, size, widths)
}

// manifestTitle names the pane and says how much of it is done, so progress is
// visible without counting boxes.
func (a *App) manifestTitle() string {
	read, total := a.readCount()
	if total == 0 {
		return "MANIFEST"
	}
	return fmt.Sprintf("MANIFEST · %d/%d READ", read, total)
}

func (a *App) diffTitle() string {
	if a.spec.Kind == vcs.DiffRange {
		if strings.HasPrefix(a.rev.ChangeIDFull, "branch:") {
			return "DIFF · " + strings.ToUpper(a.rev.ChangeID)
		}
		return "DIFF · RANGE"
	}
	return "DIFF"
}

// panel frames one column: a header of letterspaced capitals, a hairline, and
// the body below it. The design system draws the frame; what is decided here
// is what goes in the header, which for a tree is the control that puts it
// away.
func (a *App) panel(gtx layout.Context, title string, pane Pane, controls, body func(layout.Context)) {
	tree := pane == PaneRevs || pane == PaneFiles

	// A tree's header carries the control that puts it away at its right end,
	// and the title stops short of it rather than printing under it.
	reserve := gtx.Dp(reef.PadInline)
	head := a.ui.StripHeight(gtx)
	if tree {
		reserve += head
	}

	a.panelHeadH = a.ui.Panel(gtx, reef.Panel{
		Title:   title,
		Focused: a.focus == pane,
		Reserve: reserve,
		Header: func(gtx layout.Context) {
			if controls != nil {
				controls(gtx)
			}
			// Either tree can be put away to give the diff the window, and the
			// way to do it is a control in its own header rather than a
			// shortcut nobody sees.
			if tree {
				size := gtx.Constraints.Max
				a.ui.Collapse(gtx, size.X, size.Y, railTag{pane}, func() { a.hidePane(pane) })
			}
		},
		Body: body,
	})
}

// railTag names the control that puts a column away and the rail that brings
// it back. They are never both on screen for the same pane, so they share it.
type railTag struct{ pane Pane }

// hidePane puts a tree away. Focus follows: a pane you cannot see is not a
// pane you can be working in.
func (a *App) hidePane(pane Pane) {
	if a.splits.Hidden(int(pane)) {
		return
	}
	a.splits.Toggle(int(pane))
	if a.focus == pane {
		a.focus = PaneDiff
	}
	a.note("%s hidden", paneName(pane))
}

func (a *App) showPane(pane Pane) {
	if !a.splits.Hidden(int(pane)) {
		return
	}
	a.splits.Toggle(int(pane))
	a.focus = pane
}

// togglePane is the keyboard's way to the same thing.
func (a *App) togglePane(pane Pane) {
	if a.splits.Hidden(int(pane)) {
		a.showPane(pane)
		return
	}
	a.hidePane(pane)
}

// focusCode puts both trees away, or brings both back: the one keystroke for
// giving the whole window to the code.
func (a *App) focusCode() {
	if a.splits.Hidden(int(PaneRevs)) && a.splits.Hidden(int(PaneFiles)) {
		a.splits.SetHidden(int(PaneRevs), false)
		a.splits.SetHidden(int(PaneFiles), false)
		a.note("trees back")
		return
	}
	a.splits.SetHidden(int(PaneRevs), true)
	a.splits.SetHidden(int(PaneFiles), true)
	a.focus = PaneDiff
	a.note("code only — z brings the trees back")
}

func paneName(p Pane) string {
	switch p {
	case PaneRevs:
		return "revisions"
	case PaneFiles:
		return "manifest"
	}
	return "diff"
}
