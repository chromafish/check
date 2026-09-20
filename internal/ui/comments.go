package ui

import (
	"image"
	"strconv"
	"strings"

	"gioui.org/font"
	"gioui.org/io/key"
	"gioui.org/layout"

	"github.com/chromafish/check/internal/diffparse"
	"github.com/chromafish/check/internal/state"

	"github.com/chromafish/check/reef"
)

// draft is a comment being written. Only one exists at a time.
type draft struct {
	path    string
	side    state.Side
	line    int
	endLine int // the last line of a note about a run, or 0
	field   *reef.Field
	edit    string // the comment being revised, empty when writing a new one

	// wantFocus defers the focus request to the frame in which the editor is
	// actually laid out; a tag the router has not seen yet cannot take focus.
	wantFocus bool
}

// commentIndent is the width of the rule and margin down the left of a note,
// which lines its text up clear of the diff's gutter.
const commentIndent = 26

func (a *App) commentCols(gtx layout.Context, width int) int {
	cell := a.ui.Cell(gtx, reef.SizeUI, false)
	return max(8, (width-gtx.Dp(commentIndent)-gtx.Dp(reef.Sp5))/cell.X)
}

func (a *App) commentHeight(gtx layout.Context, body string, width, row int) int {
	lines := reef.Wrap(body, a.commentCols(gtx, width))
	return row*(len(lines)+1) + gtx.Dp(reef.Sp3)
}

// commentRow draws a stored note under the line it belongs to.
func (a *App) commentRow(gtx layout.Context, c *state.Comment, cursor bool) {
	ui := a.ui
	size := gtx.Constraints.Max
	row := ui.TextRow(gtx, reef.SizeUI)

	// A note is seated on the panel tint with the same two pixel edge a
	// selected row wears: moss while it is open, faint once it is resolved,
	// ink where the cursor is.
	reef.Fill(gtx, size, ui.P.BgAlt)
	bar := ui.P.Action
	if c.Resolved {
		bar = ui.P.Faint
	}
	if cursor {
		bar = ui.P.Strong
	}
	reef.Edge(gtx, size.Y, bar)
	reef.HLine(gtx, size.X, 0, ui.P.RuleFaint)
	reef.HLine(gtx, size.X, size.Y-1, ui.P.RuleFaint)

	x := gtx.Dp(commentIndent)
	header := "NOTE " + c.CreatedAt.Format("2006-01-02 15:04")
	if c.Span() {
		header += " · LINES " + strconv.Itoa(c.Line) + "-" + strconv.Itoa(c.LastLine())
	}
	if c.Resolved {
		header += " · RESOLVED"
	}
	lh := ui.Cell(gtx, reef.SizeLabel, true).Y
	fit(gtx, image.Pt(x, gtx.Dp(reef.Sp2)+(row-lh)/2), image.Pt(size.X-x, gtx.Constraints.Max.Y), func(gtx layout.Context) {
		ui.Label(gtx, ui.P.Faint, header)
	})

	// What can be done to a note is spelled out on the note itself, rather
	// than living only in the help sheet.
	a.noteControls(gtx, c, size, row)

	fg := ui.P.Fg
	if c.Resolved {
		fg = ui.P.Muted
	}
	for i, line := range reef.Wrap(c.Body, a.commentCols(gtx, size.X)) {
		y := gtx.Dp(reef.Sp2) + row*(i+1)
		fit(gtx, image.Pt(0, y), image.Pt(size.X, row), func(gtx layout.Context) {
			a.cellText(gtx, x, row, size.X-gtx.Dp(reef.PadInline), font.Normal, fg, line)
		})
	}
}

// noteTag identifies one control on one note.
type noteTag struct {
	id     string
	action string
}

