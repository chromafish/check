package ui

import (
	"context"
	"fmt"
	"image"
	"strings"

	"gioui.org/font"
	"gioui.org/layout"

	"github.com/chromafish/check/internal/diffparse"
	"github.com/chromafish/check/internal/plan"
	"github.com/chromafish/check/internal/signals"
	"github.com/chromafish/check/internal/state"
	"github.com/chromafish/check/internal/triage"
	"github.com/chromafish/check/internal/vcs"
	"github.com/chromafish/check/reef"
)

// The plan view is the main screen: for the change under review, what it does
// to the program's telemetry, where it runs unobserved, and — once a model has
// been asked — what to watch when it ships. The telemetry is found as soon as
// a change opens, and costs nothing; the plan is written only when asked for,
// and kept, so the same change is never paid for twice. Anything that names a
// place opens the diff there.

// invState is the inventory of the change on screen, finished or not.
type invState struct {
	key     string
	running bool
	err     string
	inv     *signals.Inventory
}

// planState is the plan of the change on screen: kept, being written, or
// failed.
type planState struct {
	key     string
	running bool
	stage   string
	err     string
	p       *plan.Plan
}

// planKey names what a plan is of: the diff and the commit it ends at.
func (a *App) planKey() string {
	return a.spec.Describe() + "@" + a.rev.CommitIDFull
}

// changedFiles is the diff's files, as the scan and the plan read them.
func (a *App) changedFiles() []*diffparse.File {
	var out []*diffparse.File
	if a.diff == nil {
		return nil
	}
	for _, fd := range a.diff.Files {
		if fd.File != nil {
			out = append(out, fd.File)
		}
	}
	return out
}

// loadDetectors reads the repository's detectors, once per repository.
func (a *App) loadDetectors() []signals.Detector {
	root := a.repo.Root()
	if a.detectors != nil && a.detectorsRoot == root {
		return a.detectors
	}
	ds, err := signals.Load(root)
	a.detectorsRoot = root
	a.detectorsErr = ""
	if err != nil {
		a.detectorsErr = err.Error()
		ds = signals.Builtin()
	}
	a.detectors = ds
	return ds
}

// startInventory takes stock of the change on screen's telemetry. It runs
// whenever a diff finishes loading.
func (a *App) startInventory() {
	if a.repo == nil || a.diff == nil {
		return
	}
	key := a.planKey()
	if a.inv != nil && a.inv.key == key && (a.inv.running || a.inv.inv != nil) {
		return
	}
	ds := a.loadDetectors()
	files := a.changedFiles()
	repo, spec := a.repo, a.spec
	s := &invState{key: key, running: true}
	a.inv = s
	a.planOnScreen()
	a.background(func(ctx context.Context) func() {
		inv, err := signals.Build(ctx, ds, files, func(ctx context.Context, path string, side vcs.Side) ([]byte, error) {
			return repo.FileContent(ctx, spec, path, side)
		})
		return func() {
			s.running = false
			if err != nil {
				s.err = err.Error()
				return
			}
			s.inv = inv
		}
	})
}

// dropPlan forgets the inventory and plan on screen, as a new change is
// selected.
func (a *App) dropPlan() {
	a.inv, a.pl = nil, nil
	a.planSel = 0
	a.planList.Position.First, a.planList.Position.Offset = 0, 0
}

// planOnScreen puts the kept plan of the change on screen in place, if the
// model now named has written one.
func (a *App) planOnScreen() {
	key := a.planKey()
	if a.pl != nil && a.pl.running {
		return
	}
	a.pl = nil
	if a.model == nil {
		return
	}
	dir, err := state.PlansDir()
	if err != nil {
		return
	}
	if p, ok := plan.Load(dir, key, a.model.Model()); ok {
		a.pl = &planState{key: key, p: p}
	}
}

// writePlan asks the model for a plan of the change on screen: jev first, if
// there is a key, to leave out what does not run in production.
func (a *App) writePlan() {
	switch {
	case a.model == nil:
		a.note("no model: name one in settings (,)")
		return
	case a.inv == nil || a.inv.running:
		a.note("still reading the change")
		return
	case a.pl != nil && a.pl.running:
		return
	}
	key := a.planKey()
	in := plan.Input{Commit: key, Message: a.desc, Files: a.changedFiles(), Inv: a.inv.inv}
	c, j := a.model, a.jev
	s := &planState{key: key, running: true, stage: "writing with " + c.Model() + "…"}
	if j != nil {
		s.stage = "triaging with jev…"
	}
	a.pl = s
	a.backgroundIn(context.Background(), 0, func(ctx context.Context) func() {
		if j != nil {
			v, err := triage.Run(ctx, j, plan.Hunks(in.Files))
			if err == nil {
				in.Verdicts = v
			}
			a.after(func() { s.stage = "writing with " + c.Model() + "…" })
		}
		p, err := plan.Write(ctx, c, in)
		if err == nil {
			if dir, derr := state.PlansDir(); derr == nil {
				plan.Save(dir, p)
			}
		}
		return func() {
			s.running = false
			if err != nil {
				s.err = err.Error()
				a.fail(err)
				return
			}
			s.p = p
			a.note("plan written by %s", p.Model)
		}
	})
}

