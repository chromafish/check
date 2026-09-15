package ui

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/chromafish/check/internal/diffparse"
	"github.com/chromafish/check/internal/highlight"
	"github.com/chromafish/check/internal/state"
	"github.com/chromafish/check/internal/vcs"
)

// rowKind distinguishes the several things that can occupy a row of the diff
// pane.
type rowKind uint8

const (
	rowLine rowKind = iota
	rowHunk
	rowComment
	rowDraft
	rowNote
	rowFile
	rowGap
	rowPretty
)

// Row is one line of the diff pane. File headings, diff lines, hunk headers,
// the runs of unchanged code a diff left out, stored comments and the comment
// being written all share a single flat list, so that the pane can be
// virtualised and navigated with one cursor from the first file to the last.
type Row struct {
	Kind  rowKind
	Line  diffparse.Line
	Spans []highlight.Span
	Text  string
	// Comment is set on a rowComment and nil everywhere else. It is a pointer
	// because every row would otherwise carry a note-sized hole: on a large
	// diff that is the single biggest allocation in the program.
	Comment *state.Comment
	Hunk    int
	Noted   bool // a note covers this line, so the margin says so
	Gap     Gap
	Pretty  *PrettyBlock // set for rowPretty
}

// rowRef is a row's place in the change: which file it belongs to, and where
// in that file it sits. The sheet is one of these per row — eight bytes rather
// than a whole Row — because a large change has hundreds of thousands of them
// and copying every row into the sheet doubled what the diff cost to hold.
//
// A non-negative idx is a row of the file's own diff; a negative one is ^idx
// into the notes that file has picked up, which are the only rows the sheet
// adds to what the diff itself says.
type rowRef struct {
	file int32
	idx  int32
}

// Gap is a run of unchanged lines the diff did not print. Expanding one turns
// it into context: the lines are read from the file itself, so no second diff
// has to be fetched to see what surrounds a hunk.
type Gap struct {
	OldFrom int // first hidden line on the old side
	NewFrom int // and on the new side
	Count   int // how many lines are hidden, or -1 for "to the end of the file"
}

// Pair aligns rows into two columns for the side by side view. An index of -1
// leaves that side blank, in Left and Right alike; Span is the index of a row
// that runs across both columns, or -1 for an ordinary two column row.
type Pair struct {
	Left, Right int
	Span        int
}

// FileDoc is one file's part of the sheet: its heading, its hunks, and the
// gaps between them.
type FileDoc struct {
	Path string
	File *diffparse.File // nil when the diff had nothing for this path
	Note string          // shown instead of hunks for binary files and pure renames

	base  []Row // heading, hunks, lines and gaps, without comments
	notes []Row // the comment and draft rows folded in among them
	lines []string
	old   []string
	newHL highlight.Lines
	oldHL highlight.Lines
	// read means the file's own text has been fetched and lexed whole: gaps can
	// be expanded from it, and the colouring has the context to be right.
	read    bool
	loading bool
	waiting bool // the change's diff has not reached this file yet

	MaxCols int
	oldWide int // the largest line number each side of this file will show
	newWide int
	oldDig  int // and how many columns each side takes to print
	newDig  int

	IsDoc    bool
	PrettyOn bool
	Pretty   *PrettyDoc
}

// DiffDoc is the change as the diff pane reads it. Its Files are the manifest
// App.files in the same order and at the same indices — buildDoc is handed that
// list and walks it — so a file index means the same thing on either side.
//
// DiffDoc is the whole change, printed in sequence. Every file is here, one
// after another, because a review is read from the top. What a file costs is
// paid when it is reached — its text is fetched and lexed then — and nothing
// is thrown away when you leave it.
type DiffDoc struct {
	Files  []*FileDoc
	Rows   []rowRef
	Pairs  []Pair
	Cursor int

	Hunks    []int // indices into Rows of each hunk header
	FileRows []int // and of each file heading
	Digits   int   // width of the widest line number in the change
	MaxCols  int   // widest line, in character cells

	index  map[string]int // path to the file it names, for the diff arriving
	pairOf []int32        // the two-column line each row is shown on
}

// Row is the row at a place in the sheet. It is returned by value: the sheet
// holds references, and the rows themselves belong to the files.
func (d *DiffDoc) Row(i int) Row {
	return *d.rowPtr(i)
}

// rowPtr is Row without the copy, for the walks that touch every row.
func (d *DiffDoc) rowPtr(i int) *Row {
	r := d.Rows[i]
	fd := d.Files[r.file]
	if r.idx < 0 {
		return &fd.notes[^r.idx]
	}
	return &fd.base[r.idx]
}

// rowRange is the half-open range of Rows belonging to file i. Files are laid
// out in order, so a file's rows run from its heading to the next one.
func (d *DiffDoc) rowRange(i int) (start, end int) {
	if i < 0 || i >= len(d.FileRows) {
		return 0, 0
	}
	start = d.FileRows[i]
	end = len(d.Rows)
	if i+1 < len(d.FileRows) {
		end = d.FileRows[i+1]
	}
	return start, end
}

// FileOf is the index of the file a row belongs to, or -1.
func (d *DiffDoc) FileOf(row int) int {
	if row < 0 || row >= len(d.Rows) {
		return -1
	}
	i := int(d.Rows[row].file)
	if i < 0 || i >= len(d.Files) {
		return -1
	}
	return i
}

// FileAt reports which file a row belongs to.
func (d *DiffDoc) FileAt(row int) *FileDoc {
	if i := d.FileOf(row); i >= 0 {
		return d.Files[i]
	}
	return nil
}

// PathAt is the path a row belongs to. Comments are filed under it.
func (d *DiffDoc) PathAt(row int) string {
	if f := d.FileAt(row); f != nil {
		return f.Path
	}
	return ""
}

// diffBatch is how often the sheet takes in what the diff has produced so far.
// Often enough that a large change is visibly filling in, seldom enough that
// laying the files out costs less than reading them.
const diffBatch = 50 * time.Millisecond

// diffStall is how long the diff may go without producing a file before it is
// taken as wedged. It is a limit on silence rather than on size: a change of a
// million lines is slow because there is a lot of it, and waiting for the whole
// of one is the thing streaming exists to avoid.
const diffStall = 30 * time.Second

