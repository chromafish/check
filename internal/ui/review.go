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
// as a person names it. The working copy comes first, then every branch.
// The branch being reviewed opens to list its own commits, so the same
// column goes from the branch as a whole to one commit of it and back.

// reviewKind is what one row of the column stands for.
type reviewKind int

const (
	reviewWorking reviewKind = iota // the working copy
	reviewBranch                    // a branch, reviewed whole
	reviewCommit                    // one commit of the open branch
)

// reviewRow is one row of the column. key names it across reloads, which
// renumber the rows but not what they are.
type reviewRow struct {
	kind reviewKind
	name string // the branch
	rev  vcs.Revision
	key  string
}

// classic reports whether the three-column revision view is the one on screen.
func (a *App) classic() bool { return a.settings.Classic }

// toggleClassic moves between the brief and the classic view, and records the
// choice. Whatever is under review stays under review.
func (a *App) toggleClassic() {
	a.settings.Classic = !a.settings.Classic
	a.saveSettings()
	a.swapFractions()
	if a.classic() {
		a.note("classic view")
		return
	}
	a.note("brief view")
	if a.focus == PaneFiles && a.brief == nil {
		a.focus = PaneRevs
	}
}

// The two views divide the window differently: the brief's middle column
// holds questions and answers, which want room, where the manifest holds
// paths. Each view keeps its own split, dragged or not, across a switch.
var (
	classicFractions = [2]float32{0.22, 0.23}
	briefFractions   = [2]float32{0.18, 0.34}
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
	a.splits.SetFraction(1, next[1])
}

// workingRev is the revision the column's first row stands for: the working
// copy, or where there is none on screen the newest revision.
func (a *App) workingRev() (vcs.Revision, bool) {
	for _, r := range a.revs {
		if r.WorkingCopy {
			return r, true
		}
	}
	if len(a.revs) > 0 {
		return a.revs[0], true
	}
	return vcs.Revision{}, false
}

// reviewRows is the column as it stands.
func (a *App) reviewRows() []reviewRow {
	var rows []reviewRow
	if w, ok := a.workingRev(); ok {
		rows = append(rows, reviewRow{kind: reviewWorking, rev: w, key: "@"})
	}
	for _, n := range a.branches {
		rows = append(rows, reviewRow{kind: reviewBranch, name: n, key: "b:" + n})
		if a.branch != nil && a.branch.Name == n {
			for _, c := range a.branch.Commits {
				rows = append(rows, reviewRow{kind: reviewCommit, name: n, rev: c, key: "c:" + c.CommitIDFull})
			}
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

// selectReview puts one row of the column under review.
func (a *App) selectReview(r reviewRow) {
	a.reviewKey = r.key
	switch r.kind {
	case reviewWorking:
		a.branch = nil
		a.branchGen++
		a.review(r.rev, vcs.DiffSpec{Kind: vcs.DiffChange, Rev: r.rev.ChangeID})
	case reviewCommit:
		a.review(r.rev, vcs.DiffSpec{Kind: vcs.DiffChange, Rev: r.rev.ChangeID})
	case reviewBranch:
		a.openBranch(r.name, "")
	}
}

// openBranch reads a branch and reviews it: the whole of it, or the commit
// named by commit when that is still one of its own.
func (a *App) openBranch(name, commit string) {
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
			a.branch = &b
			for _, c := range b.Commits {
				if commit != "" && c.CommitIDFull == commit {
					a.reviewKey = "c:" + commit
					a.review(c, vcs.DiffSpec{Kind: vcs.DiffChange, Rev: c.ChangeID})
					return
				}
			}
			a.reviewKey = "b:" + name
			a.reviewBranch(b)
		}
	})
}

// reviewBranch reviews a branch whole. A branch with no commits of its own —
// the trunk, or one that has been merged — is reviewed as its tip, which is
// at least something to read rather than an empty range.
func (a *App) reviewBranch(b vcs.Branch) {
	if len(b.Commits) == 0 {
		a.review(b.Tip, vcs.DiffSpec{Kind: vcs.DiffChange, Rev: b.Tip.ChangeID})
		return
	}
	// Notes and read marks are filed under the branch rather than any one
	// commit of it, and a read mark goes stale when the tip moves.
	rev := b.Tip
	rev.ChangeID = b.Name
	rev.ChangeIDFull = "branch:" + b.Name
	rev.WorkingCopy = false
	a.review(rev, b.Spec())
}

// restoreReview puts back what was under review after the column's lists
// were read again, or the working copy when that is gone.
func (a *App) restoreReview() {
	switch {
	case strings.HasPrefix(a.reviewKey, "b:"):
		name := strings.TrimPrefix(a.reviewKey, "b:")
		for _, n := range a.branches {
			if n == name {
				a.openBranch(name, "")
				return
			}
		}
	case strings.HasPrefix(a.reviewKey, "c:") && a.branch != nil:
		for _, n := range a.branches {
			if n == a.branch.Name {
				a.openBranch(n, strings.TrimPrefix(a.reviewKey, "c:"))
				return
			}
		}
	}
	a.branch = nil
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
		x += a.cellText(gtx, x, size.Y, right, reef.WeightLabel, ui.P.Action, "@") + cell.X
		x += a.cellText(gtx, x, size.Y, right, reef.WeightLabel, ui.P.Fg, r.rev.ChangeID) + cell.X
		a.cellText(gtx, x, size.Y, right, font.Normal, ui.P.Muted, r.rev.Subject())
	case reviewBranch:
		open := a.branch != nil && a.branch.Name == r.name
		mark := "+"
		if open {
			mark = "−"
		}
		if open && len(a.branch.Commits) > 0 {
			n := len(a.branch.Commits)
			count := fmt.Sprintf("%d commit", n)
			if n != 1 {
				count += "s"
			}
			right -= a.cellTextRight(gtx, right, size.Y, font.Normal, ui.P.Faint, count) + cell.X
		}
		x += a.cellText(gtx, x, size.Y, right, reef.WeightLabel, ui.P.Muted, mark) + cell.X
		a.cellText(gtx, x, size.Y, right, reef.WeightLabel, ui.P.Accent, r.name)
	case reviewCommit:
		x += cell.X * 2
		x += a.cellText(gtx, x, size.Y, right, reef.WeightLabel, ui.P.Muted, "●") + cell.X
		x += a.cellText(gtx, x, size.Y, right, reef.WeightLabel, ui.P.Fg, r.rev.ChangeID) + cell.X
		c := ui.P.Fg
		if r.rev.Description == "" {
			c = ui.P.Muted
		}
		a.cellText(gtx, x, size.Y, right, font.Normal, c, r.rev.Subject())
	}
	reef.HLine(gtx, size.X, size.Y-1, ui.P.RuleFaint)
}
