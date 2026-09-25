package ui

import (
	"context"
	"fmt"
	"image"
	"path"
	"strings"

	"gioui.org/font"
	"gioui.org/io/key"
	"gioui.org/layout"

	"github.com/chromafish/check/internal/jev"
	"github.com/chromafish/check/reef"
)

// jev's sheet floats over the review, the way help and settings do: what kind
// of change this is, what it touches, and the lines worth reading first, each
// under the question it answers. It is out of the way until asked for with A;
// the diff's gutter marks the same lines meanwhile, and the header says how
// many there are.

// jevAsk is a question typed into the sheet's bar, and what came of it.
type jevAsk struct {
	q       string
	running bool
	err     string
	hits    []briefHit
}

// toggleJev opens or closes the sheet.
func (a *App) toggleJev() {
	a.jevOpen = !a.jevOpen
	if a.jevOpen {
		a.help, a.notesOpen, a.settingsOpen = false, false, false
		a.briefSel = 0
	}
}

// jevControl is the diff header's control for the sheet: its label, and the
// colour that says whether there is anything in it.
func (a *App) jevControl() (string, reef.ColorNRGBA) {
	ui := a.ui
	switch {
	case a.jev == nil:
		return "JEV OFF", ui.P.Faint
	case a.brief == nil || a.brief.running:
		return "JEV …", ui.P.Muted
	case a.brief.err != "":
		return "JEV !", ui.P.Error
	}
	_, hits := a.briefLines()
	if len(hits) == 0 {
		return "JEV", ui.P.Muted
	}
	return fmt.Sprintf("JEV %d", len(hits)), ui.P.Warn
}

// askJev asks the bar's question of the change on screen.
func (a *App) askJev(q string) {
	c := a.jev
	if c == nil {
		a.note("asking needs a key: %s, or a key in settings", jev.EnvKey)
		return
	}
	doc := a.diff
	hunks := collectSem(doc)
	if len(hunks) == 0 {
		a.note("nothing to ask about")
		return
	}
	a.jevAskGen++
	gen := a.jevAskGen
	ask := &jevAsk{q: q, running: true}
	a.jevAsk = ask
	a.briefSel = 0
	a.backgroundIn(context.Background(), semLimit, func(ctx context.Context) func() {
		cands, err := semSearch(ctx, c, q, hunks)
		return func() {
			if gen != a.jevAskGen || a.diff != doc {
				return
			}
			ask.running = false
			if err != nil {
				ask.err = briefError(err)
				return
			}
			for _, c := range cands {
				ask.hits = append(ask.hits, briefFill([]semScore{{c.id, c.p}}, map[string]semCand{c.id: c})...)
			}
			a.briefSel = 0
		}
	})
}

// dropJevAsk forgets the bar's last question, as a new change is selected.
func (a *App) dropJevAsk() {
	a.jevAskGen++
	a.jevAsk = nil
}

// focusAsk opens the sheet with the caret in its bar.
func (a *App) focusAsk(gtx layout.Context) {
	if a.askField == nil {
		return
	}
	if a.jev == nil {
		a.note("asking needs a key: %s, or a key in settings", jev.EnvKey)
		return
	}
	if !a.jevOpen {
		a.toggleJev()
	}
	a.askField.Focus(gtx)
}

// jevKey is the keyboard while the sheet is up. Like the settings sheet it is
// modal, so a key meant for it never also moves the review underneath.
func (a *App) jevKey(gtx layout.Context, ke key.Event) {
	if a.askField != nil && a.askField.Focused() {
		if ke.Name == key.NameEscape {
			a.askField.Defocus(gtx)
			gtx.Execute(key.FocusCmd{Tag: nil})
		}
		return
	}
	switch ke.Name {
	case key.NameEscape, "A":
		a.jevOpen = false
	case key.NameUpArrow, "K":
		a.moveBrief(func(at, _ int) int { return at - 1 })
	case key.NameDownArrow, "J":
		a.moveBrief(func(at, _ int) int { return at + 1 })
	case key.NameReturn, key.NameEnter:
		a.enterBrief()
	case "/", key.NameTab:
		a.focusAsk(gtx)
	}
}

// briefRowKind is what one line of the sheet is.
type briefRowKind int

const (
	briefHead     briefRowKind = iota // a question's title
	briefQuestion                     // the question itself
	briefNote                         // a line of prose: pending, nothing found, an error
	briefHitRow                       // one line of the change
)

type briefLine struct {
	kind  briefRowKind
	text  string
	count string
	hit   briefHit
	index int // for a hit, its position among all the sheet's hits
}