// loadDiff reads the diff of every file in the change and lays it out. The
// manifest already names every file, so the sheet goes up before any of the
// diff has arrived and each file's hunks appear under its own heading as they
// are read: a change with a large body can be read from the top while the rest
// of it is still being written.
//
// The per-file work that needs the file's own text — full-file lexing, and the
// lines behind a gap — is still done when a source file is reached. Documents
// are the exception: their full text is what the pretty view is made from, so
// they are read as soon as their diff arrives and are already rendered by the
// time scrolling reaches them.
func (a *App) loadDiff() {
	a.supersede()
	gen := a.generation
	spec := a.spec
	files := append([]FileRow(nil), a.files...)
	a.diff = nil
	if len(files) == 0 || spec.Empty() {
		return
	}

	ctx, stop := context.WithCancel(context.Background())
	a.stopDiff = stop

	a.backgroundIn(ctx, 0, func(ctx context.Context) func() {
		var stalled atomic.Bool
		stall := time.AfterFunc(diffStall, func() {
			stalled.Store(true)
			stop()
		})
		defer stall.Stop()

		stream, err := a.repo.DiffStream(ctx, spec, 3)
		if err != nil {
			return func() {
				if gen == a.generation {
					a.fail(err)
				}
			}
		}

		doc := buildDoc(files, nil)
		a.after(func() {
			if gen != a.generation {
				return
			}
			a.failure = ""
			a.diff = doc
			a.rebuildRows()
			a.showFile(a.fileSel)
		})

		// Files are handed over in batches rather than one at a time: laying out
		// a file costs a splice of the sheet, and a change of ten thousand small
		// files would otherwise spend the load doing that instead of reading.
		// Handing the batch over means handing over the slice: attach keeps a
		// pointer into it for every file it takes. Emptying it with batch[:0]
		// instead would have the files still to come overwrite the diffs
		// already on screen.
		var batch []diffparse.File
		sent := time.Now()
		send := func() {
			ready := batch
			batch = nil
			sent = time.Now()
			a.after(func() {
				if gen != a.generation || a.diff != doc {
					return
				}
				byPath := a.notesByPath()
				for i := range ready {
					if j := doc.attach(&ready[i]); j >= 0 {
						a.rebuildFileWith(j, byPath[doc.Files[j].Path])
						a.lightAttachedFile(j)
					}
				}
			})
		}
		speaking := func() {
			stalled.Store(false)
			stall.Reset(diffStall)
		}
		serr := diffparse.Scan(ticking{stream, speaking}, func(f diffparse.File) {
			batch = append(batch, f)
			if time.Since(sent) >= diffBatch {
				send()
			}
		})
		cerr := stream.Close()
		rest := batch

		return func() {
			if gen != a.generation || a.diff != doc {
				return
			}
			stop()
			a.stopDiff = nil
			byPath := a.notesByPath()
			for i := range rest {
				if j := doc.attach(&rest[i]); j >= 0 {
					a.rebuildFileWith(j, byPath[doc.Files[j].Path])
					a.lightAttachedFile(j)
				}
			}
			// Only a file the diff never mentioned is left to lay out, which
			// is normally none of them. The reading position is left alone:
			// the change has been readable the whole time it was arriving.
			if doc.settle() {
				a.rebuildRows()
			}
			doc.logParsed()
			a.lightFile(a.fileSel)
			if err := cmp.Or(serr, cerr); err != nil {
				if stalled.Load() {
					// The tool was killed, so what it says is "signal: killed".
					err = fmt.Errorf("the diff stopped responding after %s", diffStall)
				}
				a.fail(err)
			}
		}
	})
}

// logParsed sums the change up for the behaviour log once the whole diff is
// in: counts, never contents.
func (d *DiffDoc) logParsed() {
	if !slog.Default().Enabled(context.Background(), slog.LevelInfo) {
		return
	}
	files, hunks, added, removed, truncated := 0, 0, 0, 0, 0
	for _, fd := range d.Files {
		if fd.File == nil {
			continue
		}
		files++
		hunks += len(fd.File.Hunks)
		a, r := fd.File.Counts()
		added += a
		removed += r
		if fd.File.Truncated {
			truncated++
		}
	}
	slog.Info("diff parsed", "files", files, "hunks", hunks, "added", added, "removed", removed, "truncated", truncated)
}

// matchFile finds the parsed diff for a path. jj names a rename with both its
// paths, so either end identifies it.
func matchFile(files []diffparse.File, path string) *diffparse.File {
	for i := range files {
		if files[i].Path() == path || files[i].OldPath == path {
			return &files[i]
		}
	}
	return nil
}

// highlightSides fetches both versions of the file and lexes them whole.
// Lexing the complete file rather than the hunks is what makes a hunk that
// opens inside a block comment or a raw string come out right, and the same
// text is what a gap is expanded from.
func (a *App) highlightSides(ctx context.Context, spec vcs.DiffSpec, f *diffparse.File) (newSrc, oldSrc []byte, newHL, oldHL highlight.Lines) {
	if !f.IsDeleted {
		if src, err := a.repo.FileContent(ctx, spec, f.Path(), vcs.After); err == nil {
			newSrc = src
			newHL = highlight.File(f.Path(), src)
		}
	}
	if !f.IsNew {
		old := f.OldPath
		if old == "" {
			old = f.Path()
		}
		if src, err := a.repo.FileContent(ctx, spec, old, vcs.Before); err == nil {
			oldSrc = src
			oldHL = highlight.File(old, src)
		}
	}
	// Falling back to the hunk text keeps colour on merges and other cases
	// where neither side can be named as a single revision.
	if newHL == nil {
		newHL = highlightFromHunks(f, diffparse.Removed)
	}
	if oldHL == nil {
		oldHL = highlightFromHunks(f, diffparse.Added)
	}
	return newSrc, oldSrc, newHL, oldHL
}

// highlightFromHunks lexes one side of a file out of the diff alone, laid out
// at that side's real line numbers so lookups by number still work. Dropping
// the additions leaves the old file; dropping the removals leaves the new one.
func highlightFromHunks(f *diffparse.File, drop diffparse.Kind) highlight.Lines {
	var b strings.Builder
	last := 0
	numberOf := func(l diffparse.Line) int {
		if drop == diffparse.Removed {
			return l.NewNum
		}
		return l.OldNum
	}
	for i := range f.Hunks {
		for _, l := range f.Hunks[i].Lines {
			if l.Kind == drop {
				continue
			}
			n := numberOf(l)
			if n <= last {
				continue
			}
			for last+1 < n {
				b.WriteByte('\n')
				last++
			}
			b.WriteString(l.Text)
			b.WriteByte('\n')
			last = n
		}
	}
	if b.Len() == 0 {
		return nil
	}
	return highlight.File(f.Path(), []byte(b.String()))
}