// copyPlan puts the plan, as Markdown, on the clipboard.
func (a *App) copyPlan() {
	if a.pl == nil || a.pl.p == nil {
		a.note("no plan to copy: M writes one")
		return
	}
	var inv *signals.Inventory
	if a.inv != nil {
		inv = a.inv.inv
	}
	a.clipboard = plan.Markdown(a.pl.p, inv)
	a.note("plan copied as Markdown")
}

// place is somewhere in the change a line of the plan points at.
type place struct {
	path string
	line int
	old  bool
}

// drill opens the diff at a place. The diff view is a drill-down: B or Esc
// comes back to the plan.
func (a *App) drill(p place) {
	doc := a.diff
	if doc == nil {
		return
	}
	fi, ok := doc.index[p.path]
	if !ok {
		a.note("%s is not in the diff", p.path)
		return
	}
	if !a.drilled {
		a.drilled = true
		a.swapFractions()
	}
	a.fileSel = fi
	a.lightFile(fi)
	row := doc.FileRows[fi]
	start, end := doc.rowRange(fi)
	for i := start; i < end; i++ {
		r := doc.rowPtr(i)
		if r.Kind != rowLine {
			continue
		}
		n := r.Line.NewNum
		if p.old {
			n = r.Line.OldNum
		}
		if n >= p.line {
			row = i
			break
		}
	}
	doc.Cursor = row
	a.diffList.Position.First = max(row-3, 0)
	a.diffList.Position.Offset = 0
	a.pinFile = fi
	a.focus = PaneDiff
}

// undrill comes back from the diff to the plan.
func (a *App) undrill() bool {
	if !a.drilled {
		return false
	}
	a.drilled = false
	a.swapFractions()
	a.focus = PaneDiff
	return true
}

// planRowKind is what one row of the plan view is.
type planRowKind int

const (
	planTitle planRowKind = iota
	planHead
	planText
	planItem
	planDetail
	planCode
	planNote
)

type planRow struct {
	kind  planRowKind
	text  string
	right string
	color reef.ColorNRGBA
	at    *place
	item  int // for an item, its index among the view's items
}

