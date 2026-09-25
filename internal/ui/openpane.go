package ui

import (
	"context"
	"errors"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"gioui.org/font"
	"gioui.org/layout"
	"gioui.org/op"

	"github.com/chromafish/check/internal/clone"
	"github.com/chromafish/check/internal/repo"
	"github.com/chromafish/check/internal/state"
	"github.com/chromafish/check/internal/vcs"

	"github.com/chromafish/check/reef"
)

// layoutOpen draws the screen shown when no repository is loaded: a way to
// choose one, and the ones opened before.
func (a *App) layoutOpen(gtx layout.Context) {
	ui := a.ui
	size := gtx.Constraints.Max
	row := ui.Row(gtx)
	pad := gtx.Dp(reef.Gutter)
	cell := ui.Cell(gtx, reef.SizeUI, false)

	fieldH := ui.FieldHeight(gtx)
	w := min(size.X-gtx.Dp(80), 72*cell.X)
	rows := len(a.recent) + 7
	consoleH := 0
	if a.console != nil {
		consoleH = ui.CodeRow(gtx)*consoleRows + pad
	}
	h := min(size.Y-gtx.Dp(80), row*rows+fieldH+consoleH+pad*2)
	x, y := (size.X-w)/2, max(gtx.Dp(24), (size.Y-h)/3)

	// The sheet sits on a dot-grid desk, which is what makes it read as a
	// sheet: the paper edge is visible, and the drop under it is a printed
	// offset rather than a glow.
	ui.DotGrid(gtx, size)
	sheet := image.Rect(x, y, x+w, y+h)
	ui.Sheet(gtx, sheet)

	// One heading of the sheet, on the band that starts at ly.
	label := func(ly int, c reef.ColorNRGBA, txt string) {
		off := op.Offset(image.Pt(0, ly)).Push(gtx.Ops)
		ui.LabelAt(gtx, c, x+pad, row, x+w-pad, txt)
		off.Pop()
	}
	text := func(ly, indent int, weight font.Weight, c reef.ColorNRGBA, txt string) {
		fit(gtx, image.Pt(0, ly), image.Pt(x+w-pad, row), func(gtx layout.Context) {
			a.cellText(gtx, x+pad+indent, row, x+w-pad, weight, c, txt)
		})
	}

	ly := y + pad
	label(ly, ui.P.Strong, "OPEN A REPOSITORY")
	ly += row

	if a.openErr != "" {
		label(ly, ui.P.Error, "ERROR "+firstLine(a.openErr))
	} else {
		text(ly, 0, font.Normal, ui.P.Muted, "⌘O  choose a folder…")
	}
	ly += row

	// A link is the other way in: cloned under the clone folder the first
	// time, found and fetched every time after.
	ly += row / 2
	label(ly, ui.P.Faint, "OR PASTE A LINK")
	ly += row
	fill(gtx, image.Pt(x+pad, ly), image.Pt(w-pad*2, fieldH), func(gtx layout.Context) {
		a.urlField.Layout(gtx, ui.Theme)
	})
	if len(a.recent) == 0 && !a.urlField.Focused() && a.cloning == nil && !a.urlAsked {
		// With nothing to pick from, the link is the obvious next thing.
		a.urlAsked = true
		a.urlField.Focus(gtx)
	}
	ly += fieldH
	switch job := a.cloning; {
	case job != nil:
		verb := "CLONING"
		if job.fetching {
			verb = "FETCHING"
		}
		label(ly, ui.P.Action, fmt.Sprintf("%s %s/%s · ESC STOPS", verb, job.src.Host, job.src.Path))
	default:
		text(ly, 0, font.Normal, ui.P.Faint, "into "+shortenHome(clone.ExpandRoot(a.settings.CloneDir))+"  ·  change it in settings (,)")
	}
	ly += row
	if a.console != nil {
		a.layoutConsole(gtx, image.Rect(x+pad, ly, x+w-pad, ly+consoleH-pad))
		ly += consoleH
	}

	if len(a.recent) == 0 {
		ly += row
		text(ly, 0, font.Normal, ui.P.Faint, "Nothing opened yet.")
		return
	}

	ly += row / 2
	label(ly, ui.P.Faint, "RECENT")
	ly += row

	for i, r := range a.recent {
		if ly+row > y+h-pad/2 {
			break
		}
		rowRect := image.Rect(x+gtx.Dp(reef.Sp3), ly, x+w-gtx.Dp(reef.Sp3), ly+row)
		if i == a.recentSel {
			ui.SelectionRect(gtx, rowRect, true)
		}
		// Each entry is clickable as well as reachable with the arrow keys.
		fill(gtx, rowRect.Min, rowRect.Size(), func(gtx layout.Context) {
			a.clickable(gtx, rowRect.Size(), &a.recent[i], func() {
				a.recentSel = i
				a.openRepo(r.Path)
			})
		})

		fg, muted := ui.P.Fg, ui.P.Faint
		text(ly, 0, reef.WeightLabel, fg, r.Name())
		text(ly, (len(r.Name())+2)*cell.X, font.Normal, muted, shortenHome(filepath.Dir(r.Path)))
		ly += row
	}
}