// buildDoc flattens every file of the change into one list of rows. With no
// parsed diff it lays out a change whose body has not arrived: every file is
// there under its heading, waiting for its own, and attach fills them in.
func buildDoc(files []FileRow, parsed []diffparse.File) *DiffDoc {
	doc := &DiffDoc{Digits: 1, index: make(map[string]int, len(files))}
	for i, fc := range files {
		fd := &FileDoc{Path: fc.Path, File: matchFile(parsed, fc.Path), waiting: parsed == nil}
		fd.IsDoc = IsDoc(fc.Path)
		fd.PrettyOn = fd.IsDoc
		buildFile(fd, i)
		doc.Files = append(doc.Files, fd)
		if _, seen := doc.index[fc.Path]; !seen {
			doc.index[fc.Path] = i
		}
		doc.MaxCols = max(doc.MaxCols, fd.MaxCols)
	}
	doc.widen()
	return doc
}

// widen sets each file's line-number columns to the largest number that side
// will show, and the change's to the largest of those. They grow over the life
// of a change: a diff read as it arrives learns it file by file, a truncated
// file fetched later brings its own, and a gap opened past the last hunk
// reaches lines no hunk header named.
func (d *DiffDoc) widen() {
	widest := 0
	for _, fd := range d.Files {
		fd.oldDig, fd.newDig = digitsOf(fd.oldWide), digitsOf(fd.newWide)
		widest = max(widest, fd.oldWide, fd.newWide)
	}
	d.Digits = max(1, len(strconv.Itoa(widest)))
}

// digitsOf is how many columns a line number takes, and zero for a side that
// has none: an added file has no old numbers, a deleted one no new.
func digitsOf(n int) int {
	if n <= 0 {
		return 0
	}
	return len(strconv.Itoa(n))
}

// digitsAt is the width of each number column for the file row i belongs to.
// The columns are sized per file rather than from the largest number anywhere
// in the change, so a short file keeps a short gutter.
func (d *DiffDoc) digitsAt(i int) (old, new int) {
	if i < 0 || i >= len(d.Rows) {
		return 0, 0
	}
	fd := d.Files[d.Rows[i].file]
	return fd.oldDig, fd.newDig
}

// attach puts a file's diff under its heading, once the diff has reached it.
// It reports which file that was, or -1 for a path the manifest does not list.
func (d *DiffDoc) attach(f *diffparse.File) int {
	i, ok := d.index[f.Path()]
	if !ok {
		i, ok = d.index[f.OldPath]
	}
	if !ok {
		return -1
	}
	fd := d.Files[i]
	fd.File = f
	fd.waiting = false
	buildFile(fd, i)
	d.widen()
	return i
}

// settle closes the change off once its diff has stopped arriving: a file the
// diff never mentioned is no longer waiting for one, and says so. It reports
// whether any file changed, and so whether the sheet has to be laid out again.
func (d *DiffDoc) settle() bool {
	changed := false
	for i, fd := range d.Files {
		if !fd.waiting {
			continue
		}
		fd.waiting = false
		buildFile(fd, i)
		changed = true
	}
	return changed
}

// ticking passes a diff through and reports that it is still coming. The
// watchdog over a streamed diff is a limit on silence from the tool, so it has
// to be fed by the bytes read: a single very large file yields nothing to
// parse until the file after it begins.
type ticking struct {
	r    io.Reader
	tick func()
}

func (t ticking) Read(b []byte) (int, error) {
	n, err := t.r.Read(b)
	if n > 0 {
		t.tick()
	}
	return n, err
}

// fileNote stands in for a file's hunks where there are none to show. A file
// still waiting for its diff gets nothing, so the heading is all that appears
// until the hunks do.
func fileNote(fd *FileDoc) string {
	f := fd.File
	switch {
	case f == nil:
		if fd.waiting {
			return ""
		}
		return "NO DIFF CONTENT"
	case f.IsBinary:
		return "BINARY FILE"
	case f.Truncated:
		return "LARGE DIFF · " + thousands(f.LineCount) + " LINES NOT SHOWN"
	case len(f.Hunks) == 0 && f.IsRename:
		return "RENAMED WITHOUT CHANGES"
	case len(f.Hunks) == 0:
		return "NO TEXTUAL CHANGES"
	}
	return ""
}

// hunkExtents is the largest line number the file's hunks name on each side. A
// side the file has no lines on stays at zero.
func hunkExtents(f *diffparse.File) (old, new int) {
	if f == nil {
		return 0, 0
	}
	for i := range f.Hunks {
		h := &f.Hunks[i]
		if h.OldCount > 0 {
			old = max(old, h.OldStart+h.OldCount)
		}
		if h.NewCount > 0 {
			new = max(new, h.NewStart+h.NewCount)
		}
	}
	return old, new
}

// layoutSize is how many rows a file needs: a heading, each hunk with the gap
// before it, its lines, and one gap for what the diff left out at the end.
// Growing into that beats letting append double its way up from nothing, which
// on a file of several hundred thousand lines copies the rows twenty times.
func layoutSize(f *diffparse.File) int {
	if f == nil {
		return 2
	}
	n := 2 + 2*len(f.Hunks)
	for i := range f.Hunks {
		n += len(f.Hunks[i].Lines)
	}
	return n
}

// buildFile lays out one file: a heading, then its hunks with the runs the
// diff left out marked between them.
//
// The rows are built fresh and put in place at the end. The sheet holds indices
// into them, so a file laid out in place would leave every index past its new
// length pointing at nothing until the sheet was rebuilt.
func buildFile(fd *FileDoc, index int) {
	f := fd.File
	base := make([]Row, 0, layoutSize(f))
	cols := 0
	fd.oldWide, fd.newWide = hunkExtents(f)
	row := func(r Row) {
		base = append(base, r)
	}
	row(Row{Kind: rowFile})

	fd.Note = fileNote(fd)
	// Binary/truncated files still show the note path; pretty rendering applies
	// only with text content.
	canPretty := fd.IsDoc && fd.PrettyOn && fd.Pretty != nil && len(fd.Pretty.Blocks) > 0 && fd.Note == "" && !fd.waiting && f != nil && !f.IsBinary && !f.Truncated
	if canPretty {
		buildPretty(fd, row, &cols)
		fd.base, fd.MaxCols = base, cols
		return
	}
	// A document defaults to its rendered form. While its full text is being
	// fetched, do not briefly expose the raw hunk layout that the rendered rows
	// will replace — that made scrolling across a file boundary visibly jump.
	// Once a read has completed, a missing Pretty value means parsing failed and
	// the raw diff remains the useful fallback promised by the spec.
	pendingPretty := fd.IsDoc && fd.PrettyOn && fd.Pretty == nil && !fd.read && fd.Note == "" && !fd.waiting && f != nil && !f.IsDeleted && !f.IsBinary && !f.Truncated
	if pendingPretty {
		row(Row{Kind: rowNote, Text: "RENDERING DOCUMENT…"})
		fd.base, fd.MaxCols = base, cols
		return
	}
	switch {
	case fd.Note != "":
		row(Row{Kind: rowNote, Text: fd.Note})

	case f == nil:
		// Still waiting for this file's diff. The heading stands alone until it
		// lands: a file the change names is there to be seen either way, and
		// saying "loading" under each of them would be louder than the wait.

	default:
		buildHunks(f, row, &cols)
	}
	fd.base, fd.MaxCols = base, cols
}