// noteControls draws the actions available on a note along the top right of it.
func (a *App) noteControls(gtx layout.Context, c *state.Comment, size image.Point, row int) {
	ui := a.ui
	rightX := size.X - gtx.Dp(reef.PadInline)

	strip := gtx
	strip.Constraints = layout.Exact(image.Pt(size.X, row+gtx.Dp(reef.Sp4)))

	rightX -= a.controlRight(strip, rightX, row+gtx.Dp(reef.Sp4), noteTag{c.ID, "delete"},
		"DELETE  D", ui.P.Muted, func() {
			a.after(func() {
				a.store.DeleteComment(a.rev.ChangeIDFull, c.ID)
				a.afterCommentChange(c.Path)
				a.note("note deleted")
			})
		}) + gtx.Dp(reef.Sp3)

	rightX -= a.controlRight(strip, rightX, row+gtx.Dp(reef.Sp4), noteTag{c.ID, "edit"},
		"EDIT  C", ui.P.Muted, func() {
			a.after(func() {
				a.cursorToNote(c.ID)
				a.startComment()
			})
		}) + gtx.Dp(reef.Sp3)

	label, colour := "RESOLVE  ⏎", ui.P.AddFg
	if c.Resolved {
		label, colour = "REOPEN  ⏎", ui.P.Muted
	}
	a.controlRight(strip, rightX, row+gtx.Dp(reef.Sp4), noteTag{c.ID, "resolve"}, label, colour, func() {
		a.after(func() {
			a.store.ToggleResolved(a.rev.ChangeIDFull, c.ID)
			a.afterCommentChange(c.Path)
		})
	})
}

// cursorToNote puts the diff cursor on a note, so a control acts on the same
// thing the keyboard would.
func (a *App) cursorToNote(id string) {
	doc := a.diff
	if doc == nil {
		return
	}
	for i := range doc.Rows {
		if r := doc.Row(i); r.Kind == rowComment && r.Comment != nil && r.Comment.ID == id {
			doc.Cursor = i
			a.focus = PaneDiff
			return
		}
	}
}

func (a *App) draftHeight(gtx layout.Context, width, row int) int {
	if a.draft == nil {
		return row
	}
	lines := max(3, len(reef.Wrap(a.draft.field.Text(), a.commentCols(gtx, width)))+1)
	return row*(lines+1) + gtx.Dp(reef.Sp4)
}

// draftRow draws the editor for the note being written.
func (a *App) draftRow(gtx layout.Context) {
	ui := a.ui
	size := gtx.Constraints.Max
	row := ui.TextRow(gtx, reef.SizeUI)
	d := a.draft
	if d == nil {
		return
	}

	reef.Fill(gtx, size, ui.P.BgAlt)
	reef.Edge(gtx, size.Y, ui.P.Action)
	reef.HLine(gtx, size.X, 0, ui.P.Action)
	reef.HLine(gtx, size.X, size.Y-1, ui.P.Action)

	x := gtx.Dp(commentIndent)
	lh := ui.Cell(gtx, reef.SizeLabel, true).Y
	head := "NEW NOTE · CMD-ENTER SAVE · ESC CANCEL"
	if d.edit != "" {
		head = "EDIT NOTE · CMD-ENTER SAVE · ESC CANCEL"
	}
	if d.endLine > d.line {
		head = "NEW NOTE ON LINES " + strconv.Itoa(d.line) + "-" + strconv.Itoa(d.endLine) +
			" · CMD-ENTER SAVE · ESC CANCEL"
	}
	fit(gtx, image.Pt(x, gtx.Dp(reef.Sp2)+(row-lh)/2), image.Pt(size.X-x, gtx.Constraints.Max.Y), func(gtx layout.Context) {
		ui.Label(gtx, ui.P.Action, head)
	})

	field := image.Pt(max(0, size.X-x-gtx.Dp(reef.PadInline)), max(row, size.Y-row-gtx.Dp(reef.PadInline)))
	fill(gtx, image.Pt(x, gtx.Dp(reef.Sp2)+row), field, func(gtx layout.Context) {
		d.field.Layout(gtx, ui.Theme)
	})
	if d.wantFocus {
		d.field.Focus(gtx)
		d.wantFocus = false
	}
}

