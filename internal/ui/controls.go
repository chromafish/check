package ui

import (
	"image"

	"gioui.org/io/event"
	"gioui.org/io/pointer"
	"gioui.org/layout"

	"github.com/chromafish/check/reef"
)

// Short local names for the design system's controls, so that a row of the
// manifest reads as a row rather than as a sequence of qualified calls.

func (a *App) hover(gtx layout.Context, r image.Rectangle, tag event.Tag, onClick func()) {
	a.ui.Area(gtx, r, tag, pointer.CursorPointer, onClick)
}

func (a *App) hovered(tag event.Tag) bool { return a.ui.Hovered(tag) }

func (a *App) clickable(gtx layout.Context, size image.Point, tag event.Tag, onClick func()) {
	a.ui.Click(gtx, size, tag, onClick)
}

func (a *App) clickArea(gtx layout.Context, r image.Rectangle, tag event.Tag, onClick func()) {
	a.ui.Area(gtx, r, tag, pointer.CursorDefault, onClick)
}

func (a *App) control(gtx layout.Context, x, height int, tag event.Tag, label string, c reef.ColorNRGBA, onClick func()) int {
	return a.ui.Control(gtx, x, height, tag, label, c, onClick)
}

func (a *App) controlRight(gtx layout.Context, rightX, height int, tag event.Tag, label string, c reef.ColorNRGBA, onClick func()) int {
	return a.ui.ControlRight(gtx, rightX, height, tag, label, c, onClick)
}

// tag names a control so its hover state survives between frames without
// having to hang a field off App for each one.
type tag string

const (
	tagSplit      tag = "split"
	tagWrap       tag = "wrap"
	tagHelp       tag = "help"
	tagNotes      tag = "notes"
	tagCopy       tag = "copy"
	tagSettings   tag = "settings"
	tagClearNotes tag = "clear-notes"
)

// viewedTag marks the read/unread box of one row. The same box is drawn in the
// manifest and on the file's heading in the diff, and they hover separately.
type viewedTag struct {
	row  int
	head bool
}