// planRows is the view as it stands, for a width of cols characters.
func (a *App) planRows(cols int) []planRow {
	ui := a.ui
	var rows []planRow
	items := 0
	add := func(r planRow) { rows = append(rows, r) }
	head := func(t, right string) { add(planRow{kind: planHead, text: t, right: right}) }
	wrap := func(kind planRowKind, t string, c reef.ColorNRGBA) {
		for _, l := range reef.Wrap(t, max(cols-4, 20)) {
			add(planRow{kind: kind, text: l, color: c})
		}
	}
	item := func(t, right string, c reef.ColorNRGBA, at *place) {
		add(planRow{kind: planItem, text: t, right: right, color: c, at: at, item: items})
		items++
	}

	if a.diff == nil {
		add(planRow{kind: planNote, text: "waiting for the diff…"})
		return rows
	}
	if s := a.rev.Subject(); s != "" {
		wrap(planTitle, s, ui.P.Strong)
	}

	// The plan, once there is one.
	switch pl := a.pl; {
	case a.model == nil:
		head("PLAN", "")
		wrap(planNote, "No model is named, so no plan is written. Name any OpenAI-compatible model in settings (,), or set CHECK_MODEL. The telemetry below is found without one.", ui.P.Faint)
	case pl != nil && pl.running:
		head("PLAN", "")
		add(planRow{kind: planNote, text: pl.stage})
	case pl != nil && pl.err != "":
		head("PLAN", "")
		wrap(planNote, pl.err, ui.P.Error)
		add(planRow{kind: planNote, text: "M to try again"})
	case pl != nil && pl.p != nil:
		p := pl.p
		risk, rc := strings.ToUpper(p.Rollout.Risk), ui.P.Muted
		switch p.Rollout.Risk {
		case "high":
			rc = ui.P.Error
		case "medium":
			rc = ui.P.Warn
		case "low":
			rc = ui.P.AddFg
		}
		head("PLAN", "")
		if p.Summary != "" {
			wrap(planText, p.Summary, ui.P.Fg)
		}
		if risk != "" {
			add(planRow{kind: planText, text: "ROLLOUT RISK " + risk, color: rc})
		}
		if p.Rollout.Advice != "" {
			wrap(planDetail, p.Rollout.Advice, ui.P.Muted)
		}
		if len(p.Watch) > 0 {
			head("WATCH", fmt.Sprint(len(p.Watch)))
			for _, w := range p.Watch {
				s := w.Signal
				item(fmt.Sprintf("%s %s — expect %s", s.Kind, nameOr(s.Signal), w.Expect),
					fmt.Sprintf("%s:%d", baseOf(s.Path), s.Line), a.signalColor(s.Status),
					&place{s.Path, s.Line, s.Status == signals.Removed})
				wrap(planDetail, w.Why, ui.P.Muted)
			}
		}
		if len(p.Gaps) > 0 {
			head("GAPS", fmt.Sprint(len(p.Gaps)))
			for _, g := range p.Gaps {
				item(g.Unseen, fmt.Sprintf("%s:%d", baseOf(g.Path), g.Line), ui.P.Fg, &place{g.Path, g.Line, g.Old})
				wrap(planDetail, fmt.Sprintf("add a %s %q", g.Kind, g.Name), ui.P.Action)
				if g.Snippet != "" {
					add(planRow{kind: planCode, text: g.Snippet})
				}
			}
		}
		if len(p.Regressions) > 0 {
			head("REGRESSIONS", fmt.Sprint(len(p.Regressions)))
			for _, r := range p.Regressions {
				var at *place
				right := "proposed"
				if r.Found != nil {
					at = &place{r.Found.Path, r.Found.Line, r.Found.Status == signals.Removed}
					right = fmt.Sprintf("%s:%d", baseOf(r.Found.Path), r.Found.Line)
				}
				item(r.Signal, right, ui.P.Fg, at)
				wrap(planDetail, r.Condition, ui.P.Muted)
			}
		}
		foot := fmt.Sprintf("written by %s · %d in, %d out tokens", p.Model, p.Usage.Prompt, p.Usage.Completion)
		if p.Omitted > 0 {
			foot += fmt.Sprintf(" · %d hunks left out", p.Omitted)
		}
		if p.Dropped > 0 {
			foot += fmt.Sprintf(" · %d unverifiable claims dropped", p.Dropped)
		}
		wrap(planNote, foot+" · M rewrites · Y copies", ui.P.Faint)
	default:
		head("PLAN", "")
		wrap(planNote, "M writes a plan for this change with "+a.model.Model()+".", ui.P.Faint)
	}

	// The telemetry, found without asking anyone.
	s := a.inv
	switch {
	case s == nil || s.running:
		head("TELEMETRY", "")
		add(planRow{kind: planNote, text: "reading the changed files…"})
		return rows
	case s.err != "":
		head("TELEMETRY", "")
		wrap(planNote, s.err, ui.P.Error)
		return rows
	}
	inv := s.inv
	head("TELEMETRY", fmt.Sprintf("%d added · %d removed · %d altered · %d nearby",
		inv.Count(signals.Added), inv.Count(signals.Removed), inv.Count(signals.Altered), inv.Count(signals.Nearby)))
	if a.detectorsErr != "" {
		wrap(planNote, a.detectorsErr, ui.P.Error)
	}
	if len(inv.Signals) == 0 {
		note := "no telemetry in or near this change"
		if inv.Files == 0 {
			note = "no changed file is one the detectors read"
		}
		add(planRow{kind: planNote, text: note})
	}
	for _, f := range inv.Signals {
		item(fmt.Sprintf("%-8s %-6s %s", f.Status, f.Kind, nameOr(f.Signal)),
			fmt.Sprintf("%s:%d", baseOf(f.Path), f.Line), a.signalColor(f.Status),
			&place{f.Path, f.Line, f.Status == signals.Removed})
	}
	if len(inv.Dark) > 0 {
		head("UNOBSERVED", fmt.Sprint(len(inv.Dark)))
		for _, d := range inv.Dark {
			what := d.Text
			if what == "" {
				what = fmt.Sprintf("%d lines changed", d.Changed)
			}
			if d.Old {
				what = "removed · " + what
			}
			item(what, fmt.Sprintf("%s:%d", baseOf(d.Path), d.Line), ui.P.Fg, &place{d.Path, d.Line, d.Old})
		}
	}
	return rows
}

func (a *App) signalColor(s signals.Status) reef.ColorNRGBA {
	switch s {
	case signals.Added:
		return a.ui.P.AddFg
	case signals.Removed:
		return a.ui.P.DelFg
	case signals.Altered:
		return a.ui.P.Warn
	}
	return a.ui.P.Muted
}

func nameOr(s signals.Signal) string {
	if s.Name != "" {
		return s.Name
	}
	return "(" + s.Detector + ")"
}

func baseOf(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[i+1:]
	}
	return p
}

// planItems is how many selectable items the view has.
func (a *App) planItems() int {
	n := 0
	for _, r := range a.planRows(a.planCols) {
		if r.kind == planItem {
			n++
		}
	}
	return n
}

