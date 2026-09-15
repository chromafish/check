package ui

import (
	"strings"

	"gioui.org/io/key"
	"gioui.org/layout"
)

// rebuildFind recomputes the rows matching the find query.
func (a *App) rebuildFind() {
	if a.diff == nil || a.findField == nil {
		a.findHits = nil
		a.findAt = -1
		return
	}
	q := strings.TrimSpace(a.findField.Text())
	if q == "" {
		a.findHits = nil
		a.findAt = -1
		return
	}
	lower := strings.ToLower(q)
	var hits []int
	for i := range a.diff.Rows {
		if a.diff.Row(i).Kind != rowLine {
			continue
		}
		text := a.diff.Row(i).Line.Text
		if strings.Contains(strings.ToLower(text), lower) {
			hits = append(hits, i)
		}
	}
	if len(hits) == 0 {
		a.findHits = hits
		a.findAt = -1
		return
	}
	a.findHits = hits
	if a.findAt >= 0 && a.findAt < len(a.findHits) {
		// Keep current index if still in bounds.
		return
	}
	cur := 0
	if a.diff != nil {
		cur = a.diff.Cursor
	}
	at := 0
	for i, r := range a.findHits {
		if r >= cur {
			at = i
			break
		}
		// Otherwise first hit after cursor not found, wrap to 0.
		if i == len(a.findHits)-1 {
			at = 0
		}
	}
	a.findAt = at
}

// findRanges returns column ranges of the query within a row's text, in
// display columns. Only for rowLine rows.
func (a *App) findRanges(row int) [][2]int {
	if a.findField == nil || a.diff == nil || row < 0 || row >= len(a.diff.Rows) {
		return nil
	}
	q := strings.TrimSpace(a.findField.Text())
	if q == "" {
		return nil
	}
	r := a.diff.Row(row)
	if r.Kind != rowLine {
		return nil
	}
	lowerText := strings.ToLower(r.Line.Text)
	lowerQ := strings.ToLower(q)
	var out [][2]int
	start := 0
	for {
		idx := strings.Index(lowerText[start:], lowerQ)
		if idx < 0 {
			break
		}
		s := start + idx
		e := s + len(q)
		// Convert byte offsets to display columns (tabs expanded).
		c0 := displayWidth(r.Line.Text[:s])
		c1 := c0 + displayWidth(r.Line.Text[s:e])
		out = append(out, [2]int{c0, c1})
		start = e
		if start >= len(lowerText) {
			break
		}
	}
	return out
}

// isFindCurrent reports whether row is the current find match.
func (a *App) isFindCurrent(row int) bool {
	if a.findAt < 0 || a.findAt >= len(a.findHits) {
		return false
	}
	return a.findHits[a.findAt] == row
}

// updateFind processes the find field once per frame. Returns whether the
// field is focused.
func (a *App) updateFind(gtx layout.Context) bool {
	if a.findField == nil {
		return false
	}
	prev := a.findField.Text()
	text, submitted := a.findField.Update(gtx)
	if text != prev {
		a.rebuildFind()
	}
	if submitted {
		// Enter jumps to current or next.
		if len(a.findHits) > 0 {
			a.jumpFind(1)
		}
	}
	return a.findField.Focused()
}

// startFind focuses the diff find bar.
func (a *App) startFind(gtx layout.Context) {
	if a.findField == nil {
		return
	}
	a.focus = PaneDiff
	a.findField.Focus(gtx)
	a.rebuildFind()
	if len(a.findHits) > 0 && a.findAt >= 0 {
		row := a.findHits[a.findAt]
		if a.diff != nil {
			a.diff.Cursor = row
			a.scrollTo(row)
		}
	}
}

// closeFind clears and unfocuses find.
func (a *App) closeFind(gtx layout.Context) {
	if a.findField != nil {
		a.findField.SetText("")
		a.findField.Defocus(gtx)
	}
	a.findHits = nil
	a.findAt = -1
	gtx.Execute(key.FocusCmd{Tag: nil})
}

// jumpFind moves to the next (dir=1) or previous (dir=-1) match, wrapping.
func (a *App) jumpFind(dir int) {
	if len(a.findHits) == 0 {
		a.note("no matches")
		return
	}
	if a.findAt < 0 {
		a.findAt = 0
	} else {
		a.findAt = (a.findAt + dir + len(a.findHits)) % len(a.findHits)
	}
	row := a.findHits[a.findAt]
	if a.diff != nil {
		a.diff.Cursor = row
	}
	a.focus = PaneDiff
	a.scrollTo(row)
	if q := strings.TrimSpace(a.findField.Text()); q != "" {
		a.note("find %d/%d", a.findAt+1, len(a.findHits))
	}
}