func buildPretty(fd *FileDoc, row func(Row), cols *int) {
	if fd.Pretty == nil {
		return
	}
	for i := range fd.Pretty.Blocks {
		b := &fd.Pretty.Blocks[i]
		w := estimatePrettyWidth(b)
		*cols = max(*cols, w)
		row(Row{
			Kind:   rowPretty,
			Line:   diffparse.Line{OldNum: b.StartLine, NewNum: b.StartLine, Text: ""},
			Hunk:   i,
			Pretty: b,
		})
	}
}

func estimatePrettyWidth(b *PrettyBlock) int {
	switch b.Kind {
	case BlockCode:
		maxW := 0
		for _, line := range strings.Split(b.Code, "\n") {
			maxW = max(maxW, displayWidth(line))
		}
		return maxW
	case BlockPara, BlockHeading, BlockQuote:
		w := 0
		for _, inl := range b.Inlines {
			w += displayWidth(inl.Text)
		}
		if w == 0 {
			w = displayWidth(b.Code)
		}
		return w
	case BlockList:
		maxW := 0
		for _, item := range b.Items {
			w := 2 // bullet/number + space
			for _, inl := range item {
				w += displayWidth(inl.Text)
			}
			maxW = max(maxW, w)
		}
		return maxW
	case BlockTable:
		w := 0
		for _, cell := range b.TableHead {
			for _, inl := range cell {
				w += displayWidth(inl.Text) + 3
			}
		}
		for _, row := range b.TableRows {
			rw := 0
			for _, cell := range row {
				for _, inl := range cell {
					rw += displayWidth(inl.Text) + 3
				}
			}
			w = max(w, rw)
		}
		return w
	case BlockFrontmatter:
		w := 0
		for _, e := range b.Frontmatter {
			w = max(w, displayWidth(e.Key+e.ValueStr)+3)
		}
		return w
	default:
		return 20
	}
}

// buildHunks lays out the body of a file that has one: each hunk, with the run
// the diff left out before it.
func buildHunks(f *diffparse.File, row func(Row), cols *int) {
	for hi := range f.Hunks {
		h := &f.Hunks[hi]
		// What the diff left out above this hunk. The first hunk's gap runs
		// from the top of the file; every other one from the end of the hunk
		// before it.
		oldFrom, newFrom := 1, 1
		if hi > 0 {
			prev := &f.Hunks[hi-1]
			oldFrom = prev.OldStart + prev.OldCount
			newFrom = prev.NewStart + prev.NewCount
		}
		// Whichever side the file has: a deletion's hunks say nothing about the
		// new side, and an addition's nothing about the old, so the gap before
		// a hunk is however much either side left out.
		if n := max(h.NewStart-newFrom, h.OldStart-oldFrom); n > 0 {
			row(Row{Kind: rowGap, Hunk: hi, Gap: Gap{OldFrom: oldFrom, NewFrom: newFrom, Count: n}})
		}

		row(Row{Kind: rowHunk, Text: hunkLabel(h), Hunk: hi})
		for _, l := range h.Lines {
			*cols = max(*cols, displayWidth(l.Text))
			row(Row{Kind: rowLine, Line: l, Hunk: hi})
		}
	}

	// And what it left out below the last hunk. How much that is only becomes
	// known once the file itself has been read, so it is left open.
	last := &f.Hunks[len(f.Hunks)-1]
	row(Row{Kind: rowGap, Hunk: len(f.Hunks) - 1, Gap: Gap{
		OldFrom: last.OldStart + last.OldCount,
		NewFrom: last.NewStart + last.NewCount,
		Count:   -1,
	}})
}

func (fd *FileDoc) applyHighlight(newHL, oldHL highlight.Lines) {
	applySpans(fd.base, newHL, oldHL)
}

// applySpans colours a run of rows from a file's two lexed sides. An added
// line is only in the new file and a removed one only in the old; a context
// line is in both, and is looked up in whichever was read.
func applySpans(rows []Row, newHL, oldHL highlight.Lines) {
	for i := range rows {
		r := &rows[i]
		if r.Kind != rowLine {
			continue
		}
		switch r.Line.Kind {
		case diffparse.Added:
			r.Spans = newHL.Line(r.Line.NewNum)
		case diffparse.Removed:
			r.Spans = oldHL.Line(r.Line.OldNum)
		default:
			r.Spans = newHL.Line(r.Line.NewNum)
			if r.Spans == nil {
				r.Spans = oldHL.Line(r.Line.OldNum)
			}
		}
	}
}

// showWhole fetches a file the change's diff left out and puts it back. The
// ceilings keep a change openable; this overrides them for a single file.
func (a *App) showWhole(i int) {
	doc := a.diff
	if doc == nil || i < 0 || i >= len(doc.Files) {
		return
	}
	fd := doc.Files[i]
	if fd.loading || fd.File == nil || !fd.File.Truncated {
		return
	}
	fd.loading = true
	gen, spec, path := a.generation, a.spec, fd.Path
	a.note("reading %s…", path)

	a.background(func(ctx context.Context) func() {
		raw, err := a.repo.FileDiff(ctx, spec, path, 3)
		var parsed []diffparse.File
		if err == nil {
			parsed = diffparse.ParseOne(raw)
		}
		return func() {
			fd.loading = false
			if gen != a.generation || a.diff != doc {
				return
			}
			if err != nil {
				a.fail(err)
				return
			}
			f := matchFile(parsed, path)
			if f == nil || f.Truncated {
				a.note("%s could not be read", path)
				return
			}
			fd.File = f
			fd.Note = ""
			buildFile(fd, i)
			fd.applyHighlight(fd.newHL, fd.oldHL)
			a.rebuildFile(i)
			a.showFile(i)
			a.lightFile(i)
			added, removed := f.Counts()
			a.note("%s · +%d −%d", path, added, removed)
		}
	})
}

