package ui

import (
	"context"
	"fmt"
	"image"
	"strings"

	"gioui.org/font"
	"gioui.org/layout"

	"github.com/chromafish/check/internal/vcs"
	"github.com/chromafish/check/reef"
)

// The review column is the brief view's left edge: what there is to review,
// as a person names it.
//
// Uncommitted work comes first, and only when there is some. Then the branch
// the working copy is on, open onto its commits: its own ones when it has
// left the trunk, and otherwise its recent history, since a branch everything
// goes straight onto is reviewed a commit at a time. Every other branch
// follows, closed until it is chosen; a branch with commits of its own is
// then reviewed whole, and opens to list them.

// reviewKind is what one row of the column stands for.
type reviewKind int

const (
	reviewWorking reviewKind = iota // uncommitted work, or HEAD when detached
	reviewBranch                    // a branch
	reviewCommit                    // one commit listed under a branch
)

// reviewRow is one row of the column. key names it across reloads, which
// renumber the rows but not what they are.
type reviewRow struct {
	kind reviewKind
	name string // the branch
	rev  vcs.Revision
	key  string
}

func branchKey(name string) string { return "b:" + name }

func commitKey(branch string, rev vcs.Revision) string {
	return "c:" + branch + ":" + rev.CommitIDFull
}

// classic reports whether the three-column revision view is the one on screen.
func (a *App) classic() bool { return a.settings.Classic || a.drilled }

// toggleClassic moves between the brief and the classic view, and records the
// choice. Whatever is under review stays under review.
func (a *App) toggleClassic() {
	if a.undrill() {
		return
	}
	a.settings.Classic = !a.settings.Classic
	a.saveSettings()
	a.swapFractions()
	if a.classic() {
		a.note("classic view")
		return
	}
	a.note("brief view")
	if a.focus == PaneFiles {
		a.focus = PaneRevs
	}
}

// The two views divide the window differently: the brief view is the
// navigator and the diff, where the classic view has three columns. Each
// view keeps its own split, dragged or not, across a switch.
var (
	classicFractions = [2]float32{0.22, 0.23}
	briefFractions   = [2]float32{0.24, 0}
)

// swapFractions puts the split of the view now on screen in place, keeping
// the other's as it was left.
func (a *App) swapFractions() {
	if a.splits == nil {
		return
	}
	cur := [2]float32{a.splits.Fraction(0), a.splits.Fraction(1)}
	next := a.otherFractions
	if next == ([2]float32{}) {
		next = briefFractions
		if a.classic() {
			next = classicFractions
		}
	}
	a.otherFractions = cur
	a.splits.SetFraction(0, next[0])
	if next[1] > 0 {
		a.splits.SetFraction(1, next[1])
	}
}

// uncommitted is the working copy when it holds work not yet committed.
func (a *App) uncommitted() (vcs.Revision, bool) {
	for _, r := range a.revs {
		if r.WorkingCopy && !r.Empty {
			return r, true
		}
	}
	return vcs.Revision{}, false
}

// branchOrder is the branches as the column lists them: the current one
// first, the rest as the tool listed them.
func (a *App) branchOrder() []string {
	names := make([]string, 0, len(a.branches))
	if a.current != "" {
		names = append(names, a.current)
	}
	for _, n := range a.branches {
		if n != a.current {
			names = append(names, n)
		}
	}
	return names
}

// branchCommits is what a branch opens onto: its own commits, or its history
// when it has none.
func branchCommits(b *vcs.Branch) []vcs.Revision {
	if b == nil {
		return nil
	}
	if len(b.Commits) > 0 {
		return b.Commits
	}
	return b.History
}

// reviewRows is the column as it stands.
func (a *App) reviewRows() []reviewRow {
	var rows []reviewRow
	if w, ok := a.uncommitted(); ok {
		rows = append(rows, reviewRow{kind: reviewWorking, rev: w, key: "@"})
	} else if a.current == "" && len(a.revs) > 0 {
		// Detached, with nothing uncommitted: what is checked out is still
		// somewhere to start.
		rows = append(rows, reviewRow{kind: reviewWorking, rev: a.revs[0], key: "@"})
	}
	for _, n := range a.branchOrder() {
		rows = append(rows, reviewRow{kind: reviewBranch, name: n, key: branchKey(n)})
		if !a.opened[n] {
			continue
		}
		for _, c := range branchCommits(a.details[n]) {
			rows = append(rows, reviewRow{kind: reviewCommit, name: n, rev: c, key: commitKey(n, c)})
		}
	}
	return rows
}