// visibleFiles returns indices of files passing the manifest filter. When the
// filter is empty, all files are visible; when non-empty it filters even
// after defocusing, until cleared.
func (a *App) visibleFiles() []int {
	q := ""
	if a.fileFilter != nil {
		q = strings.ToLower(strings.TrimSpace(a.fileFilter.Text()))
	}
	if q == "" {
		out := make([]int, len(a.files))
		for i := range out {
			out[i] = i
		}
		return out
	}
	var out []int
	for i, f := range a.files {
		if strings.Contains(strings.ToLower(f.Display()), q) || strings.Contains(strings.ToLower(f.Path), q) {
			out = append(out, i)
		}
	}
	return out
}

// visiblePos returns the position of absolute file index in the visible list, or -1.
func (a *App) visiblePos(abs int) int {
	vis := a.visibleFiles()
	for i, v := range vis {
		if v == abs {
			return i
		}
	}
	return -1
}

// scrollFileList keeps the manifest's cursor visible, accounting for filtering.
func (a *App) scrollFileList(abs int) {
	vis := a.visibleFiles()
	for i, v := range vis {
		if v == abs {
			a.scrollList(&a.fileList, i)
			return
		}
	}
	// If hidden by filter, clear filter to show it.
	if a.fileFilter != nil && a.fileFilter.Text() != "" {
		// Keep selection but don't scroll; user sees NO MATCH.
	}
}

// updateFileFilter processes the manifest filter field once per frame.
func (a *App) updateFileFilter(gtx layout.Context) bool {
	if a.fileFilter == nil {
		return false
	}
	prev := a.fileFilter.Text()
	_, _ = a.fileFilter.Update(gtx)
	cur := a.fileFilter.Text()
	if cur != prev {
		// If current selection hidden, jump to first visible.
		if a.visiblePos(a.fileSel) < 0 {
			if vis := a.visibleFiles(); len(vis) > 0 {
				a.selectFile(vis[0])
				// Reset scroll to top of filtered list.
				a.fileList.Position.First = 0
				a.fileList.Position.Offset = 0
			}
		}
	}
	return a.fileFilter.Focused()
}

func (a *App) startFileFilter(gtx layout.Context) {
	if a.fileFilter == nil {
		return
	}
	a.focus = PaneFiles
	a.fileFilter.Focus(gtx)
}

func (a *App) clearFileFilter(gtx layout.Context) {
	if a.fileFilter == nil {
		return
	}
	a.fileFilter.SetText("")
	a.fileFilter.Defocus(gtx)
	gtx.Execute(key.FocusCmd{Tag: nil})
}

// jumpUnread moves to the next (dir=1) or previous (dir=-1) file not marked viewed.
func (a *App) jumpUnread(dir int) {
	if len(a.files) == 0 {
		return
	}
	vis := a.visibleFiles()
	if len(vis) == 0 {
		a.note("no matching files")
		return
	}
	// Find current position in visible list.
	curVis := -1
	for i, v := range vis {
		if v == a.fileSel {
			curVis = i
			break
		}
	}
	if curVis < 0 {
		curVis = 0
	}
	n := len(vis)
	for step := 1; step <= n; step++ {
		idxVis := (curVis + dir*step + n) % n
		abs := vis[idxVis]
		f := a.files[abs]
		if !f.Viewed || f.Stale {
			a.focus = PaneFiles
			a.selectFile(abs)
			a.scrollList(&a.fileList, idxVis)
			a.note("unread %s", f.Path)
			return
		}
	}
	a.note("no unread files")
}

// jumpOpen moves to the next (dir=1) or previous (dir=-1) unresolved note.
func (a *App) jumpOpen(dir int) {
	doc := a.diff
	if doc == nil {
		a.note("no diff")
		return
	}
	cur := doc.Cursor
	// Scan outward wrapping once.
	n := len(doc.Rows)
	if n == 0 {
		return
	}
	for step := 1; step <= n; step++ {
		i := cur + dir*step
		// Wrap around.
		if i < 0 {
			i += n
		} else if i >= n {
			i -= n
		}
		if i < 0 || i >= n {
			continue
		}
		r := doc.Row(i)
		if r.Kind == rowComment && r.Comment != nil && !r.Comment.Resolved {
			doc.Cursor = i
			a.focus = PaneDiff
			a.scrollTo(i)
			// Keep manifest in step.
			if fi := doc.FileOf(i); fi >= 0 {
				a.fileSel = fi
				if pos := a.visiblePos(fi); pos >= 0 {
					a.scrollList(&a.fileList, pos)
				}
			}
			a.note("open note")
			return
		}
	}
	a.note("no open notes")
}
