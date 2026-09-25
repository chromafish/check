package ui

import (
	"fmt"
	"image"
	"strconv"
	"strings"
	"unicode/utf8"

	"gioui.org/font"
	"gioui.org/io/event"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"

	"github.com/chromafish/check/internal/diffparse"
	"github.com/chromafish/check/internal/highlight"

	"github.com/chromafish/check/reef"
)

// tabWidth is how far a tab advances in the diff body.
const tabWidth = 4

// actionCols is the width, in characters, of the strip at the left of the
// gutter that carries the comment affordance.
const actionCols = 1

// prettyFocused reports whether the file the reader is focused on is a doc
// being shown in pretty mode. When that is the case the CHANGE/COMMIT/AUTHOR
// meta header is hidden so the pretty-rendered document is the focus.
func (a *App) prettyFocused() bool {
	doc := a.diff
	if doc == nil || len(doc.Files) == 0 {
		return false
	}
	isPretty := func(idx int) bool {
		if idx < 0 || idx >= len(doc.Files) {
			return false
		}
		fd := doc.Files[idx]
		return fd != nil && fd.IsDoc && fd.PrettyOn && fd.Pretty != nil && len(fd.Pretty.Blocks) > 0
	}
	if isPretty(a.fileSel) {
		return true
	}
	if doc.Cursor >= 0 && doc.Cursor < len(doc.Rows) {
		if idx := doc.FileOf(doc.Cursor); isPretty(idx) {
			return true
		}
	}
	if first := a.diffList.Position.First; first >= 0 && first < len(doc.Rows) {
		if idx := doc.FileOf(first); isPretty(idx) {
			return true
		}
	}
	if a.sideBySide {
		if first := a.pairList.Position.First; first >= 0 {
			pairs := doc.pairs()
			if first < len(pairs) {
				row := rowOfPair(pairs[first])
				if row >= 0 && row < len(doc.Rows) {
					if idx := doc.FileOf(row); isPretty(idx) {
						return true
					}
				}
			}
		}
	}
	return false
}

// layoutDiff draws the title block describing what is being reviewed, and the
// diff itself below it.
func (a *App) layoutDiff(gtx layout.Context) {
	size := gtx.Constraints.Max
	blockH := 0
	if !a.prettyFocused() {
		blockH = a.layoutTitleBlock(gtx)
	}
	findH := 0
	if a.findField != nil && a.findOpen {
		findH = a.layoutFindBarHeight(gtx)
	}
	a.diffBodyY = a.bodyTop + a.panelHeadH + 1 + blockH + findH

	if size.Y <= blockH+findH {
		return
	}
	if findH > 0 {
		fill(gtx, image.Pt(0, blockH), image.Pt(size.X, findH), a.layoutFindBar)
	}
	fill(gtx, image.Pt(0, blockH+findH), image.Pt(size.X, size.Y-blockH-findH), a.layoutDiffBody)
}

func (a *App) layoutFindBarHeight(gtx layout.Context) int {
	return a.ui.FieldHeight(gtx) + gtx.Dp(4)
}

func (a *App) layoutFindBar(gtx layout.Context) {
	size := gtx.Constraints.Max
	pad := gtx.Dp(reef.PadInline)
	fh := a.ui.FieldHeight(gtx)
	// Field spans most of width, count on right.
	fieldW := max(gtx.Dp(160), size.X-pad*2-gtx.Dp(140))
	fill(gtx, image.Pt(pad, gtx.Dp(2)), image.Pt(fieldW, fh), func(gtx layout.Context) {
		a.findField.Layout(gtx, a.ui.Theme)
	})
	// Match counter. A question says so even while it is empty, because the
	// bar looks the same either way and Enter does something different.
	if q := a.findField.Text(); q != "" || a.askMode {
		cnt := ""
		switch {
		case a.semRunning:
			cnt = "…"
		case a.askMode && len(a.findHits) == 0:
			// The column is sized for a match count, and the controls to the
			// right of it start where it ends.
			cnt = "ASK"
		case len(a.findHits) == 0:
			cnt = "0"
		}
		if cnt == "" {
			if a.findAt >= 0 {
				cnt = fmt.Sprintf("%d/%d", a.findAt+1, len(a.findHits))
			} else {
				cnt = fmt.Sprintf("%d", len(a.findHits))
			}
		}
		fit(gtx, image.Pt(pad+fieldW+gtx.Dp(reef.Sp3), gtx.Dp(2)+(fh-a.ui.TextRow(gtx, reef.SizeUI))/2), image.Pt(80, fh), func(gtx layout.Context) {
			a.ui.Label(gtx, a.ui.P.Muted, cnt)
		})
	}
	rightX := size.X - pad
	// Close.
	rightX -= a.controlRight(gtx, rightX, size.Y, findCloseTag{}, "CLOSE  ESC", a.ui.P.Muted, func() {
		a.closeFind(gtx)
	}) + gtx.Dp(reef.Sp3)
	// Next / Prev when there are hits.
	if len(a.findHits) > 0 {
		rightX -= a.controlRight(gtx, rightX, size.Y, findNextTag{}, "NEXT  ⏎", a.ui.P.Action, func() {
			a.jumpFind(1)
		}) + gtx.Dp(reef.Sp3)
		rightX -= a.controlRight(gtx, rightX, size.Y, findPrevTag{}, "PREV", a.ui.P.Muted, func() {
			a.jumpFind(-1)
		}) + gtx.Dp(reef.Sp3)
	}
	reef.HLine(gtx, size.X, size.Y-1, a.ui.P.RuleFaint)
}

type findCloseTag struct{}
type findNextTag struct{}
type findPrevTag struct{}

// layoutTitleBlock draws the metadata for the change under review, in the
// manner of the title block on a drawing: short capitalised field names with
// their values beside them.
func (a *App) layoutTitleBlock(gtx layout.Context) int {
	ui := a.ui
	width := gtx.Constraints.Max.X
	pad := gtx.Dp(reef.PadInline)
	row := ui.Row(gtx)
	rev := a.rev

	height := pad + row*2 + gtx.Dp(reef.Sp2)

	reef.Fill(gtx, image.Pt(width, height), ui.P.Bg)

	if rev.ChangeID == "" {
		reef.HLine(gtx, width, height-1, ui.P.Rule)
		return height
	}

	y := pad
	x := pad
	limit := max(pad, width-pad)
	x += a.field(gtx, x, y, limit, "CHANGE", rev.ChangeID) + gtx.Dp(reef.Sp5)
	if rev.CommitID != "" && rev.CommitID != rev.ChangeID {
		x += a.field(gtx, x, y, limit, "COMMIT", rev.CommitID) + gtx.Dp(reef.Sp5)
	}
	if rev.Author != "" {
		x += a.field(gtx, x, y, limit, "AUTHOR", rev.Author) + gtx.Dp(reef.Sp5)
	}
	if !rev.Timestamp.IsZero() {
		a.field(gtx, x, y, limit, "DATE", rev.Timestamp.Format("2006-01-02 15:04"))
	}

	y += row
	desc := strings.TrimSpace(a.desc)
	if desc == "" {
		desc = rev.Subject()
	}
	fit(gtx, image.Pt(0, y), image.Pt(width, row), func(gtx layout.Context) {
		a.cellText(gtx, pad, row, width-pad, reef.WeightLabel, ui.P.Fg, firstLine(desc))
	})

	reef.HLine(gtx, width, height-1, ui.P.Rule)
	return height
}