// lightAttachedFile starts the full-text work needed immediately after a diff
// file lands. Ordinary source files keep the old lazy behaviour and are read
// only when selected; renderable documents are read eagerly because pretty is
// their default view and must not depend on scroll position.
func (a *App) lightAttachedFile(i int) {
	doc := a.diff
	if doc == nil || i < 0 || i >= len(doc.Files) {
		return
	}
	fd := doc.Files[i]
	if i == a.fileSel || shouldLightDocument(fd) {
		a.lightFile(i)
	}
}

func shouldLightDocument(fd *FileDoc) bool {
	if fd == nil || !fd.IsDoc || !fd.PrettyOn || fd.read || fd.loading || fd.Note != "" {
		return false
	}
	f := fd.File
	return f != nil && !f.IsDeleted && !f.IsBinary && !f.Truncated
}

// lightFile reads and lexes one file's text. It is called for a source file
// under the cursor, and as soon as the diff arrives for a renderable document.
func (a *App) lightFile(i int) {
	doc := a.diff
	if doc == nil || i < 0 || i >= len(doc.Files) {
		return
	}
	fd := doc.Files[i]
	if fd.read || fd.loading || fd.File == nil || fd.File.IsBinary {
		return
	}
	fd.loading = true
	gen, spec, f := a.generation, a.spec, fd.File

	a.background(func(ctx context.Context) func() {
		newSrc, oldSrc, newHL, oldHL := a.highlightSides(ctx, spec, f)
		return func() {
			fd.loading = false
			if gen != a.generation || a.diff != doc {
				return
			}
			slog.Info("file selected", "path", fd.Path, "hunks", len(f.Hunks), "syntax", cmp.Or(highlight.Syntax(fd.Path), "none"))
			fd.lines = splitLines(newSrc)
			fd.old = splitLines(oldSrc)
			fd.newHL, fd.oldHL = newHL, oldHL
			fd.read = true
			// Try pretty rendering for docs. Whether parsing succeeds or not, lay
			// the file out again after the read: before it completed the default
			// pretty view deliberately showed only its rendering placeholder.
			if fd.IsDoc && len(newSrc) > 0 {
				if pretty, err := buildPrettyDoc(fd.Path, newSrc); err == nil && pretty != nil {
					// Map hunks to blocks for overlay markers.
					markChanges(pretty, f)
					// If Before was unavailable, overlay shows only additions: clear del marking by re-marking?
					if len(oldSrc) == 0 {
						for j := range pretty.Blocks {
							if pretty.Blocks[j].Change == ChangeRemoved {
								pretty.Blocks[j].Change = ChangeContext
							}
						}
					}
					fd.Pretty = pretty
				}
			}
			if fd.IsDoc && fd.PrettyOn && fd.Note == "" && !f.IsBinary && !f.Truncated {
				buildFile(fd, i)
				a.rebuildFile(i)
			}
			// The sheet points at these rows, so colouring them is all there
			// is to do: no rebuild, and nothing to keep in step.
			fd.applyHighlight(newHL, oldHL)
		}
	})
}

// togglePretty flips the pretty/raw view for a doc file.
func (a *App) togglePretty(i int) {
	doc := a.diff
	if doc == nil || i < 0 || i >= len(doc.Files) {
		return
	}
	fd := doc.Files[i]
	if !fd.IsDoc {
		return
	}
	fd.PrettyOn = !fd.PrettyOn
	// If turning pretty on but we haven't rendered yet and file has been read, try to build.
	if fd.PrettyOn && fd.Pretty == nil && fd.read && len(fd.lines) > 0 && fd.File != nil && !fd.File.IsBinary {
		src := []byte(strings.Join(fd.lines, "\n"))
		if pretty, err := buildPrettyDoc(fd.Path, src); err == nil {
			markChanges(pretty, fd.File)
			fd.Pretty = pretty
		}
	}
	buildFile(fd, i)
	a.rebuildFile(i)
	if fd.PrettyOn {
		a.note("pretty on for %s", fd.Path)
	} else {
		a.note("raw for %s", fd.Path)
	}
}

// splitLines cuts a file into lines without keeping a trailing empty one for
// the final newline.
func splitLines(src []byte) []string {
	if len(src) == 0 {
		return nil
	}
	s := string(src)
	s = strings.TrimSuffix(s, "\n")
	return strings.Split(s, "\n")
}

// expandStep is how much of a gap one press opens: enough to see what a
// function does around a hunk, not so much that the change is lost in it.
const expandStep = 20

// expandGap opens a run of unchanged lines the diff left out. A count of -1
// opens the whole run. The lines come from the file's own text, so this needs
// no round trip once the file has been read, and asks for it if it has not.
func (a *App) expandGap(rowIndex, count int) {
	doc := a.diff
	if doc == nil || rowIndex < 0 || rowIndex >= len(doc.Rows) {
		return
	}
	r := doc.Row(rowIndex)
	file := doc.FileOf(rowIndex)
	if r.Kind != rowGap {
		return
	}
	fd := doc.FileAt(rowIndex)
	if fd == nil {
		return
	}
	if !fd.read {
		a.lightFile(file)
		a.note("reading %s…", fd.Path)
		return
	}
	if !fd.expand(r.Gap, count) {
		a.note("%s could not be read", fd.Path)
		return
	}
	a.rebuildFile(file)
}

// collapseFile puts a file back to the hunks the diff actually names, which is
// the other half of the whole-file toggle.
func (a *App) collapseFile(i int) {
	doc := a.diff
	if doc == nil || i < 0 || i >= len(doc.Files) {
		return
	}
	fd := doc.Files[i]
	buildFile(fd, i)
	fd.applyHighlight(fd.newHL, fd.oldHL)
	a.rebuildFile(i)
	a.showFile(i)
	a.note("hunks only")
}

// expandFile opens every gap in one file, which is the whole file shown.
func (a *App) expandFile(i int) {
	doc := a.diff
	if doc == nil || i < 0 || i >= len(doc.Files) {
		return
	}
	fd := doc.Files[i]
	if !fd.read {
		a.lightFile(i)
		a.note("reading %s…", fd.Path)
		return
	}
	before := len(fd.base)
	if !fd.expandAll() {
		a.note("%s could not be read", fd.Path)
		return
	}
	a.rebuildFile(i)
	if len(fd.base) > before {
		a.note("whole file")
		return
	}
	a.note("nothing more to show in %s", fd.Path)
}