// movePlan steps the view's cursor from item to item.
func (a *App) movePlan(where func(at, n int) int) {
	rows := a.planRows(a.planCols)
	n := a.planItems()
	if n == 0 {
		return
	}
	a.planSel = clamp(where(a.planSel, n), 0, n-1)
	for i, r := range rows {
		if r.kind == planItem && r.item == a.planSel {
			a.scrollList(&a.planList, i)
			return
		}
	}
}

// enterPlan opens the diff at the item under the cursor.
func (a *App) enterPlan() {
	for _, r := range a.planRows(a.planCols) {
		if r.kind == planItem && r.item == a.planSel && r.at != nil {
			a.drill(*r.at)
			return
		}
	}
}

// planTitle names the right pane.
func (a *App) planTitle() string {
	if a.pl != nil && a.pl.p != nil {
		return "PLAN · " + strings.ToUpper(a.pl.p.Model)
	}
	return "PLAN"
}

type planTag struct{ item int }

const (
	tagWrite    tag = "write-plan"
	tagCopyPlan tag = "copy-plan"
)

// planControls are the view's header controls.
func (a *App) planControls(gtx layout.Context) {
	ui := a.ui
	size := gtx.Constraints.Max
	rightX := size.X - gtx.Dp(reef.PadInline)
	if a.pl != nil && a.pl.p != nil {
		rightX -= a.controlRight(gtx, rightX, size.Y, tagCopyPlan, "COPY  Y", ui.P.Action, a.copyPlan) + gtx.Dp(reef.Sp3)
	}
	label, c := "WRITE  M", ui.P.Action
	switch {
	case a.model == nil:
		c = ui.P.Faint
	case a.pl != nil && a.pl.running:
		label, c = "WRITING…", ui.P.Muted
	case a.pl != nil && a.pl.p != nil:
		label = "REWRITE  M"
	}
	a.controlRight(gtx, rightX, size.Y, tagWrite, label, c, a.writePlan)
}

func (a *App) layoutPlan(gtx layout.Context) {
	ui := a.ui
	size := gtx.Constraints.Max
	row := ui.Row(gtx)
	cell := ui.Cell(gtx, reef.SizeUI, false)
	pad := gtx.Dp(reef.PadInline)
	a.planCols = max((size.X-pad*2)/max(cell.X, 1), 20)
	rows := a.planRows(a.planCols)
	a.planList.Layout(gtx, len(rows), func(gtx layout.Context, i int) layout.Dimensions {
		gtx.Constraints = layout.Exact(image.Pt(size.X, row))
		a.planRowView(gtx, rows[i], cell)
		return layout.Dimensions{Size: image.Pt(size.X, row)}
	})
}

func (a *App) planRowView(gtx layout.Context, r planRow, cell image.Point) {
	ui := a.ui
	size := gtx.Constraints.Max
	pad := gtx.Dp(reef.PadInline)
	right := size.X - pad
	switch r.kind {
	case planTitle:
		a.cellText(gtx, pad, size.Y, right, reef.WeightLabel, r.color, r.text)
	case planHead:
		reef.HLine(gtx, size.X, 0, ui.P.RuleFaint)
		if r.right != "" {
			right -= a.cellTextRight(gtx, right, size.Y, font.Normal, ui.P.Faint, r.right) + cell.X
		}
		a.cellText(gtx, pad, size.Y, right, reef.WeightLabel, ui.P.Muted, r.text)
	case planText:
		a.cellText(gtx, pad, size.Y, right, font.Normal, r.color, r.text)
	case planDetail:
		a.cellText(gtx, pad+cell.X*2, size.Y, right, font.Normal, r.color, r.text)
	case planCode:
		a.codeText(gtx, pad+cell.X*2, size.Y, right, font.Normal, ui.P.Syntax[reef.SyntaxPlain], r.text)
	case planNote:
		c := r.color
		if c.A == 0 {
			c = ui.P.Faint
		}
		a.cellText(gtx, pad, size.Y, right, font.Normal, c, r.text)
	case planItem:
		tag := planTag{r.item}
		switch {
		case r.item == a.planSel:
			ui.Selection(gtx, size, a.focus == PaneDiff)
		case a.hovered(tag):
			reef.Fill(gtx, size, ui.P.Hover)
		}
		a.clickable(gtx, size, tag, func() {
			a.focus = PaneDiff
			a.planSel = r.item
			if r.at != nil {
				a.drill(*r.at)
			}
		})
		if r.right != "" {
			right -= a.cellTextRight(gtx, right, size.Y, font.Normal, ui.P.Faint, r.right) + cell.X
		}
		a.cellText(gtx, pad, size.Y, right, font.Normal, r.color, r.text)
	}
}