// field draws one "LABEL value" pair on the band that starts at y, in the
// manner of a title block, and returns its width.
func (a *App) field(gtx layout.Context, x, y, limit int, label, value string) int {
	if x >= limit {
		return 0
	}
	row := a.ui.Row(gtx)
	off := op.Offset(image.Pt(0, y)).Push(gtx.Ops)
	defer off.Pop()

	w := a.ui.LabelAt(gtx, a.ui.P.Faint, x, row, limit, label) + gtx.Dp(reef.Sp3)
	w += a.cellText(gtx, x+w, row, limit, font.Normal, a.ui.P.Fg, value)
	return w
}

// layoutDiffBody draws the scrolling diff.
func (a *App) layoutDiffBody(gtx layout.Context) {
	ui := a.ui
	size := gtx.Constraints.Max
	doc := a.diff

	if doc == nil || len(doc.Rows) == 0 {
		if a.busy > 0 {
			a.placeholder(gtx, "LOADING")
		} else {
			a.placeholder(gtx, "NO CHANGES")
		}
		return
	}

	a.horizontalScroll(gtx, size)

	cell := ui.Cell(gtx, reef.SizeCode, false)
	row := ui.CodeRow(gtx)

	defer clip.Rect{Max: size}.Push(gtx.Ops).Pop()

	// Heights land in a scratch map: hit testing during this frame — a drag
	// in progress, say — still needs the last complete frame's geometry, not
	// a half-filled one.
	a.nextRows = make(map[int]int, 64)

	if a.sideBySide {
		pairs := doc.pairs()
		a.pairList.Position.First = doc.pairFor(a.diffList.Position.First)
		a.pairList.Layout(gtx, len(pairs), func(gtx layout.Context, i int) layout.Dimensions {
			h := a.pairHeight(gtx, doc, pairs[i], size.X, row)
			a.nextRows[i] = h
			gtx.Constraints = layout.Exact(image.Pt(size.X, h))
			a.pairRow(gtx, doc, pairs[i], cell, row)
			return layout.Dimensions{Size: image.Pt(size.X, h)}
		})
		if n := a.pairList.Position.First; n >= 0 && n < len(pairs) {
			a.diffList.Position.First = rowOfPair(pairs[n])
		}
		a.placePairs(pairs)
		a.syncFileSel()
		return
	}

	a.diffList.Layout(gtx, len(doc.Rows), func(gtx layout.Context, i int) layout.Dimensions {
		h := a.rowHeight(gtx, doc, i, size.X, row)
		a.nextRows[i] = h
		gtx.Constraints = layout.Exact(image.Pt(size.X, h))
		a.diffRow(gtx, doc, i, cell, row)
		return layout.Dimensions{Size: image.Pt(size.X, h)}
	})
	a.placeRows()

	// Reading down the change moves the manifest with it: the two are one
	// list, seen from two sides.
	a.syncFileSel()
}

// placeRows works out where each row it just drew ended up. The list gives the
// first row and how far it is scrolled; every other row follows from the
// heights, which is exact and needs no second pass over the layout.
func (a *App) placeRows() {
	if tops := a.listTops(&a.diffList); tops != nil {
		a.rowHeights, a.rowTops = a.nextRows, tops
	}
}

// placePairs is placeRows for the two-column view. Both halves of a line sit at
// the same height, so each is recorded at that top and hit testing tells them
// apart by which column the pointer is in.
func (a *App) placePairs(pairs []Pair) {
	tops := a.listTops(&a.pairList)
	if tops == nil {
		return
	}
	rowTops := make(map[int]int, len(tops)*2)
	rowHeights := make(map[int]int, len(tops)*2)
	put := func(row, top, h int) {
		if row >= 0 {
			rowTops[row], rowHeights[row] = top, h
		}
	}
	for i, top := range tops {
		if i < 0 || i >= len(pairs) {
			continue
		}
		p, h := pairs[i], a.nextRows[i]
		if p.Span >= 0 {
			put(p.Span, top, h)
			continue
		}
		put(p.Left, top, h)
		put(p.Right, top, h)
	}
	a.rowTops, a.rowHeights = rowTops, rowHeights
}

// listTops walks the heights of what a list just drew back to where each
// element ended up, or reports nil if the list drew nothing it can start from.
func (a *App) listTops(list *layout.List) map[int]int {
	heights := a.nextRows
	tops := make(map[int]int, len(heights))
	first := list.Position.First
	if _, ok := heights[first]; !ok {
		return nil
	}
	y := a.diffBodyY - list.Position.Offset
	tops[first] = y
	for i := first + 1; ; i++ {
		h, ok := heights[i-1]
		if !ok {
			break
		}
		if _, ok := heights[i]; !ok {
			break
		}
		y += h
		tops[i] = y
	}
	y = tops[first]
	for i := first - 1; i >= 0; i-- {
		h, ok := heights[i]
		if !ok {
			break
		}
		y -= h
		tops[i] = y
	}
	return tops
}

// rowAtY reports which diff row a window y lands in, or -1.
func (a *App) rowAtY(y int) int {
	for row, top := range a.rowTops {
		if y >= top && y < top+a.rowHeights[row] {
			return row
		}
	}
	return -1
}

// horizontalScroll lets a trackpad pan the diff sideways without a scrollbar.
// There is nothing to pan while lines are wrapped.
func (a *App) horizontalScroll(gtx layout.Context, size image.Point) {
	if a.wrapping() {
		return
	}
	stack := clip.Rect{Max: size}.Push(gtx.Ops)
	event.Op(gtx.Ops, a)
	pointer.CursorText.Add(gtx.Ops)
	stack.Pop()
	for {
		ev, ok := gtx.Event(pointer.Filter{
			Target:  a,
			Kinds:   pointer.Scroll,
			ScrollX: pointer.ScrollRange{Min: -200, Max: 200},
		})
		if !ok {
			break
		}
		if pe, ok := ev.(pointer.Event); ok {
			a.diffX = clamp(a.diffX+int(pe.Scroll.X), 0, a.maxScrollX(gtx, size))
		}
	}
}