// startComment opens the editor against the line the cursor is on, or revises
// the note the cursor is already sitting in.
func (a *App) startComment() {
	doc := a.diff
	if doc == nil || doc.Cursor >= len(doc.Rows) {
		return
	}
	a.focus = PaneDiff
	r := doc.Row(doc.Cursor)
	path := doc.PathAt(doc.Cursor)
	if path == "" {
		return
	}

	if r.Kind == rowComment {
		a.draft = &draft{
			path: path, side: r.Comment.Side, line: r.Comment.Line,
			field: reef.NewMultiField(r.Comment.Body), edit: r.Comment.ID, wantFocus: true,
		}
		a.rebuildRows()
		return
	}
	// A rendered document block is numbered in the new file, so a note on it
	// covers the source lines the block was made from.
	if r.Kind == rowPretty && r.Pretty != nil {
		b := r.Pretty
		if b.StartLine == 0 {
			return
		}
		endLine := 0
		if b.EndLine > b.StartLine {
			endLine = b.EndLine
		}
		a.clearSelection()
		a.draft = &draft{
			path: path, side: state.SideNew, line: b.StartLine, endLine: endLine,
			field: reef.NewMultiField(""), wantFocus: true,
		}
		a.rebuildRows()
		return
	}
	if r.Kind != rowLine {
		return
	}

	// A run swept out with the pointer or with shift-j is what the note is
	// about; without one it is the line the cursor is on.
	side, line, endLine := state.SideNew, r.Line.NewNum, 0
	if r.Line.Kind == diffparse.Removed {
		side, line = state.SideOld, r.Line.OldNum
	}
	if first, last, ok := a.selectedLines(); ok && last > first {
		if s, from, to, good := a.selectedRange(first, last); good {
			side, line, endLine = s, from, to
		}
	}
	if line == 0 {
		return
	}
	a.clearSelection()
	a.draft = &draft{
		path: path, side: side, line: line, endLine: endLine,
		field: reef.NewMultiField(""), wantFocus: true,
	}
	a.rebuildRows()
}

func (a *App) saveDraft(gtx layout.Context) {
	d := a.draft
	if d == nil {
		return
	}
	body := strings.TrimSpace(d.field.Text())
	if body == "" {
		a.cancelDraft(gtx)
		return
	}
	if d.edit != "" {
		a.store.UpdateComment(a.rev.ChangeIDFull, d.edit, body)
	} else {
		a.store.AddComment(a.rev.ChangeIDFull, state.Comment{
			Path: d.path, Side: d.side, Line: d.line, EndLine: d.endLine,
			Body: body, CommitID: a.rev.CommitIDFull,
		})
	}
	path := d.path
	a.draft = nil
	gtx.Execute(key.FocusCmd{})
	a.afterCommentChange(path)
	a.note("note added")
}

func (a *App) cancelDraft(gtx layout.Context) {
	if a.draft == nil {
		return
	}
	a.draft = nil
	gtx.Execute(key.FocusCmd{})
	a.rebuildRows()
}

// commentUnderCursor is the note the diff cursor is sitting in, if it is
// sitting in one.
func (a *App) commentUnderCursor() (*state.Comment, bool) {
	doc := a.diff
	if doc == nil || doc.Cursor >= len(doc.Rows) {
		return nil, false
	}
	if r := doc.Row(doc.Cursor); r.Kind == rowComment && r.Comment != nil {
		return r.Comment, true
	}
	return nil, false
}

func (a *App) deleteCommentUnderCursor() {
	c, ok := a.commentUnderCursor()
	if !ok {
		return
	}
	a.store.DeleteComment(a.rev.ChangeIDFull, c.ID)
	a.afterCommentChange(c.Path)
	a.note("note deleted")
}

func (a *App) toggleResolvedUnderCursor() {
	c, ok := a.commentUnderCursor()
	if !ok {
		return
	}
	a.store.ToggleResolved(a.rev.ChangeIDFull, c.ID)
	a.afterCommentChange(c.Path)
}

// afterCommentChange brings the manifest row and the diff rows back into step
// with the store, which every edit to a note has to do. A note belongs to one
// file, so only that file's rows are put back together; an empty path means
// the change was wider than that.
func (a *App) afterCommentChange(path string) {
	if i := a.fileIndex(path); i >= 0 {
		a.refreshRow(i)
		a.rebuildFile(i)
		return
	}
	for i := range a.files {
		a.refreshRow(i)
	}
	a.rebuildRows()
}

// fileIndex finds a path in the sheet, or -1.
func (a *App) fileIndex(path string) int {
	if a.diff == nil || path == "" {
		return -1
	}
	for i, fd := range a.diff.Files {
		if fd.Path == path {
			return i
		}
	}
	return -1
}

// jumpComment moves the cursor to the next or previous note in the file.
func (a *App) jumpComment(dir int) {
	doc := a.diff
	if doc == nil {
		return
	}
	for i := doc.Cursor + dir; i >= 0 && i < len(doc.Rows); i += dir {
		if doc.Row(i).Kind == rowComment {
			doc.Cursor = i
			a.focus = PaneDiff
			a.scrollTo(i)
			return
		}
	}
	a.note("no more notes in this file")
}