// shortenHome writes a path under the home directory with a tilde, the way it
// would be typed.
func shortenHome(path string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	if path == home {
		return "~"
	}
	if rest, found := strings.CutPrefix(path, home+string(filepath.Separator)); found {
		return "~" + string(filepath.Separator) + rest
	}
	return path
}

// chooseFolder runs the system directory chooser and opens what comes back.
// Only one chooser is ever in flight: a second request while a panel is up
// would sit behind it and then open a panel nobody asked for any more, which
// is worse than the click appearing to do nothing.
func (a *App) chooseFolder() {
	if a.picking {
		return
	}
	start := ""
	if a.repo != nil {
		start = a.repo.Root()
	} else if len(a.recent) > 0 {
		start = a.recent[0].Path
	}
	a.picking = true
	a.background(func(ctx context.Context) func() {
		path, err := pickFolder(ctx, start)
		return func() {
			a.picking = false
			if errors.Is(err, errPickCancelled) {
				return
			}
			if err != nil {
				a.openErr = err.Error()
				a.fail(err)
				return
			}
			a.openRepo(path)
		}
	})
}

// consoleRows is how much of git's output the open screen shows.
const consoleRows = 8

// console holds what git has printed, as a terminal would show it: a
// carriage return starts its line over, which is how git draws progress, so
// a transfer is one line counting up rather than hundreds.
type console struct {
	mu      sync.Mutex
	lines   []string
	cur     []byte
	restart bool // a carriage return came, and the next byte starts the line over
	changed func()
}

// consoleKeep bounds what is held; only the tail is ever drawn.
const consoleKeep = 200

func (c *console) Write(p []byte) (int, error) {
	c.mu.Lock()
	for _, b := range p {
		switch b {
		case '\n':
			c.lines = append(c.lines, string(c.cur))
			c.cur, c.restart = c.cur[:0], false
			if len(c.lines) > consoleKeep {
				c.lines = append(c.lines[:0], c.lines[len(c.lines)-consoleKeep:]...)
			}
		case '\r':
			c.restart = true
		default:
			if c.restart {
				c.cur, c.restart = c.cur[:0], false
			}
			c.cur = append(c.cur, b)
		}
	}
	c.mu.Unlock()
	if c.changed != nil {
		c.changed()
	}
	return len(p), nil
}

// tail is the last n lines, the one being written included.
func (c *console) tail(n int) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	all := c.lines
	if len(c.cur) > 0 {
		all = append(all[:len(all):len(all)], string(c.cur))
	}
	if len(all) > n {
		all = all[len(all)-n:]
	}
	return append([]string(nil), all...)
}

// layoutConsole draws the tail of git's output in a sunken well.
func (a *App) layoutConsole(gtx layout.Context, r image.Rectangle) {
	ui := a.ui
	reef.FillRect(gtx, r, ui.P.BgSunken)
	pad := gtx.Dp(reef.Sp2)
	line := ui.CodeRow(gtx)
	for i, l := range a.console.tail(consoleRows) {
		c := ui.P.Muted
		low := strings.ToLower(l)
		switch {
		case strings.HasPrefix(l, "$ "):
			c = ui.P.Fg
		case strings.HasPrefix(low, "fatal:") || strings.HasPrefix(low, "error:"):
			c = ui.P.Error
		}
		y := r.Min.Y + i*line
		fit(gtx, image.Pt(0, y), image.Pt(r.Max.X-pad, line), func(gtx layout.Context) {
			a.codeText(gtx, r.Min.X+pad, line, r.Max.X-pad, font.Normal, c, l)
		})
	}
}

// cloneJob is a clone or fetch in flight from a pasted link.
type cloneJob struct {
	src      clone.Source
	fetching bool // the clone was already there
	stop     context.CancelFunc
}