// maxScrollX is how far right the code can be taken: far enough to bring the
// end of the widest line into view, and no further. Scrolling past the last
// character leaves a blank pane with nothing to say which way is back.
// Wrapped lines all fit, so the range is nothing.
func (a *App) maxScrollX(gtx layout.Context, size image.Point) int {
	if a.wrapping() {
		return 0
	}
	doc := a.diff
	if doc == nil {
		return 0
	}
	cell := a.ui.Cell(gtx, reef.SizeCode, false)
	if cell.X <= 0 {
		return 0
	}
	// The widest gutter any file in the change can take, so the range a line
	// can be scrolled to does not change from file to file.
	visible := (size.X - a.gutterWidth(gtx, doc.Digits, doc.Digits)) / cell.X
	return max(0, doc.MaxCols-visible)
}

// gutterWidth is the space before the code: the action column, the number
// columns, a gap, and the +/- marker.
func (a *App) gutterWidth(gtx layout.Context, oldDig, newDig int) int {
	cell := a.ui.Cell(gtx, reef.SizeCode, false)
	return cell.X*(actionCols+numberCols(oldDig, newDig)+2) + gtx.Dp(reef.PadInline)
}

// numberCols is the width of the number columns and the gap between them, in
// character cells. A file with nothing to print on one side — an added or a
// deleted file — spends no columns on it.
func numberCols(oldDig, newDig int) int {
	n := oldDig + newDig
	if oldDig > 0 && newDig > 0 {
		n++
	}
	return n
}

// wrapping reports whether long diff lines are soft wrapped onto several
// visual lines. It is on by default: noWrap is what is stored, so the zero
// value of an App already wraps.
func (a *App) wrapping() bool { return !a.noWrap }

// toggleWrap turns soft wrapping on and off. Turning it on drops the
// horizontal scroll, which has nothing to say once every line fits.
func (a *App) toggleWrap() {
	a.noWrap = !a.noWrap
	if a.wrapping() {
		a.diffX = 0
	}
	a.diffList.Position.Offset = 0
	a.pairList.Position.Offset = 0
	a.settings.NoWrap = a.noWrap
	a.saveSettings()
	if a.wrapping() {
		a.note("wrap on")
	} else {
		a.note("wrap off")
	}
}

// wrapCols is how many character cells of code fit in a column of the given
// pixel width after its gutter. At least one, so a narrow window wraps every
// character rather than disappearing the line.
func (a *App) wrapCols(gtx layout.Context, width, gutter int) int {
	cell := a.ui.Cell(gtx, reef.SizeCode, false)
	if cell.X <= 0 {
		return 1
	}
	return max(1, (width-gutter)/cell.X)
}

// expandIsSpace reports, for each display cell of text, whether it is a
// breakable space. Tabs advance to the next tab stop and expand into spaces,
// which is what makes them breakable too; every other rune takes one cell.
func expandIsSpace(text string) []bool {
	out := make([]bool, 0, len(text)+8)
	col := 0
	for _, r := range text {
		if r == '\t' {
			for n := tabWidth - col%tabWidth; n > 0; n-- {
				out = append(out, true)
				col++
			}
			continue
		}
		out = append(out, r == ' ')
		col++
	}
	return out
}

// wordWrapBounds breaks text into visual lines of at most cols display cells,
// preserving words: breaks fall after spaces, never inside a word. Each bound
// is a half-open range of display columns, and the bounds cover the whole
// line contiguously, so selection can intersect them the way it does fixed
// strides.
//
// A token longer than the column — leading indent included — cannot be
// preserved and fit. It takes one visual line of its own, shown truncated
// with an ellipsis; truncated reports which lines those are. The bound still
// spans the whole token, so copying keeps what the ellipsis stands in for.
func wordWrapBounds(text string, cols int) (bounds [][2]int, truncated []bool) {
	if cols <= 0 {
		cols = 1
	}
	isSpace := expandIsSpace(text)
	n := len(isSpace)
	if n == 0 {
		return [][2]int{{0, 0}}, []bool{false}
	}
	// Words are maximal non-space runs; a line of only spaces has none and
	// stays one visual line whatever its length — there is nothing to show
	// on a second one.
	type span struct{ s, e int }
	var words []span
	for i := 0; i < n; {
		if isSpace[i] {
			i++
			continue
		}
		j := i + 1
		for j < n && !isSpace[j] {
			j++
		}
		words = append(words, span{i, j})
		i = j
	}
	if len(words) == 0 {
		return [][2]int{{0, n}}, []bool{false}
	}
	// Tokens attach the spaces around a word to it: leading indent to the
	// first word, inter-word runs to the word before them, trailing spaces
	// to the last. That keeps indent with the word it indents instead of
	// stranding it alone on a visual line.
	type token struct{ s, e int }
	tokens := make([]token, 0, len(words))
	for wi, w := range words {
		s := w.s
		if wi == 0 {
			s = 0
		}
		e := w.e
		for e < n && isSpace[e] {
			e++
		}
		tokens = append(tokens, token{s, e})
	}
	emit := func(s, e int, trunc bool) {
		bounds = append(bounds, [2]int{s, e})
		truncated = append(truncated, trunc)
	}
	curStart, curLen := tokens[0].s, 0
	flush := func() {
		if curLen > 0 {
			emit(curStart, curStart+curLen, false)
			curLen = 0
		}
	}
	for _, t := range tokens {
		l := t.e - t.s
		if l > cols {
			flush()
			emit(t.s, t.e, true)
			curStart = t.e
			continue
		}
		if curLen == 0 {
			curStart, curLen = t.s, l
			continue
		}
		if curLen+l <= cols {
			curLen += l
			continue
		}
		flush()
		curStart, curLen = t.s, l
	}
	flush()
	if len(bounds) == 0 {
		return [][2]int{{0, n}}, []bool{false}
	}
	return bounds, truncated
}

// wrappedLineCount is how many visual lines text takes at cols cells per
// line, words preserved. An empty line still takes one.
func wrappedLineCount(text string, cols int) int {
	bounds, _ := wordWrapBounds(text, cols)
	return max(1, len(bounds))
}

func (a *App) rowHeight(gtx layout.Context, doc *DiffDoc, i int, width, row int) int {
	r := doc.Row(i)
	switch r.Kind {
	case rowComment:
		return a.commentHeight(gtx, r.Comment.Body, width, row)
	case rowDraft:
		return a.draftHeight(gtx, width, row)
	case rowFile:
		return gtx.Dp(reef.GapRow) + gtx.Dp(reef.Sp4)
	case rowGap:
		return gtx.Dp(reef.GapRow)
	case rowPretty:
		return a.prettyHeight(gtx, doc, i, width, row)
	case rowLine:
		if a.wrapping() {
			oldDig, newDig := doc.digitsAt(i)
			gut := a.gutterWidth(gtx, oldDig, newDig)
			cols := a.wrapCols(gtx, width, gut)
			return wrappedLineCount(r.Line.Text, cols) * row
		}
		return row
	default:
		return row
	}
}