// briefLines is the sheet as it stands, top to bottom, and the hits in it in
// the same order.
func (a *App) briefLines() ([]briefLine, []briefHit) {
	var lines []briefLine
	var hits []briefHit
	add := func(l briefLine) { lines = append(lines, l) }
	addHit := func(h briefHit) {
		add(briefLine{kind: briefHitRow, hit: h, index: len(hits)})
		hits = append(hits, h)
	}

	if a.jev == nil {
		add(briefLine{kind: briefNote, text: "jev is off. Put a TypeSafe key in settings (,) or " + jev.EnvKey + ","})
		add(briefLine{kind: briefNote, text: "and every change is read as it opens."})
		return lines, hits
	}

	if q := a.jevAsk; q != nil {
		count := ""
		if !q.running && q.err == "" {
			count = fmt.Sprint(len(q.hits))
		}
		add(briefLine{kind: briefHead, text: "ASKED", count: count})
		add(briefLine{kind: briefQuestion, text: q.q})
		switch {
		case q.running:
			add(briefLine{kind: briefNote, text: "asking…"})
		case q.err != "":
			add(briefLine{kind: briefNote, text: q.err})
		case len(q.hits) == 0:
			add(briefLine{kind: briefNote, text: "nothing in this change"})
		}
		for _, h := range q.hits {
			addHit(h)
		}
	}

	b := a.brief
	switch {
	case b == nil:
		add(briefLine{kind: briefNote, text: "waiting for the diff…"})
		return lines, hits
	case b.running:
		add(briefLine{kind: briefNote, text: "reading the change…"})
		return lines, hits
	case b.err != "":
		add(briefLine{kind: briefNote, text: b.err})
		return lines, hits
	case b.res == nil:
		return lines, hits
	}

	res := b.res
	if quietKinds[res.kind.id] && len(res.asked) == 0 {
		add(briefLine{kind: briefNote, text: fmt.Sprintf("A %s change: nothing in it for review questions to point at.",
			strings.ToLower(res.kind.label))})
		return lines, hits
	}
	groups, empty := res.findings()
	for i, g := range groups {
		if len(g) == 0 {
			continue
		}
		add(briefLine{kind: briefHead, text: res.asked[i].title, count: fmt.Sprint(len(g))})
		add(briefLine{kind: briefQuestion, text: res.asked[i].ask})
		for _, f := range g {
			addHit(f.hit)
		}
	}
	if len(hits) == 0 && len(res.asked) > 0 {
		add(briefLine{kind: briefNote, text: "jev found nothing worth pointing at."})
	}
	if len(empty) > 0 {
		titles := make([]string, len(empty))
		for i, c := range empty {
			titles[i] = strings.ToLower(c.title)
		}
		add(briefLine{kind: briefNote, text: "also asked, nothing found: " + strings.Join(titles, " · ")})
	}
	return lines, hits
}

// briefSummary is the sheet's heading: the kind of change, its languages, and
// what it touches.
func (a *App) briefSummary() (kind, touches string) {
	if a.brief == nil || a.brief.res == nil || a.brief.res.kind.id == "" {
		return "", ""
	}
	res := a.brief.res
	kind = res.kind.label
	if len(res.languages) > 0 {
		kind += " · " + strings.Join(res.languages, ", ")
	}
	if len(res.topics) > 0 {
		labels := make([]string, len(res.topics))
		for i, t := range res.topics {
			labels[i] = t.label
		}
		touches = "touches " + strings.Join(labels, " · ")
	}
	return kind, touches
}

// moveBrief steps the sheet's cursor from hit to hit, and the diff follows.
func (a *App) moveBrief(where func(at, n int) int) {
	lines, hits := a.briefLines()
	if len(hits) == 0 {
		return
	}
	a.briefSel = clamp(where(clamp(a.briefSel, 0, len(hits)-1), len(hits)), 0, len(hits)-1)
	a.showHit(hits[a.briefSel])
	for i, l := range lines {
		if l.kind == briefHitRow && l.index == a.briefSel {
			a.scrollList(&a.briefList, i)
			break
		}
	}
}

// showHit puts the diff's cursor on a hit's line.
func (a *App) showHit(h briefHit) {
	if a.diff == nil {
		return
	}
	row := a.diff.briefRow(h)
	if row < 0 {
		a.note("that line is no longer in the diff")
		return
	}
	a.diff.Cursor = row
	a.scrollTo(row)
}

// enterBrief goes to the hit under the cursor: the sheet is put away and the
// diff is left on the line, to be read.
func (a *App) enterBrief() {
	_, hits := a.briefLines()
	if a.briefSel < 0 || a.briefSel >= len(hits) {
		return
	}
	a.showHit(hits[a.briefSel])
	a.jevOpen = false
	a.focus = PaneDiff
}

// briefTag names one hit for the pointer.
type briefTag struct{ index int }