// reviewAt is the index of the row the column's cursor is on, or -1.
func (a *App) reviewAt(rows []reviewRow) int {
	for i, r := range rows {
		if r.key == a.reviewKey {
			return i
		}
	}
	return -1
}

// review puts a diff under review: the manifest, the diff and the brief all
// follow from it.
func (a *App) review(rev vcs.Revision, spec vcs.DiffSpec) {
	a.rev = rev
	a.spec = spec
	a.loadFiles()
}

// reviewCommitOf reviews one commit.
func (a *App) reviewCommitOf(rev vcs.Revision) {
	a.review(rev, vcs.DiffSpec{Kind: vcs.DiffChange, Rev: rev.ChangeID})
}

// selectReview puts one row of the column under review.
func (a *App) selectReview(r reviewRow) {
	a.reviewKey = r.key
	switch r.kind {
	case reviewWorking:
		a.reviewCommitOf(r.rev)
	case reviewCommit:
		a.reviewCommitOf(r.rev)
	case reviewBranch:
		a.openBranch(r.name, "", false)
	}
}

// openBranch reads a branch, opens it in the column, and reviews it: the
// whole of it when it has commits of its own, or the commit named by commit
// when that is still listed under it. A branch with none of its own is only
// opened, unless first is set, when its newest commit is reviewed.
func (a *App) openBranch(name, commit string, first bool) {
	if a.repo == nil {
		return
	}
	a.branchGen++
	gen := a.branchGen
	a.background(func(ctx context.Context) func() {
		b, err := a.repo.Branch(ctx, name)
		return func() {
			if gen != a.branchGen {
				return
			}
			if err != nil {
				a.fail(err)
				return
			}
			a.adoptBranch(&b)
			for _, c := range branchCommits(&b) {
				if commit != "" && c.CommitIDFull == commit {
					a.reviewKey = commitKey(name, c)
					a.reviewCommitOf(c)
					return
				}
			}
			switch commits := branchCommits(&b); {
			case len(b.Commits) > 0:
				a.reviewKey = branchKey(name)
				a.reviewBranch(b)
			case first && len(commits) > 0:
				a.reviewKey = commitKey(name, commits[0])
				a.reviewCommitOf(commits[0])
			default:
				a.reviewKey = branchKey(name)
			}
		}
	})
}

// adoptBranch records a branch's details and opens it in the column, closing
// any other that is not the current one: the column shows where the working
// copy is and where the cursor is, and nothing else.
func (a *App) adoptBranch(b *vcs.Branch) {
	if a.details == nil {
		a.details = map[string]*vcs.Branch{}
	}
	a.details[b.Name] = b
	for n := range a.opened {
		if n != a.current {
			delete(a.opened, n)
		}
	}
	if a.opened == nil {
		a.opened = map[string]bool{}
	}
	a.opened[b.Name] = true
}

// reviewBranch reviews a branch whole.
func (a *App) reviewBranch(b vcs.Branch) {
	// Notes and read marks are filed under the branch rather than any one
	// commit of it, and a read mark goes stale when the tip moves.
	rev := b.Tip
	rev.ChangeID = b.Name
	rev.ChangeIDFull = "branch:" + b.Name
	rev.WorkingCopy = false
	a.review(rev, b.Spec())
}

// restoreReview puts back what was under review after the column's lists
// were read again. With nothing to put back, it starts on uncommitted work
// when there is some, and otherwise on the newest commit of the current
// branch.
func (a *App) restoreReview() {
	has := func(name string) bool {
		for _, n := range a.branches {
			if n == name {
				return true
			}
		}
		return false
	}
	switch key := a.reviewKey; {
	case strings.HasPrefix(key, "b:") && has(key[2:]):
		a.openBranch(key[2:], "", true)
		return
	case strings.HasPrefix(key, "c:"):
		rest := key[2:]
		if i := strings.LastIndex(rest, ":"); i > 0 && has(rest[:i]) {
			a.openBranch(rest[:i], rest[i+1:], true)
			return
		}
	case key == "@":
		if w, ok := a.uncommitted(); ok {
			a.reviewCommitOf(w)
			return
		}
	}
	if w, ok := a.uncommitted(); ok {
		a.reviewKey = "@"
		a.reviewCommitOf(w)
		return
	}
	if a.current != "" {
		a.openBranch(a.current, "", true)
		return
	}
	rows := a.reviewRows()
	if len(rows) == 0 {
		a.selectRev(-1)
		return
	}
	a.selectReview(rows[0])
}