// diffRow draws one row of the unified view.
func (a *App) diffRow(gtx layout.Context, doc *DiffDoc, i int, cell image.Point, row int) {
	ui := a.ui
	size := gtx.Constraints.Max
	r := doc.Row(i)
	cursor := i == doc.Cursor && a.focus == PaneDiff
	oldDig, newDig := doc.digitsAt(i)
	gut := a.gutterWidth(gtx, oldDig, newDig)

	switch r.Kind {
	case rowFile:
		a.fileHeadRow(gtx, doc, i, cursor)
		return
	case rowGap:
		a.gapRow(gtx, doc, i, row, cursor)
		return
	case rowNote:
		a.noteRow(gtx, doc, i, row, cursor)
		return
	case rowHunk:
		// Hide hunk headers in pretty mode: the overlay replaces them.
		if fd := doc.FileAt(i); fd != nil && fd.IsDoc && fd.PrettyOn && fd.Pretty != nil {
			return
		}
		// A hunk header is a section rule: groups in this system are separated
		// by a line, never by a gap.
		reef.Fill(gtx, size, ui.P.HunkBg)
		a.codeText(gtx, gtx.Dp(reef.PadInline), row, size.X, font.Normal, ui.P.HunkFg, r.Text)
		reef.HLine(gtx, size.X, 0, ui.P.Rule)
		if cursor {
			reef.Edge(gtx, size.Y, ui.P.Focus)
		}
		return
	case rowPretty:
		a.drawPretty(gtx, doc, i, cell, row)
		return
	case rowComment:
		a.commentRow(gtx, r.Comment, cursor)
		return
	case rowDraft:
		a.draftRow(gtx)
		return
	}

	// Tint, a two pixel gutter and the sign column: three carriers for one
	// fact, because a diff must never depend on colour alone.
	l := r.Line
	bg, hot, marker, markerColor := ui.P.Bg, ui.P.Bg, " ", ui.P.Faint
	gutter := reef.ColorNRGBA{}
	switch l.Kind {
	case diffparse.Added:
		bg, hot, marker, markerColor = ui.P.AddBg, ui.P.AddBgHot, "+", ui.P.AddFg
		gutter = ui.P.AddGutter
	case diffparse.Removed:
		bg, hot, marker, markerColor = ui.P.DelBg, ui.P.DelBgHot, "−", ui.P.DelFg
		gutter = ui.P.DelGutter
	}
	reef.Fill(gtx, size, bg)
	if gutter.A != 0 {
		reef.Edge(gtx, size.Y, gutter)
	}

	// The gutter starts a note on the line; the code area just moves the
	// cursor. They do not overlap, so a click means exactly one thing.
	gutRect := image.Rect(0, 0, gut, size.Y)
	a.hoverable(gtx, gutRect, diffTag{row: i, gutter: true}, i, func() {
		doc.Cursor = i
		a.focus = PaneDiff
		a.after(a.startComment)
	})
	// The code column belongs to the selection, which moves the cursor on the
	// press that starts it. There is no second area over it: two overlapping
	// areas would mean only the upper one ever heard the press.
	if size.X > gut {
		a.selectArea(gtx, image.Rect(gut, 0, size.X, size.Y), i, gut, cell)
	}

	// The cursor line is banded top and bottom rather than marked with a
	// sliver, so it can be found without hunting for it. The bands are the
	// focus colour; the leading edge stays with the diff, whose gutter it is,
	// and only stands in for the cursor on a context line that has none.
	if cursor {
		reef.HLine(gtx, size.X, 0, ui.P.Focus)
		reef.HLine(gtx, size.X, size.Y-1, ui.P.Focus)
		if gutter.A == 0 {
			reef.Edge(gtx, size.Y, ui.P.Focus)
		}
	}

	// Line numbers, dimmed, in two fixed columns after the action strip.
	x := gtx.Dp(reef.Sp3) + actionCols*cell.X
	nums := numberCols(oldDig, newDig)
	if oldDig > 0 {
		a.codeTextRight(gtx, x+oldDig*cell.X, row, ui.P.Faint, num(l.OldNum))
	}
	if newDig > 0 {
		a.codeTextRight(gtx, x+nums*cell.X, row, ui.P.Faint, num(l.NewNum))
	}
	a.codeText(gtx, x+(nums+1)*cell.X, row, size.X, reef.WeightLabel, markerColor, marker)

	// The affordance: a + under the pointer, or on the line the cursor is on,
	// so the gesture is discoverable both with a mouse and without one.
	if (a.hoverRow == i || cursor) && l.OldNum+l.NewNum > 0 {
		a.codeText(gtx, gtx.Dp(reef.Sp3), row, size.X, reef.WeightLabel, ui.P.Action, "+")
	}

	// A hairline separates the numbers from the code, the way a ruled margin
	// would on paper. Where a note covers this line it takes the note's own
	// colour, so a note about a run of lines says which lines.
	margin := ui.P.RuleFaint
	if r.Noted {
		margin = ui.P.Action
	}
	reef.VLine(gtx, gut-gtx.Dp(reef.Sp3), size.Y, margin)

	// The code itself, clipped to the area right of the gutter. Unwrapped it
	// is shifted by the horizontal scroll; wrapped every line fits, so there
	// is nothing to scroll to.
	if size.X <= gut {
		return
	}
	if a.wrapping() {
		cols := a.wrapCols(gtx, size.X, gut)
		area := clip.Rect(image.Rect(gut, 0, size.X, size.Y)).Push(gtx.Ops)
		fill(gtx, image.Pt(gut, 0), image.Pt(size.X-gut, size.Y), func(gtx layout.Context) {
			a.drawCodeWrapped(gtx, r, cell, row, i, hot, cols)
		})
		area.Pop()

		if l.NoNewline {
			lines := wrappedLineCount(r.Line.Text, cols)
			off := op.Offset(image.Pt(0, (lines-1)*row)).Push(gtx.Ops)
			a.codeTextRight(gtx, size.X-gtx.Dp(reef.Sp3), row, ui.P.Faint, "no newline")
			off.Pop()
		}
		return
	}
	area := clip.Rect(image.Rect(gut, 0, size.X, size.Y)).Push(gtx.Ops)
	fill(gtx, image.Pt(gut-a.diffX*cell.X, 0), image.Pt(size.X-gut+a.diffX*cell.X, size.Y), func(gtx layout.Context) {
		a.drawCode(gtx, r, cell, row, i, hot)
	})
	area.Pop()

	if l.NoNewline {
		a.codeTextRight(gtx, size.X-gtx.Dp(reef.Sp3), row, ui.P.Faint, "no newline")
	}
}