// Collapsed reports whether a file still has anything left out.
func (fd *FileDoc) Collapsed() bool {
	_, found := fd.firstGap()
	return found
}

func (fd *FileDoc) firstGap() (Gap, bool) {
	for _, r := range fd.base {
		if r.Kind == rowGap {
			return r.Gap, true
		}
	}
	return Gap{}, false
}

// source is the side the expanded context lines are read from: the new file
// where there is one, and the old file for a deletion.
func (fd *FileDoc) source() []string {
	if len(fd.lines) > 0 {
		return fd.lines
	}
	return fd.old
}

// hidden is how many lines a gap covers. A gap left open at the end of the
// file learns its length here, now that the file has been read.
func (fd *FileDoc) hidden(g Gap) int {
	if g.Count >= 0 {
		return g.Count
	}
	src := fd.source()
	if len(fd.lines) == 0 {
		return len(src) - g.OldFrom + 1
	}
	return len(src) - g.NewFrom + 1
}

// gapLines reads the head of a gap out of the file's own text: count lines of
// it, or all of them for -1. It also reports what stays hidden behind them.
func (fd *FileDoc) gapLines(g Gap, count int) (lines []Row, rest Gap, keep bool) {
	total := fd.hidden(g)
	if total <= 0 {
		return nil, Gap{}, false
	}
	take := count
	if take < 0 || take > total {
		take = total
	}

	lines = make([]Row, 0, take)
	for n := range take {
		oldNum, newNum := g.OldFrom+n, g.NewFrom+n
		text := ""
		switch {
		case len(fd.lines) > 0 && newNum-1 < len(fd.lines):
			text = fd.lines[newNum-1]
		case len(fd.old) > 0 && oldNum-1 < len(fd.old):
			text = fd.old[oldNum-1]
		default:
			take = n
			lines = lines[:n]
		}
		if take == n {
			break
		}
		spans := fd.newHL.Line(newNum)
		if spans == nil {
			spans = fd.oldHL.Line(oldNum)
		}
		lines = append(lines, Row{
			Kind:  rowLine,
			Line:  diffparse.Line{Kind: diffparse.Context, Text: text, OldNum: oldNum, NewNum: newNum},
			Spans: spans,
		})
		fd.oldWide, fd.newWide = max(fd.oldWide, oldNum), max(fd.newWide, newNum)
		fd.MaxCols = max(fd.MaxCols, displayWidth(text))
	}
	if len(lines) == 0 {
		return nil, Gap{}, false
	}
	rest = Gap{OldFrom: g.OldFrom + take, NewFrom: g.NewFrom + take, Count: total - take}
	return lines, rest, rest.Count > 0
}

// expand turns the head of one gap into context lines. It reports whether
// anything moved.
func (fd *FileDoc) expand(g Gap, count int) bool {
	if len(fd.source()) == 0 {
		return false
	}
	lines, rest, keep := fd.gapLines(g, count)
	return fd.replaceGap(g, lines, rest, keep)
}

// expandAll opens every gap in the file in one pass. Opening them one at a
// time lays the file out again for each, so a file with hundreds of hunks paid
// a copy of itself per hunk to be shown whole.
func (fd *FileDoc) expandAll() bool {
	if len(fd.source()) == 0 {
		return false
	}
	n := len(fd.base)
	for _, r := range fd.base {
		if r.Kind == rowGap {
			n += max(fd.hidden(r.Gap), 0)
		}
	}
	out := make([]Row, 0, n)
	opened := false
	for _, r := range fd.base {
		if r.Kind != rowGap {
			out = append(out, r)
			continue
		}
		opened = true
		lines, _, _ := fd.gapLines(r.Gap, -1)
		for _, l := range lines {
			l.Hunk = r.Hunk
			out = append(out, l)
		}
	}
	if !opened {
		return false
	}
	fd.base = out
	return true
}

// replaceGap swaps a gap row for the lines it was hiding, keeping whatever is
// still hidden as a smaller gap. Only the rows after it move, and they move in
// place, so opening a gap costs the tail rather than the whole file.
func (fd *FileDoc) replaceGap(g Gap, lines []Row, rest Gap, keep bool) bool {
	i := slices.IndexFunc(fd.base, func(r Row) bool {
		return r.Kind == rowGap && r.Gap == g
	})
	if i < 0 {
		return false
	}
	gapRow := fd.base[i]
	repl := make([]Row, 0, len(lines)+1)
	for _, l := range lines {
		l.Hunk = gapRow.Hunk
		repl = append(repl, l)
	}
	if keep {
		gapRow.Gap = rest
		repl = append(repl, gapRow)
	}
	// The context that has just appeared has no colouring of its own yet; it
	// gets it when the file's lexing lands, which for an expanded gap has
	// already happened.
	fd.base = splice(fd.base, i, i+1, repl)
	return true
}

func hunkLabel(h *diffparse.Hunk) string {
	label := "@@ -" + strconv.Itoa(h.OldStart) + "," + strconv.Itoa(h.OldCount) +
		" +" + strconv.Itoa(h.NewStart) + "," + strconv.Itoa(h.NewCount) + " @@"
	if h.Section != "" {
		label += "  " + h.Section
	}
	return label
}

// rebuildRows folds the stored comments, and any comment being written,
// into the diff rows. It runs whenever a comment changes or a gap opens, and
// never refetches the diff itself.
func (a *App) rebuildRows() {
	doc := a.diff
	if doc == nil {
		return
	}
	// Every row index is about to change. The reading position is remembered
	// as a file and a distance into it, which the rebuild does not move, and
	// put back afterwards.
	cursor := doc.place(doc.Cursor)
	first := doc.place(a.diffList.Position.First)
	anchor := doc.place(a.sel.Anchor.Row)
	head := doc.place(a.sel.Head.Row)

	total := 0
	for _, fd := range doc.Files {
		total += len(fd.base)
	}
	rows := make([]rowRef, 0, total+8)
	doc.Hunks = doc.Hunks[:0]
	doc.FileRows = doc.FileRows[:0]
	doc.MaxCols = 0

	byPath := a.notesByPath()
	for i, fd := range doc.Files {
		doc.MaxCols = max(doc.MaxCols, fd.MaxCols)
		doc.FileRows = append(doc.FileRows, len(rows))
		one, hunks := a.fileRefs(fd, byPath[fd.Path], i)
		for _, h := range hunks {
			doc.Hunks = append(doc.Hunks, len(rows)+h)
		}
		rows = append(rows, one...)
	}
	doc.Rows = rows
	doc.widen()
	doc.Cursor = doc.locate(cursor)
	a.diffList.Position.First = doc.locate(first)
	a.lastFirst = a.diffList.Position.First
	a.sel.Anchor.Row, a.sel.Head.Row = doc.locate(anchor), doc.locate(head)
	doc.Pairs, doc.pairOf = nil, nil
	a.rebuildFind()
}