// layoutJev draws the sheet.
func (a *App) layoutJev(gtx layout.Context) {
	if !a.jevOpen || a.repo == nil {
		return
	}
	ui := a.ui
	size := gtx.Constraints.Max
	row := ui.Row(gtx)
	pad := gtx.Dp(reef.PadCard)
	cell := ui.Cell(gtx, reef.SizeUI, false)
	fieldH := ui.FieldHeight(gtx)

	lines, _ := a.briefLines()
	w := min(size.X-gtx.Dp(80), 110*cell.X+pad*2)
	head := row*3 + fieldH + pad
	h := min(size.Y-gtx.Dp(80), head+row*len(lines)+pad*2)
	x, y := (size.X-w)/2, max(gtx.Dp(24), (size.Y-h)/3)
	ui.Sheet(gtx, image.Rect(x, y, x+w, y+h))

	ly := y + pad
	kind, touches := a.briefSummary()
	fit(gtx, image.Pt(x+pad, ly), image.Pt(w-pad*2, row), func(gtx layout.Context) {
		title := "JEV"
		if kind != "" {
			title += " · " + kind
		}
		hint := a.cellTextRight(gtx, w-pad*2, row, font.Normal, ui.P.Faint, "j k move · enter read · / ask · esc close")
		a.cellText(gtx, 0, row, w-pad*2-hint-cell.X, reef.WeightLabel, ui.P.Strong, title)
	})
	ly += row
	fit(gtx, image.Pt(x+pad, ly), image.Pt(w-pad*2, row), func(gtx layout.Context) {
		a.cellText(gtx, 0, row, w-pad*2, font.Normal, ui.P.Muted, touches)
	})
	ly += row
	if a.askField != nil {
		fill(gtx, image.Pt(x+pad, ly), image.Pt(w-pad*2, fieldH), func(gtx layout.Context) {
			a.askField.Layout(gtx, ui.Theme)
		})
	}
	ly += fieldH + pad/2

	bodyH := y + h - pad - ly
	if bodyH <= 0 {
		return
	}
	fill(gtx, image.Pt(x+pad/2, ly), image.Pt(w-pad, bodyH), func(gtx layout.Context) {
		a.briefList.Layout(gtx, len(lines), func(gtx layout.Context, i int) layout.Dimensions {
			gtx.Constraints = layout.Exact(image.Pt(w-pad, row))
			a.briefLineView(gtx, lines[i])
			return layout.Dimensions{Size: image.Pt(w-pad, row)}
		})
	})
}

func (a *App) briefLineView(gtx layout.Context, l briefLine) {
	ui := a.ui
	size := gtx.Constraints.Max
	pad := gtx.Dp(reef.PadInline)
	cell := ui.Cell(gtx, reef.SizeUI, false)
	right := size.X - pad

	switch l.kind {
	case briefHead:
		reef.HLine(gtx, size.X, 0, ui.P.RuleFaint)
		if l.count != "" {
			right -= a.cellTextRight(gtx, right, size.Y, reef.WeightLabel, ui.P.Accent, l.count) + cell.X
		}
		a.cellText(gtx, pad, size.Y, right, reef.WeightLabel, ui.P.Strong, l.text)
	case briefQuestion:
		a.cellText(gtx, pad, size.Y, right, font.Normal, ui.P.Muted, l.text)
	case briefNote:
		a.cellText(gtx, pad, size.Y, right, font.Normal, ui.P.Faint, l.text)
	case briefHitRow:
		tag := briefTag{l.index}
		switch {
		case l.index == a.briefSel:
			ui.Selection(gtx, size, true)
		case a.hovered(tag):
			reef.Fill(gtx, size, ui.P.Hover)
		}
		a.clickable(gtx, size, tag, func() {
			a.briefSel = l.index
			a.enterBrief()
		})
		h := l.hit
		x := pad
		a.jevMark(gtx, x, size.Y)
		x += cell.X * 2
		where := fmt.Sprintf("%s:%d", path.Base(h.path), h.num)
		x += a.cellText(gtx, x, size.Y, right, reef.WeightLabel, ui.P.Fg, where) + cell.X
		a.cellText(gtx, x, size.Y, right, font.Normal, a.hitColor(h), strings.TrimSpace(h.text))
	}
}

// jevMark is jev's mark on a line: a small square in the warning colour,
// centred in a row of height h at x. It is drawn rather than set as a glyph,
// so it is the same size in the code's face as in the interface's.
func (a *App) jevMark(gtx layout.Context, x, h int) {
	s := gtx.Dp(6)
	y := (h - s) / 2
	reef.FillRect(gtx, image.Rect(x, y, x+s, y+s), a.ui.P.Warn)
}

// hitColor sets a hit's line in the colour the diff sets it in.
func (a *App) hitColor(h briefHit) reef.ColorNRGBA {
	if h.text == "" {
		return a.ui.P.Muted
	}
	switch h.text[0] {
	case '+':
		return a.ui.P.AddFg
	case '-':
		return a.ui.P.DelFg
	}
	return a.ui.P.Muted
}