// fileHeadRow draws the heading that opens one file's part of the sheet: the
// same facts the manifest row carries, printed where the diff itself starts,
// so scrolling through the change never loses track of which file is on
// screen.
func (a *App) fileHeadRow(gtx layout.Context, doc *DiffDoc, i int, cursor bool) {
	ui := a.ui
	size := gtx.Constraints.Max
	fd := doc.FileAt(i)
	if fd == nil {
		return
	}
	idx := doc.FileOf(i)
	if idx < 0 || idx >= len(a.files) {
		return
	}
	// The manifest row itself, not a copy taken when the change was opened:
	// the read/unread box on a heading is the same box as the one in the
	// manifest, and marking a file read from either has to move both.
	f := a.files[idx]
	cell := ui.Cell(gtx, reef.SizeUI, false)

	reef.Fill(gtx, size, ui.P.BgSunken)
	reef.FillRect(gtx, image.Rect(0, 0, size.X, gtx.Dp(reef.BorderThick)), ui.P.Rule)
	reef.HLine(gtx, size.X, size.Y-1, ui.P.Rule)
	if cursor {
		reef.Edge(gtx, size.Y, ui.P.Focus)
	}
	a.clickArea(gtx, image.Rect(0, 0, size.X, size.Y), fileHeadTag{idx}, func() {
		a.focus = PaneDiff
		doc.Cursor = i
		a.fileSel = idx
		a.scrollList(&a.fileList, idx)
	})

	x := gtx.Dp(reef.PadInline)
	a.cellText(gtx, x, size.Y, size.X, reef.WeightLabel, a.statusColor(f.Status), f.Status.String())
	x += cell.X + gtx.Dp(reef.Sp3)

	boxW := cell.X * 3
	a.viewedBox(gtx, image.Rect(x, 0, x+boxW, size.Y), idx, f, true)
	x += boxW + gtx.Dp(reef.Sp3)

	rightX := size.X - gtx.Dp(reef.PadInline)
	// Pretty toggle for doc files, beside WHOLE FILE.
	if fd.IsDoc {
		label, col := "RAW", ui.P.Muted
		if fd.PrettyOn {
			label, col = "PRETTY", ui.P.Action
		}
		// When pretty doc not yet rendered, show muted even when on.
		if fd.PrettyOn && fd.Pretty == nil {
			col = ui.P.Muted
		}
		rightX -= a.controlRight(gtx, rightX, size.Y, prettyTag{idx}, label+"  P", col, func() {
			doc.Cursor = i
			a.after(func() { a.togglePretty(idx) })
		}) + gtx.Dp(reef.Sp3)
	}
	// The way to see the rest of the file sits on the file, not in a menu.
	if fd.Collapsed() {
		rightX -= a.controlRight(gtx, rightX, size.Y, wholeTag{idx}, "WHOLE FILE  ⇧E", ui.P.Muted, func() {
			doc.Cursor = i
			a.after(func() { a.expandFile(idx) })
		}) + gtx.Dp(reef.Sp3)
	}
	if f.Added > 0 || f.Removed > 0 {
		counts := fmt.Sprintf("+%d −%d", f.Added, f.Removed)
		rightX -= a.cellTextRight(gtx, rightX, size.Y, font.Normal, ui.P.Muted, counts) + gtx.Dp(reef.Sp4)
	}
	a.cellText(gtx, x, size.Y, rightX, reef.WeightLabel, ui.P.Strong, f.Display())
}

// gapRow draws a run of unchanged lines the diff left out. It says how many
// there are and how to see them: nothing in this interface is hidden without
// saying so, and a diff's three lines of context is the one place where
// something always is.
func (a *App) gapRow(gtx layout.Context, doc *DiffDoc, i, row int, cursor bool) {
	ui := a.ui
	size := gtx.Constraints.Max
	g := doc.Row(i).Gap
	fd := doc.FileAt(i)

	reef.Fill(gtx, size, ui.P.BgAlt)
	reef.HLine(gtx, size.X, 0, ui.P.RuleFaint)
	reef.HLine(gtx, size.X, size.Y-1, ui.P.RuleFaint)
	if cursor {
		reef.Edge(gtx, size.Y, ui.P.Focus)
	}
	// The whole strip opens the run; the controls on it are the same gesture
	// spelled out, and are registered after so they take their own presses.
	a.clickArea(gtx, image.Rect(0, 0, size.X, size.Y), gapTag{i, false}, func() {
		doc.Cursor = i
		a.after(func() { a.expandGap(i, expandStep) })
	})

	label := "… " + strconv.Itoa(g.Count) + " LINES NOT SHOWN"
	switch {
	case g.Count < 0:
		label = "… TO THE END OF THE FILE"
	case g.Count == 1:
		label = "… 1 LINE NOT SHOWN"
	}
	if fd != nil && fd.loading {
		label += " · READING"
	}

	rightX := size.X - gtx.Dp(reef.PadInline)
	rightX -= a.controlRight(gtx, rightX, size.Y, gapTag{i, true}, "ALL", ui.P.Muted, func() {
		doc.Cursor = i
		a.after(func() { a.expandGap(i, -1) })
	}) + gtx.Dp(reef.Sp3)
	rightX -= a.controlRight(gtx, rightX, size.Y, gapExpandTag{i}, "EXPAND  E", ui.P.Action, func() {
		doc.Cursor = i
		a.after(func() { a.expandGap(i, expandStep) })
	}) + gtx.Dp(reef.Sp3)

	a.cellText(gtx, gtx.Dp(reef.PadInline), size.Y, rightX, reef.WeightLabel, ui.P.Faint, label)
}

// noteRow draws what stands in for a file's hunks: binary, renamed without
// changes, or a diff large enough that it was left out of the change. The last
// of those is the only one that can be asked for, and it says so.
func (a *App) noteRow(gtx layout.Context, doc *DiffDoc, i, row int, cursor bool) {
	ui := a.ui
	size := gtx.Constraints.Max
	fd := doc.FileAt(i)

	reef.Fill(gtx, size, ui.P.BgAlt)
	if cursor {
		reef.Edge(gtx, size.Y, ui.P.Focus)
	}

	rightX := size.X - gtx.Dp(reef.PadInline)
	if fd != nil && fd.File != nil && fd.File.Truncated {
		idx := doc.FileOf(i)
		label := "SHOW ANYWAY"
		if fd.loading {
			label = "READING…"
		}
		rightX -= a.controlRight(gtx, rightX, size.Y, showWholeTag{idx}, label, ui.P.Action, func() {
			doc.Cursor = i
			a.after(func() { a.showWhole(idx) })
		}) + gtx.Dp(reef.Sp3)
	}

	a.codeText(gtx, gtx.Dp(reef.PadInline), row, rightX, reef.WeightLabel, ui.P.Faint, doc.Row(i).Text)
}

// The controls carried by the sheet's own headings and gaps, each named so its
// hover state survives between frames.
type showWholeTag struct{ file int }
type fileHeadTag struct{ file int }
type wholeTag struct{ file int }
type gapTag struct {
	row int
	all bool
}
type gapExpandTag struct{ row int }
type prettyTag struct{ file int }

