package ui

import (
	"gioui.org/layout"
)

// move steps the selection in whichever pane has focus.
func (a *App) move(delta int) { a.goTo(func(at, _ int) int { return at + delta }) }

// moveTo jumps to an absolute position, clamped to the list.
func (a *App) moveTo(pos int) { a.goTo(func(_, _ int) int { return pos }) }

// goTo moves the cursor of the focused pane. where is handed the cursor's
// current position and the length of the list, and returns where it should
// land; the result is clamped, so a jump past either end simply stops there.
func (a *App) goTo(where func(at, n int) int) {
	a.clearSelection()
	switch {
	case a.focus == PaneRevs && !a.classic():
		a.moveReview(where)
	case a.focus == PaneDiff && !a.classic():
		a.movePlan(where)
	case a.focus == PaneRevs:
		if n := len(a.revs); n > 0 {
			a.selectRev(clamp(where(a.revSel, n), 0, n-1))
			a.scrollList(&a.revList, a.revSel)
		}
	case a.focus == PaneFiles:
		vis := a.visibleFiles()
		if n := len(vis); n > 0 {
			// Map current absolute to visible index.
			cur := 0
			for i, v := range vis {
				if v == a.fileSel {
					cur = i
					break
				}
			}
			nextVis := clamp(where(cur, n), 0, n-1)
			abs := vis[nextVis]
			a.selectFile(abs)
			a.scrollList(&a.fileList, nextVis)
		}
	case a.focus == PaneDiff:
		if a.diff == nil {
			return
		}
		if n := len(a.diff.Rows); n > 0 {
			a.diff.Cursor = clamp(where(a.diff.Cursor, n), 0, n-1)
			a.scrollTo(a.diff.Cursor)
		}
	}
}

// page moves by roughly a screenful.
func (a *App) page(gtx layout.Context, dir int) {
	h := a.ui.Row(gtx)
	if a.focus == PaneDiff {
		h = a.ui.CodeRow(gtx)
	}
	rows := 20
	if h > 0 {
		rows = max(1, (gtx.Constraints.Max.Y/h)-2)
	}
	a.move(rows * dir)
}

// scrollList keeps the selected row of a plain list on screen.
func (a *App) scrollList(list *layout.List, index int) {
	if index < list.Position.First {
		list.Position.First = index
		list.Position.Offset = 0
		return
	}
	// Position.Count is how many rows the last frame drew, so this reacts to
	// the actual window size rather than to a guess.
	if last := list.Position.First + list.Position.Count - 1; list.Position.Count > 0 && index > last {
		list.Position.First += index - last
		list.Position.Offset = 0
	}
}

func (a *App) scrollTo(index int) {
	a.scrollList(&a.diffList, index)
}

// stepFile moves to the next or previous file and puts focus on the diff, the
// motion of working steadily through a change.
func (a *App) stepFile(delta int) {
	if len(a.files) == 0 {
		return
	}
	vis := a.visibleFiles()
	if len(vis) == 0 {
		a.note("no matching files")
		return
	}
	curVis := 0
	for i, v := range vis {
		if v == a.fileSel {
			curVis = i
			break
		}
	}
	nextVis := clamp(curVis+delta, 0, len(vis)-1)
	if vis[nextVis] == a.fileSel {
		a.note("no more files")
		return
	}
	a.selectFile(vis[nextVis])
	a.scrollList(&a.fileList, nextVis)
	a.focus = PaneDiff
}

// expandHere opens the run of unchanged lines the cursor is on, or the nearest
// one below it.
func (a *App) expandHere() {
	doc := a.diff
	if doc == nil || len(doc.Rows) == 0 {
		return
	}
	a.focus = PaneDiff
	for i := doc.Cursor; i < len(doc.Rows); i++ {
		if doc.Row(i).Kind == rowGap {
			a.expandGap(i, expandStep)
			return
		}
	}
	for i := doc.Cursor - 1; i >= 0; i-- {
		if doc.Row(i).Kind == rowGap {
			a.expandGap(i, expandStep)
			return
		}
	}
	a.note("nothing left out here")
}

// expandWholeFile shows every line of the file the cursor is in.
func (a *App) expandWholeFile() {
	doc := a.diff
	if doc == nil || len(doc.Rows) == 0 {
		return
	}
	a.focus = PaneDiff
	i := doc.FileOf(clamp(doc.Cursor, 0, len(doc.Rows)-1))
	if fd := doc.Files[i]; fd.read && !fd.Collapsed() {
		a.collapseFile(i)
		return
	}
	a.expandFile(i)
}

// stepHunk moves the diff cursor between hunk headers.
func (a *App) stepHunk(delta int) {
	doc := a.diff
	if doc == nil || len(doc.Hunks) == 0 {
		return
	}
	a.focus = PaneDiff
	if delta > 0 {
		for _, h := range doc.Hunks {
			if h > doc.Cursor {
				doc.Cursor = h
				a.scrollTo(h)
				return
			}
		}
	} else {
		for i := len(doc.Hunks) - 1; i >= 0; i-- {
			if doc.Hunks[i] < doc.Cursor {
				doc.Cursor = doc.Hunks[i]
				a.scrollTo(doc.Hunks[i])
				return
			}
		}
	}
	a.note("no more hunks")
}

// setViewed marks one file read or unread, without moving on.
func (a *App) setViewed(i int, want bool) {
	if i < 0 || i >= len(a.files) {
		return
	}
	f := &a.files[i]
	a.store.SetViewed(a.rev.ChangeIDFull, f.Path, a.rev.CommitIDFull, want)
	a.refreshRow(i)
	if want {
		a.note("read %s", f.Path)
	} else {
		a.note("unmarked %s", f.Path)
	}
}

// toggleViewed marks the selected file read, or unmarks it, and moves on.
// Keyboard review is a sequence, so marking a file is nearly always followed
// by wanting the next one.
func (a *App) toggleViewed() {
	if a.fileSel >= len(a.files) {
		return
	}
	want := !a.files[a.fileSel].Viewed || a.files[a.fileSel].Stale
	a.setViewed(a.fileSel, want)
	if want && a.fileSel+1 < len(a.files) {
		a.stepFile(1)
	}
}

// readCount reports how many files have been read at the current version.
func (a *App) readCount() (read, total int) {
	for _, f := range a.files {
		if f.Viewed && !f.Stale {
			read++
		}
	}
	return read, len(a.files)
}

func (a *App) markAllViewed() {
	for i := range a.files {
		a.store.SetViewed(a.rev.ChangeIDFull, a.files[i].Path, a.rev.CommitIDFull, true)
		a.refreshRow(i)
	}
	a.note("marked %d files viewed", len(a.files))
}