// spot is a place in the change that outlives the sheet it was read from:
// which file, and how far into it. A rebuild renumbers every row but moves no
// file's contents, so a spot names the same code before and after one.
type spot struct{ file, off int }

func (d *DiffDoc) place(row int) spot {
	if row < 0 || row >= len(d.Rows) {
		return spot{}
	}
	i := int(d.Rows[row].file)
	start, _ := d.rowRange(i)
	return spot{file: i, off: row - start}
}

// locate is place run backwards. A file that shrank under the spot gives back
// its last row, which is the same rule shiftRow applies to a splice.
func (d *DiffDoc) locate(s spot) int {
	if s.file < 0 || s.file >= len(d.FileRows) {
		return 0
	}
	start, end := d.rowRange(s.file)
	return clamp(start+s.off, start, max(start, end-1))
}

// pairs is the two-column alignment, worked out the first time it is asked
// for. Only the side by side view reads it, so it costs nothing while that is
// off.
func (d *DiffDoc) pairs() []Pair {
	if d.Pairs == nil {
		d.Pairs = pairRows(d)
		d.pairOf = make([]int32, len(d.Rows))
		for i, p := range d.Pairs {
			for _, row := range [...]int{p.Span, p.Left, p.Right} {
				if row >= 0 && row < len(d.pairOf) {
					d.pairOf[row] = int32(i)
				}
			}
		}
	}
	return d.Pairs
}

// pairFor is the two-column line a row is shown on. The row position is what
// survives a rebuild and a change of view, so moving between the two views is
// a matter of converting it rather than keeping two of them in step.
func (d *DiffDoc) pairFor(row int) int {
	d.pairs()
	if row < 0 || row >= len(d.pairOf) {
		return 0
	}
	return int(d.pairOf[row])
}

// rowOfPair is the row a two-column line stands for: the one it runs across,
// or the left of the two it shows.
func rowOfPair(p Pair) int {
	switch {
	case p.Span >= 0:
		return p.Span
	case p.Left >= 0:
		return p.Left
	default:
		return p.Right
	}
}

func (a *App) rebuildFile(i int) {
	doc := a.diff
	if doc == nil || i < 0 || i >= len(doc.Files) {
		a.rebuildRows()
		return
	}
	a.rebuildFileWith(i, a.store.Comments(a.rev.ChangeIDFull, doc.Files[i].Path))
}

// rebuildFileWith puts one file's rows back together and splices them in, for
// the changes that reach only that file: a gap opened, a whole file shown, a
// diff fetched after the change left it out. Everything after it shifts, a move
// rather than a rebuild, and the other files are not walked at all.
//
// The file's notes come in from the caller: one rebuilding several files at a
// time groups the change once and hands out an entry per file, where a single
// rebuild asks the store for just the path it needs.
func (a *App) rebuildFileWith(i int, comments []state.Comment) {
	doc := a.diff
	if doc == nil || i < 0 || i >= len(doc.Files) || i >= len(doc.FileRows) {
		a.rebuildRows()
		return
	}
	fd := doc.Files[i]
	start, end := doc.rowRange(i)
	one, hunks := a.fileRefs(fd, comments, i)

	delta := len(one) - (end - start)
	doc.Rows = splice(doc.Rows, start, end, one)

	// The headings and hunk marks after this file all move by the same amount.
	for j := i + 1; j < len(doc.FileRows); j++ {
		doc.FileRows[j] += delta
	}
	// Hunks stays in order: the ones before this file, then its own, then the
	// ones after, moved by however much it grew or shrank.
	marks := make([]int, 0, len(doc.Hunks)+len(hunks))
	for _, h := range doc.Hunks {
		if h < start {
			marks = append(marks, h)
		}
	}
	for _, h := range hunks {
		marks = append(marks, start+h)
	}
	for _, h := range doc.Hunks {
		if h >= end {
			marks = append(marks, h+delta)
		}
	}
	doc.Hunks = marks

	doc.MaxCols = 0
	for _, f := range doc.Files {
		doc.MaxCols = max(doc.MaxCols, f.MaxCols)
	}
	doc.widen()
	// The reading position moves with the rows under it. Rows appearing above
	// — a note on an earlier file, a gap opened, the diff of a file still
	// arriving — would otherwise slide the sheet out from under the cursor and
	// leave the manifest naming a file the pane is not showing.
	doc.Cursor = shiftRow(doc.Cursor, start, end, delta)
	a.diffList.Position.First = shiftRow(a.diffList.Position.First, start, end, delta)
	a.lastFirst = shiftRow(a.lastFirst, start, end, delta)
	// The selection names rows too, and copying or filing a note against the
	// wrong lines is worse than losing it.
	a.sel.Anchor.Row = shiftRow(a.sel.Anchor.Row, start, end, delta)
	a.sel.Head.Row = shiftRow(a.sel.Head.Row, start, end, delta)
	doc.Pairs, doc.pairOf = nil, nil
	a.rebuildFind()
}

// shiftRow moves a row index across a splice: one before the rebuilt file stays
// where it is, one after it moves by however much the file grew or shrank, and
// one inside it stays inside it.
func shiftRow(row, start, end, delta int) int {
	switch {
	case row < start:
		return row
	case row >= end:
		return row + delta
	default:
		return clamp(row, start, max(start, end+delta-1))
	}
}

// splice replaces s[start:end] with one, moving the tail exactly once and in
// place. Building the result with append instead would copy the tail twice and
// allocate a second copy of the whole slice.
func splice[T any](s []T, start, end int, one []T) []T {
	delta := len(one) - (end - start)
	switch {
	case delta == 0:
		copy(s[start:end], one)
	case delta < 0:
		copy(s[start:], one)
		copy(s[start+len(one):], s[end:])
		s = s[:len(s)+delta]
	default:
		n := len(s)
		s = slices.Grow(s, delta)[:n+delta]
		copy(s[end+delta:], s[end:n])
		copy(s[start:], one)
	}
	return s
}