// drawCode paints one line of source: the changed runs behind it, then the
// text in runs of a single syntax colour.
func (a *App) drawCode(gtx layout.Context, r Row, cell image.Point, row, index int, hotColor reef.ColorNRGBA) {
	ui := a.ui
	cells := buildCells(r.Line.Text, r.Spans, r.Line.Segments)

	// Background for the parts of the line that actually differ.
	for i := 0; i < len(cells.hot); {
		if !cells.hot[i] {
			i++
			continue
		}
		j := i
		for j < len(cells.hot) && cells.hot[j] {
			j++
		}
		reef.FillRect(gtx, image.Rect(i*cell.X, 0, j*cell.X, row), hotColor)
		i = j
	}

	// Find matches.
	if ranges := a.findRanges(index); len(ranges) > 0 {
		isCur := a.isFindCurrent(index)
		for _, rng := range ranges {
			c0, c1 := rng[0], rng[1]
			c0 = clamp(c0, 0, len(cells.runes))
			c1 = clamp(c1, 0, len(cells.runes))
			if c1 > c0 {
				col := ui.P.WarnBg
				if isCur {
					col = ui.P.Warn
				}
				reef.FillRect(gtx, image.Rect(c0*cell.X, 0, c1*cell.X, row), col)
			}
		}
		// Current match gets an edge for distinction.
		if isCur {
			for _, rng := range ranges {
				c0, c1 := rng[0], rng[1]
				c0 = clamp(c0, 0, len(cells.runes))
				c1 = clamp(c1, 0, len(cells.runes))
				if c1 > c0 {
					reef.Stroke(gtx, image.Rect(c0*cell.X, 0, c1*cell.X, row), ui.P.Warn)
				}
			}
		}
	}

	// What is selected is tinted before the text goes down, so the code stays
	// on top of it rather than being reversed out. Drawn after find so selection remains visible.
	if c0, c1, ok := a.sel.Cols(index); ok {
		c1 = min(c1, len(cells.runes))
		if c1 > c0 {
			reef.FillRect(gtx, image.Rect(c0*cell.X, 0, c1*cell.X, row), ui.P.TextSel)
		}
	}

	for i := 0; i < len(cells.runes); {
		class := cells.class[i]
		j := i
		for j < len(cells.runes) && cells.class[j] == class {
			j++
		}
		text := strings.TrimRight(string(cells.runes[i:j]), " ")
		if text != "" {
			style := font.Regular
			if highlight.Class(class) == highlight.Comment {
				style = font.Italic
			}
			a.codeStyled(gtx, i*cell.X, row, gtx.Constraints.Max.X, style, a.syntax(class), text)
		}
		i = j
	}
}

// drawCodeWrapped paints one line of source wrapped onto several visual
// lines, words preserved: breaks fall after spaces, never inside a word. It
// carries the same selection tint, changed background and syntax colours as
// drawCode down each visual line the code takes.
//
// A token longer than the column takes one visual line of its own, shown
// truncated with an ellipsis; the bound still spans the whole token, so
// copying keeps what the ellipsis stands in for.
func (a *App) drawCodeWrapped(gtx layout.Context, r Row, cell image.Point, rowH, index int, hotColor reef.ColorNRGBA, cols int) {
	ui := a.ui
	cells := buildCells(r.Line.Text, r.Spans, r.Line.Segments)
	n := len(cells.runes)
	bounds, truncated := wordWrapBounds(r.Line.Text, cols)
	if len(bounds) == 0 {
		return
	}

	var sc0, sc1 int
	selOk := false
	if c0, c1, ok := a.sel.Cols(index); ok {
		sc0, sc1, selOk = c0, min(c1, n), true
	}
	// anyHot reports whether any cell in [s,e) carries the changed
	// background, for painting it behind the ellipsis that stands in for
	// them.
	anyHot := func(s, e int) bool {
		s = clamp(s, 0, n)
		e = clamp(e, 0, n)
		for i := s; i < e; i++ {
			if cells.hot[i] {
				return true
			}
		}
		return false
	}
	// drawRuns paints cells [s,e) in runs of one syntax colour at x offset
	// ox, in the current visual line already offset to its y.
	drawRuns := func(s, e, ox int) {
		for i := s; i < e; {
			class := cells.class[i]
			j := i
			for j < e && cells.class[j] == class {
				j++
			}
			text := strings.TrimRight(string(cells.runes[i:j]), " ")
			if text != "" {
				style := font.Regular
				if highlight.Class(class) == highlight.Comment {
					style = font.Italic
				}
				a.codeStyled(gtx, ox+(i-s)*cell.X, rowH, gtx.Constraints.Max.X, style, a.syntax(class), text)
			}
			i = j
		}
	}
	// ellipsisClass is the syntax colour the ellipsis takes: the colour of
	// the last cell it stands beside, so it reads as part of the token.
	ellipsisClass := func(s, shown int) uint8 {
		if shown > 0 && s+shown-1 < n {
			return cells.class[s+shown-1]
		}
		if s < n {
			return cells.class[s]
		}
		return uint8(highlight.Plain)
	}

	findRgs := a.findRanges(index)
	isCurRow := a.isFindCurrent(index)
	// helper to draw find highlights intersecting [ls, le) at y offset already pushed
	drawFind := func(ls, le, offX int) {
		if len(findRgs) == 0 {
			return
		}
		for _, rng := range findRgs {
			fs, fe := rng[0], rng[1]
			// intersect with this visual line's displayed range
			a0 := max(fs, ls)
			b0 := min(fe, le)
			if b0 <= a0 {
				continue
			}
			col := ui.P.WarnBg
			if isCurRow {
				col = ui.P.Warn
			}
			reef.FillRect(gtx, image.Rect((a0-offX)*cell.X, 0, (b0-offX)*cell.X, rowH), col)
		}
		if isCurRow {
			for _, rng := range findRgs {
				fs, fe := rng[0], rng[1]
				a0 := max(fs, ls)
				b0 := min(fe, le)
				if b0 <= a0 {
					continue
				}
				reef.Stroke(gtx, image.Rect((a0-offX)*cell.X, 0, (b0-offX)*cell.X, rowH), ui.P.Warn)
			}
		}
	}
	for k, be := range bounds {
		s, e := be[0], be[1]
		s = clamp(s, 0, n)
		e = clamp(e, 0, n)
		if s > e {
			s = e
		}
		off := op.Offset(image.Pt(0, k*rowH)).Push(gtx.Ops)

		if !truncated[k] {
			for i := s; i < e; {
				if !cells.hot[i] {
					i++
					continue
				}
				j := i
				for j < e && cells.hot[j] {
					j++
				}
				reef.FillRect(gtx, image.Rect((i-s)*cell.X, 0, (j-s)*cell.X, rowH), hotColor)
				i = j
			}
			drawFind(s, e, s)
			if selOk && sc1 > sc0 {
				if a0, b0 := max(sc0, s), min(sc1, e); b0 > a0 {
					reef.FillRect(gtx, image.Rect((a0-s)*cell.X, 0, (b0-s)*cell.X, rowH), ui.P.TextSel)
				}
			}
			drawRuns(s, e, 0)
			off.Pop()
			continue
		}
		// An overlong token: the first cols-1 cells, then an ellipsis in
		// the last cell. The bound spans the whole token; only the head is
		// shown.
		shown := max(0, min(cols-1, e-s))
		dispEnd := s + shown
		for i := s; i < dispEnd; {
			if !cells.hot[i] {
				i++
				continue
			}
			j := i
			for j < dispEnd && cells.hot[j] {
				j++
			}
			reef.FillRect(gtx, image.Rect((i-s)*cell.X, 0, (j-s)*cell.X, rowH), hotColor)
			i = j
		}
		if anyHot(s+shown, e) {
			reef.FillRect(gtx, image.Rect(shown*cell.X, 0, cols*cell.X, rowH), hotColor)
		}
		drawFind(s, dispEnd, s)
		// If find intersects hidden tail, highlight ellipsis.
		if len(findRgs) > 0 {
			for _, rng := range findRgs {
				if rng[0] < e && rng[1] > dispEnd {
					col := ui.P.WarnBg
					if isCurRow {
						col = ui.P.Warn
					}
					reef.FillRect(gtx, image.Rect(shown*cell.X, 0, cols*cell.X, rowH), col)
					break
				}
			}
		}
		if selOk && sc1 > sc0 {
			if a0, b0 := max(sc0, s), min(sc1, dispEnd); b0 > a0 {
				reef.FillRect(gtx, image.Rect((a0-s)*cell.X, 0, (b0-s)*cell.X, rowH), ui.P.TextSel)
			}
			// The ellipsis stands in for the hidden tail: it takes the
			// selection tint when the selection reaches past what is shown.
			if sc1 > s+shown && sc0 < e {
				reef.FillRect(gtx, image.Rect(shown*cell.X, 0, cols*cell.X, rowH), ui.P.TextSel)
			}
		}
		drawRuns(s, dispEnd, 0)
		if cols >= 1 {
			class := ellipsisClass(s, shown)
			style := font.Regular
			if highlight.Class(class) == highlight.Comment {
				style = font.Italic
			}
			a.codeStyled(gtx, shown*cell.X, rowH, gtx.Constraints.Max.X, style, a.syntax(class), "…")
		}
		off.Pop()
	}
}