// moveReview steps the column's cursor, putting what it lands on under
// review as it goes, the way the revisions column does.
func (a *App) moveReview(where func(at, n int) int) {
	rows := a.reviewRows()
	if len(rows) == 0 {
		return
	}
	at := max(a.reviewAt(rows), 0)
	next := clamp(where(at, len(rows)), 0, len(rows)-1)
	if next == at && a.reviewAt(rows) >= 0 {
		return
	}
	a.selectReview(rows[next])
	a.scrollList(&a.reviewList, next)
}

// reviewTag names one row of the column for the pointer.
type reviewTag struct{ key string }

func (a *App) layoutReview(gtx layout.Context) {
	ui := a.ui
	size := gtx.Constraints.Max
	rows := a.reviewRows()
	if len(rows) == 0 {
		a.placeholder(gtx, "NOTHING TO REVIEW")
		return
	}
	row := ui.Row(gtx)
	cell := ui.Cell(gtx, reef.SizeUI, false)
	at := a.reviewAt(rows)
	a.reviewList.Layout(gtx, len(rows), func(gtx layout.Context, i int) layout.Dimensions {
		gtx.Constraints = layout.Exact(image.Pt(size.X, row))
		a.reviewRowView(gtx, rows[i], i == at, cell)
		return layout.Dimensions{Size: image.Pt(size.X, row)}
	})
}

func (a *App) reviewRowView(gtx layout.Context, r reviewRow, selected bool, cell image.Point) {
	ui := a.ui
	size := gtx.Constraints.Max
	tag := reviewTag{r.key}
	switch {
	case selected:
		ui.Selection(gtx, size, a.focus == PaneRevs)
	case a.hovered(tag):
		reef.Fill(gtx, size, ui.P.Hover)
	}
	a.clickable(gtx, size, tag, func() {
		a.focus = PaneRevs
		a.selectReview(r)
	})

	pad := gtx.Dp(reef.PadInline)
	x := pad
	right := size.X - pad
	switch r.kind {
	case reviewWorking:
		label, c := "uncommitted", ui.P.Action
		if !r.rev.WorkingCopy || r.rev.Empty {
			label, c = "HEAD", ui.P.Muted
		}
		x += a.cellText(gtx, x, size.Y, right, reef.WeightLabel, c, "●") + cell.X
		x += a.cellText(gtx, x, size.Y, right, reef.WeightLabel, ui.P.Fg, label) + cell.X
		if r.rev.Description != "" {
			a.cellText(gtx, x, size.Y, right, font.Normal, ui.P.Muted, r.rev.Subject())
		}
	case reviewBranch:
		open := a.opened[r.name]
		mark := "+"
		if open {
			mark = "−"
		}
		if b := a.details[r.name]; b != nil && len(b.Commits) > 0 {
			n := len(b.Commits)
			count := fmt.Sprintf("%d commit", n)
			if n != 1 {
				count += "s"
			}
			right -= a.cellTextRight(gtx, right, size.Y, font.Normal, ui.P.Faint, count) + cell.X
		} else if r.name == a.current {
			right -= a.cellTextRight(gtx, right, size.Y, font.Normal, ui.P.Faint, "current") + cell.X
		}
		x += a.cellText(gtx, x, size.Y, right, reef.WeightLabel, ui.P.Muted, mark) + cell.X
		a.cellText(gtx, x, size.Y, right, reef.WeightLabel, ui.P.Accent, r.name)
	case reviewCommit:
		x += cell.X * 2
		x += a.cellText(gtx, x, size.Y, right, reef.WeightLabel, ui.P.Fg, r.rev.ChangeID) + cell.X
		c := ui.P.Fg
		if r.rev.Description == "" {
			c = ui.P.Muted
		}
		a.cellText(gtx, x, size.Y, right, font.Normal, c, r.rev.Subject())
	}
	reef.HLine(gtx, size.X, size.Y-1, ui.P.RuleFaint)
}