// notesByPath groups the change's notes by file. AllComments is already
// ordered by path and then by line, so each file gets its notes in the order
// they are printed. Asking the store once beats asking it per file: every call
// locks, copies and sorts the whole change.
func (a *App) notesByPath() map[string][]state.Comment {
	byPath := map[string][]state.Comment{}
	for _, c := range a.store.AllComments(a.rev.ChangeIDFull) {
		byPath[c.Path] = append(byPath[c.Path], c)
	}
	return byPath
}

// fileRefs points the sheet at one file's rows, with the notes on that file —
// and any note being written on it — folded in among them. The notes are the
// only rows the sheet adds; everything else already exists in the file.
//
// The mark down the edge of a noted line lives on the row itself, so it is set
// here rather than carried on a copy.
// The notes are built fresh and put in place at the end, for the reason
// buildFile does the same with the rows: the sheet holds indices into them.
// Nothing may read the sheet between this and the splice that takes the refs,
// because for that moment the two disagree about how many notes a file has.
func prettyCovers(c state.Comment, b *PrettyBlock) bool {
	if b == nil {
		return false
	}
	// Pretty blocks represent After side lines. For old side, still overlap via same numbers (approx).
	// We check if comment range overlaps block range.
	lo, hi := c.Line, c.LastLine()
	return b.StartLine <= hi && b.EndLine >= lo
}

func prettyAnchors(c state.Comment, b *PrettyBlock) bool {
	if b == nil {
		return false
	}
	last := c.LastLine()
	return b.StartLine <= last && b.EndLine >= last
}

func prettyAnchorsDraft(d *draft, b *PrettyBlock) bool {
	if d == nil || b == nil {
		return false
	}
	last := max(d.line, d.endLine)
	// Draft side matters but pretty is After; treat similarly to prettyCovers.
	// If draft is on old side, still anchor if line inside block.
	return b.StartLine <= last && b.EndLine >= last
}

func (a *App) fileRefs(fd *FileDoc, comments []state.Comment, index int) (refs []rowRef, hunks []int) {
	refs = make([]rowRef, 0, len(fd.base)+len(comments)+1)
	notes := make([]Row, 0, len(comments)+1)
	note := func(r Row) {
		notes = append(notes, r)
		refs = append(refs, rowRef{file: int32(index), idx: ^int32(len(notes) - 1)})
	}

	for i := range fd.base {
		row := &fd.base[i]
		if row.Kind == rowHunk {
			hunks = append(hunks, len(refs))
		}
		if row.Kind == rowLine {
			row.Noted = false
			for _, c := range comments {
				if c.Span() && covers(c, row.Line) {
					row.Noted = true
					break
				}
			}
		} else if row.Kind == rowPretty && row.Pretty != nil {
			row.Noted = false
			for _, c := range comments {
				if c.Span() && prettyCovers(c, row.Pretty) {
					row.Noted = true
					break
				}
			}
		}
		refs = append(refs, rowRef{file: int32(index), idx: int32(i)})
		if row.Kind == rowLine {
			for j := range comments {
				if anchors(comments[j], row.Line) {
					note(Row{Kind: rowComment, Comment: &comments[j], Hunk: row.Hunk})
				}
			}
			if d := a.draft; d != nil && d.path == fd.Path && anchorsDraft(d, row.Line) {
				note(Row{Kind: rowDraft, Hunk: row.Hunk})
			}
		} else if row.Kind == rowPretty && row.Pretty != nil {
			for j := range comments {
				if prettyAnchors(comments[j], row.Pretty) {
					note(Row{Kind: rowComment, Comment: &comments[j], Hunk: row.Hunk})
				}
			}
			if d := a.draft; d != nil && d.path == fd.Path && prettyAnchorsDraft(d, row.Pretty) {
				note(Row{Kind: rowDraft, Hunk: row.Hunk})
			}
		}
	}
	fd.notes = notes
	return refs, hunks
}

// anchors reports whether a comment belongs under a diff line. A note about a
// run of lines is printed under the last of them, where a reader arrives at it
// having just read what it is about.
func anchors(c state.Comment, l diffparse.Line) bool {
	return anchorsAt(c.Side, c.LastLine(), l)
}

func anchorsDraft(d *draft, l diffparse.Line) bool {
	return anchorsAt(d.side, max(d.line, d.endLine), l)
}

// anchorsAt is the shared half: a note on the old side hangs off the line
// numbers of the file as it was, and one on the new side off the file as it
// is, so in each case the lines the other side never had are skipped.
func anchorsAt(side state.Side, last int, l diffparse.Line) bool {
	if side == state.SideOld {
		return l.Kind != diffparse.Added && l.OldNum == last
	}
	return l.Kind != diffparse.Removed && l.NewNum == last
}

// covers reports whether a line falls inside a note's run. The mark down the
// edge of those lines is drawn from it.
func covers(c state.Comment, l diffparse.Line) bool {
	if c.Side == state.SideOld {
		return l.Kind != diffparse.Added && l.OldNum >= c.Line && l.OldNum <= c.LastLine()
	}
	return l.Kind != diffparse.Removed && l.NewNum >= c.Line && l.NewNum <= c.LastLine()
}

// pairRows aligns rows into two columns. Context lines sit opposite
// themselves; a replaced run is zipped together so a changed line faces the
// version it replaced; anything left over faces a blank.
func pairRows(d *DiffDoc) []Pair {
	var pairs []Pair
	// A run of removals faces the run of additions after it, so the alignment
	// only ever asks whether the next row continues the run it is in.
	runs := func(i int, k diffparse.Kind) bool {
		if i >= len(d.Rows) {
			return false
		}
		r := d.rowPtr(i)
		return r.Kind == rowLine && r.Line.Kind == k
	}
	for i := 0; i < len(d.Rows); {
		r := d.rowPtr(i)
		if r.Kind != rowLine {
			pairs = append(pairs, Pair{Left: -1, Right: -1, Span: i})
			i++
			continue
		}
		switch r.Line.Kind {
		case diffparse.Context:
			pairs = append(pairs, Pair{Left: i, Right: i, Span: -1})
			i++
		case diffparse.Removed:
			start := i
			for runs(i, diffparse.Removed) {
				i++
			}
			dels := i - start
			addStart := i
			for runs(i, diffparse.Added) {
				i++
			}
			adds := i - addStart
			for n := 0; n < max(dels, adds); n++ {
				p := Pair{Left: -1, Right: -1, Span: -1}
				if n < dels {
					p.Left = start + n
				}
				if n < adds {
					p.Right = addStart + n
				}
				pairs = append(pairs, p)
			}
		default: // an addition with nothing removed before it
			pairs = append(pairs, Pair{Left: -1, Right: i, Span: -1})
			i++
		}
	}
	return pairs
}