// syntaxRole maps the highlighter's token classes onto the design system's
// syntax roles. The two lists are in the same order, but writing the mapping
// out is what stops one of them being extended without the other.
var syntaxRole = [...]reef.Syntax{
	highlight.Plain:       reef.SyntaxPlain,
	highlight.Keyword:     reef.SyntaxKeyword,
	highlight.Name:        reef.SyntaxName,
	highlight.Function:    reef.SyntaxFunction,
	highlight.Type:        reef.SyntaxType,
	highlight.String:      reef.SyntaxString,
	highlight.Number:      reef.SyntaxNumber,
	highlight.Comment:     reef.SyntaxComment,
	highlight.Operator:    reef.SyntaxOperator,
	highlight.Punctuation: reef.SyntaxPunctuation,
	highlight.Preproc:     reef.SyntaxPreproc,
	highlight.Error:       reef.SyntaxError,
}

// syntax returns the colour a token class is set in.
func (a *App) syntax(class uint8) reef.ColorNRGBA {
	if int(class) >= len(syntaxRole) {
		return a.ui.P.Syntax[reef.SyntaxPlain]
	}
	return a.ui.P.Syntax[syntaxRole[class]]
}

// lineCells is a line expanded onto the character grid, with a syntax class
// and a changed flag for every cell. Working per cell rather than per byte
// keeps tabs, multi-byte runes and overlapping spans from having to agree.
type lineCells struct {
	runes []rune
	class []uint8
	hot   []bool
}

func buildCells(text string, spans []highlight.Span, segs []diffparse.Segment) lineCells {
	out := lineCells{
		runes: make([]rune, 0, len(text)+8),
		class: make([]uint8, 0, len(text)+8),
		hot:   make([]bool, 0, len(text)+8),
	}
	si, gi := 0, 0
	for b := 0; b < len(text); {
		r, size := utf8.DecodeRuneInString(text[b:])

		for si < len(spans) && spans[si].End <= b {
			si++
		}
		class := uint8(highlight.Plain)
		if si < len(spans) && spans[si].Start <= b {
			class = uint8(spans[si].Class)
		}

		for gi < len(segs) && segs[gi].End <= b {
			gi++
		}
		hot := gi < len(segs) && segs[gi].Start <= b && segs[gi].Changed

		if r == '\t' {
			for n := tabWidth - len(out.runes)%tabWidth; n > 0; n-- {
				out.runes = append(out.runes, ' ')
				out.class = append(out.class, class)
				out.hot = append(out.hot, hot)
			}
		} else {
			out.runes = append(out.runes, r)
			out.class = append(out.class, class)
			out.hot = append(out.hot, hot)
		}
		b += size
	}
	return out
}

func displayWidth(text string) int {
	n := 0
	for _, r := range text {
		if r == '\t' {
			n += tabWidth - n%tabWidth
		} else {
			n++
		}
	}
	return n
}

func num(n int) string {
	if n == 0 {
		return ""
	}
	return strconv.Itoa(n)
}

// pairHeight is how tall a two-column line is. One that runs across both
// columns takes whatever that row takes; a pair of code lines takes the
// taller of the two sides once wrapping is on, so the columns stay aligned.
func (a *App) pairHeight(gtx layout.Context, doc *DiffDoc, p Pair, width, row int) int {
	if p.Span >= 0 {
		return a.rowHeight(gtx, doc, p.Span, width, row)
	}
	if !a.wrapping() {
		return row
	}
	cell := a.ui.Cell(gtx, reef.SizeCode, false)
	if cell.X <= 0 {
		return row
	}
	halfW := width / 2
	rightW := width - halfW - 1
	height := func(idx int, colW int, left bool) int {
		if idx < 0 {
			return row
		}
		r := doc.Row(idx)
		if r.Kind != rowLine {
			return row
		}
		oldDig, newDig := doc.digitsAt(idx)
		digits := newDig
		if left {
			digits = oldDig
		}
		gutter := gtx.Dp(reef.Sp3) + (digits+1)*cell.X
		cols := max(1, (colW-gutter)/cell.X)
		return wrappedLineCount(r.Line.Text, cols) * row
	}
	return max(height(p.Left, halfW, true), height(p.Right, rightW, false))
}