// cloneLink clones the repository a link names, or fetches the clone made
// from it before, and opens it — on the branch the link named, if any.
func (a *App) cloneLink(link string) {
	if a.cloning != nil || strings.TrimSpace(link) == "" {
		return
	}
	src, err := clone.Parse(link)
	if err != nil {
		a.openErr = err.Error()
		return
	}
	a.startClone(src)
}

// startClone clones or fetches a parsed source and opens it.
func (a *App) startClone(src clone.Source) {
	if a.cloning != nil {
		return
	}
	root := clone.ExpandRoot(a.settings.CloneDir)
	ctx, stop := context.WithCancel(context.Background())
	_, statErr := os.Stat(clone.Dir(root, src))
	job := &cloneJob{src: src, fetching: statErr == nil, stop: stop}
	a.cloning = job
	a.openErr = ""
	out := &console{changed: func() {
		if a.win != nil {
			a.win.Invalidate()
		}
	}}
	a.console = out
	// No time limit: a large repository takes as long as it takes, and Esc
	// is how someone who has had enough says so.
	a.backgroundIn(ctx, 0, func(ctx context.Context) func() {
		res, err := clone.Ensure(ctx, root, src, out)
		// Whether Esc stopped it is read now, before this job's context is
		// released below: releasing it cancels it too, and a finished clone
		// would then read as a stopped one.
		stopped := ctx.Err() != nil
		return func() {
			stop()
			if a.cloning != job {
				return
			}
			a.cloning = nil
			switch {
			case stopped:
				a.openErr = "stopped"
				return
			case err != nil:
				a.openErr = err.Error()
				return
			}
			// The output stays up until the repository is open, so a
			// failure to open it is read beside what git said.
			a.urlField.SetText("")
			a.openRepoOn(res.Dir, res.Branch, res.Warning)
		}
	})
}

// Clone opens the repository a link names, as a pasted link would. It is how
// a link given on the command line is taken.
func (a *App) Clone(link string) { a.after(func() { a.cloneLink(link) }) }

// stopClone abandons the clone in flight. Its partial directory is removed,
// so the next paste starts it over.
func (a *App) stopClone() bool {
	if a.cloning == nil {
		return false
	}
	a.cloning.stop()
	return true
}

// openRepo switches to another repository, keeping the window and the theme.
func (a *App) openRepo(path string) { a.openRepoOn(path, "", "") }

// openRepoOn opens a repository and, when branch is set, reviews that branch
// first. notice is said once it is open, in place of the usual line.
func (a *App) openRepoOn(path, branch, notice string) {
	a.background(func(ctx context.Context) func() {
		opened, err := repo.Open(ctx, path)
		if err != nil {
			return func() {
				a.openErr = err.Error()
				a.failure = err.Error()
			}
		}
		return func() {
			a.supersede()
			a.resetSonda()
			// A query is written in one tool's language, and means nothing,
			// or something else, to the other's.
			if a.repo == nil || a.repo.Info().Name != opened.Info().Name {
				a.revset = ""
				a.revsetInput.SetText("")
			}
			a.repo = opened
			a.dir = absDir(path, opened)
			a.adoptBackend(opened)
			a.store = state.New()
			a.repoName = filepath.Base(opened.Root())
			a.openErr = ""
			a.failure = ""
			a.console = nil
			a.recent = state.RememberRecent(opened.Root())
			a.recentSel = 0
			a.revs = nil
			a.files = nil
			a.diff = nil
			a.rev = vcs.Revision{}
			a.desc = ""
			a.focus = PaneRevs
			a.details, a.opened, a.current = nil, nil, ""
			a.reviewKey = ""
			if branch != "" {
				a.reviewKey = branchKey(branch)
			}
			a.setTitle()
			a.reload(true)
			switch {
			case notice != "":
				a.note("%s", notice)
			case branch != "":
				a.note("opened %s on %s", shortenHome(opened.Root()), branch)
			default:
				a.note("opened %s", opened.Root())
			}
		}
	})
}

// moveRecent steps through the recent list on the open screen.
func (a *App) moveRecent(delta int) {
	if len(a.recent) == 0 {
		return
	}
	a.recentSel = clamp(a.recentSel+delta, 0, len(a.recent)-1)
}

func (a *App) openSelectedRecent() {
	if a.recentSel < len(a.recent) {
		a.openRepo(a.recent[a.recentSel].Path)
	}
}