// pairRow draws one line of the side by side view.
func (a *App) pairRow(gtx layout.Context, doc *DiffDoc, p Pair, cell image.Point, row int) {
	ui := a.ui
	size := gtx.Constraints.Max

	if p.Span >= 0 {
		r := doc.Row(p.Span)
		cursor := p.Span == doc.Cursor && a.focus == PaneDiff
		switch r.Kind {
		case rowHunk:
			if fd := doc.FileAt(p.Span); fd != nil && fd.IsDoc && fd.PrettyOn && fd.Pretty != nil {
				return
			}
			reef.Fill(gtx, size, ui.P.HunkBg)
			a.codeText(gtx, gtx.Dp(reef.PadInline), row, size.X, font.Normal, ui.P.HunkFg, r.Text)
			reef.HLine(gtx, size.X, 0, ui.P.Rule)
		case rowFile:
			a.fileHeadRow(gtx, doc, p.Span, cursor)
		case rowGap:
			a.gapRow(gtx, doc, p.Span, row, cursor)
		case rowNote:
			a.noteRow(gtx, doc, p.Span, row, cursor)
		case rowPretty:
			a.drawPretty(gtx, doc, p.Span, cell, row)
		case rowComment:
			a.commentRow(gtx, r.Comment, cursor)
		case rowDraft:
			a.draftRow(gtx)
		}
		return
	}

	half := size.X / 2
	draw := func(x, w, idx int, left bool) {
		if w <= 0 {
			return
		}
		fill(gtx, image.Pt(x, 0), image.Pt(w, size.Y), func(gtx layout.Context) {
			if idx < 0 {
				reef.Fill(gtx, gtx.Constraints.Max, ui.P.BgAlt)
			} else {
				a.halfRow(gtx, doc, idx, cell, row, left)
			}
		})
	}
	draw(0, half, p.Left, true)
	draw(half+1, size.X-half-1, p.Right, false)
	reef.VLine(gtx, half, size.Y, ui.P.Rule)
}

// halfRow draws one side of the side by side view: the same content as a
// unified row, with a single line number column. Which column it is decides
// whether context lines are numbered from the old file or the new one.
func (a *App) halfRow(gtx layout.Context, doc *DiffDoc, i int, cell image.Point, row int, left bool) {
	ui := a.ui
	size := gtx.Constraints.Max
	r := doc.Row(i)
	l := r.Line

	bg, hot := ui.P.Bg, ui.P.Bg
	gutter := reef.ColorNRGBA{}
	switch l.Kind {
	case diffparse.Added:
		bg, hot = ui.P.AddBg, ui.P.AddBgHot
		gutter = ui.P.AddGutter
	case diffparse.Removed:
		bg, hot = ui.P.DelBg, ui.P.DelBgHot
		gutter = ui.P.DelGutter
	}
	reef.Fill(gtx, size, bg)
	if gutter.A != 0 {
		reef.Edge(gtx, size.Y, gutter)
	}
	if i == doc.Cursor && a.focus == PaneDiff {
		reef.HLine(gtx, size.X, 0, ui.P.Focus)
		reef.HLine(gtx, size.X, size.Y-1, ui.P.Focus)
		if gutter.A == 0 {
			reef.Edge(gtx, size.Y, ui.P.Focus)
		}
	}

	n := l.NewNum
	if left {
		n = l.OldNum
	}
	oldDig, newDig := doc.digitsAt(i)
	digits := newDig
	if left {
		digits = oldDig
	}
	half := gtx.Dp(reef.Sp3) + (digits+1)*cell.X
	a.codeTextRight(gtx, gtx.Dp(reef.Sp3)+digits*cell.X, row, ui.P.Faint, num(n))

	if size.X <= half {
		return
	}
	if a.wrapping() {
		cols := max(1, (size.X-half)/cell.X)
		area := clip.Rect(image.Rect(half, 0, size.X, size.Y)).Push(gtx.Ops)
		fill(gtx, image.Pt(half, 0), image.Pt(size.X-half, size.Y), func(gtx layout.Context) {
			a.drawCodeWrapped(gtx, r, cell, row, i, hot, cols)
		})
		area.Pop()

		// The same two areas a unified row carries, over this column alone: the
		// number gutter starts a note, the code takes a selection.
		a.hoverable(gtx, image.Rect(0, 0, half, size.Y), diffTag{row: i, gutter: true}, i, func() {
			doc.Cursor = i
			a.focus = PaneDiff
			a.after(a.startComment)
		})
		a.selectArea(gtx, image.Rect(half, 0, size.X, size.Y), i, half, cell)
		return
	}
	area := clip.Rect(image.Rect(half, 0, size.X, size.Y)).Push(gtx.Ops)
	fill(gtx, image.Pt(half-a.diffX*cell.X, 0), image.Pt(size.X-half+a.diffX*cell.X, size.Y), func(gtx layout.Context) {
		a.drawCode(gtx, r, cell, row, i, hot)
	})
	area.Pop()

	// The same two areas a unified row carries, over this column alone: the
	// number gutter starts a note, the code takes a selection.
	a.hoverable(gtx, image.Rect(0, 0, half, size.Y), diffTag{row: i, gutter: true}, i, func() {
		doc.Cursor = i
		a.focus = PaneDiff
		a.after(a.startComment)
	})
	a.selectArea(gtx, image.Rect(half, 0, size.X, size.Y), i, half, cell)
}

// diffTag identifies one clickable region of a diff row. It is a value rather
// than a pointer so it stays the same across frames as rows are rebuilt.
type diffTag struct {
	row    int
	gutter bool
}

// hoverable is clickArea that also tracks which row the pointer is over, so
// the comment affordance can appear under it.
func (a *App) hoverable(gtx layout.Context, r image.Rectangle, tag event.Tag, row int, onClick func()) {
	stack := clip.Rect(r).Push(gtx.Ops)
	event.Op(gtx.Ops, tag)
	pointer.CursorPointer.Add(gtx.Ops)
	stack.Pop()

	for {
		ev, ok := gtx.Event(pointer.Filter{
			Target: tag,
			Kinds:  pointer.Press | pointer.Enter | pointer.Leave,
		})
		if !ok {
			break
		}
		pe, ok := ev.(pointer.Event)
		if !ok {
			continue
		}
		switch pe.Kind {
		case pointer.Enter:
			a.hoverRow = row
		case pointer.Leave:
			if a.hoverRow == row {
				a.hoverRow = -1
			}
		case pointer.Press:
			onClick()
			reef.Redraw(gtx)
		}
	}
}

// diffControls draws the controls in the diff pane's header: how the diff is
// laid out, and the ways of reading less of it.
func (a *App) diffControls(gtx layout.Context) {
	ui := a.ui
	size := gtx.Constraints.Max
	rightX := size.X - gtx.Dp(reef.PadInline)

	label, c := "UNIFIED", ui.P.Muted
	if a.sideBySide {
		label, c = "SPLIT", ui.P.Action
	}
	rightX -= a.controlRight(gtx, rightX, size.Y, tagSplit, label+"  \\", c, a.toggleSplit) + gtx.Dp(reef.Sp3)

	wrapLabel, wc := "WRAP  W", ui.P.Action
	if a.noWrap {
		wrapLabel, wc = "WRAP  W", ui.P.Muted
	}
	rightX -= a.controlRight(gtx, rightX, size.Y, tagWrap, wrapLabel, wc, a.toggleWrap) + gtx.Dp(reef.Sp3)

}

// toggleSplit swaps the unified and two-column views. The row position carries
// across, since both views are laid out from it; how far into that row the last
// view happened to be scrolled does not.
func (a *App) toggleSplit() {
	a.sideBySide = !a.sideBySide
	a.diffList.Position.Offset = 0
	a.pairList.Position.Offset = 0
}
