package ui

import (
	"context"
	"fmt"
	"image"
	"path"
	"strings"

	"gioui.org/font"
	"gioui.org/layout"

	"github.com/chromafish/check/internal/jev"
	"github.com/chromafish/check/reef"
)

// The brief column is the middle of the default view: a bar that takes a
// question for jev, the answer to the last one asked, and under it the
// standing cards. Every answer is a line of the change, and the cursor
// walking them walks the diff with it.

// jevAsk is a question typed into the bar, and what came of it.
type jevAsk struct {
	q       string
	running bool
	err     string
	hits    []briefHit
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
			if len(ask.hits) > 0 {
				a.showHit(ask.hits[0])
			}
		}
	})
}

// dropJevAsk forgets the bar's last question, as a new change is selected.
func (a *App) dropJevAsk() {
	a.jevAskGen++
	a.jevAsk = nil
}

// focusAsk puts the caret in the bar.
func (a *App) focusAsk(gtx layout.Context) {
	if a.askField == nil {
		return
	}
	if a.jev == nil {
		a.note("asking needs a key: %s, or a key in settings", jev.EnvKey)
		return
	}
	a.focus = PaneFiles
	a.askField.Focus(gtx)
}

// briefRowKind is what one line of the column is.
type briefRowKind int

const (
	briefHead     briefRowKind = iota // a card's title
	briefQuestion                     // the question under it
	briefNote                         // a line of prose: pending, nothing found, an error
	briefHitRow                       // one answer
)

type briefLine struct {
	kind  briefRowKind
	text  string
	count string
	hit   briefHit
	index int  // for a hit, its position among all the column's hits
	dim   bool // a card of a change jev reads as mechanical
}

// briefLines is the column as it stands, top to bottom, and the hits in it
// in the same order.
func (a *App) briefLines() ([]briefLine, []briefHit) {
	var lines []briefLine
	var hits []briefHit
	add := func(l briefLine) { lines = append(lines, l) }
	addHits := func(hs []briefHit, dim bool) {
		for _, h := range hs {
			add(briefLine{kind: briefHitRow, hit: h, index: len(hits), dim: dim})
			hits = append(hits, h)
		}
	}

	if a.jev == nil {
		add(briefLine{kind: briefHead, text: "NO KEY"})
		add(briefLine{kind: briefNote, text: "Put a TypeSafe key in settings (,) or " + jev.EnvKey + ","})
		add(briefLine{kind: briefNote, text: "and jev reads every change as it opens."})
		add(briefLine{kind: briefNote, text: "B switches to the classic view."})
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
		addHits(q.hits, false)
	}

	b := a.brief
	switch {
	case b == nil && a.diff == nil:
		add(briefLine{kind: briefNote, text: "waiting for the diff…"})
		return lines, hits
	case b == nil:
		add(briefLine{kind: briefNote, text: "reading the diff…"})
		return lines, hits
	case b.err != "":
		add(briefLine{kind: briefHead, text: "BRIEF"})
		add(briefLine{kind: briefNote, text: b.err})
		return lines, hits
	}

	dim := false
	if b.res != nil && b.res.mechanical >= briefMechanical {
		dim = true
		add(briefLine{kind: briefHead, text: "MECHANICAL", count: fmt.Sprintf("%.0f%%", b.res.mechanical*100)})
		add(briefLine{kind: briefNote, text: "jev reads this as a rename, reformat or generated code."})
	}
	for i, card := range briefCards {
		switch {
		case b.running:
			add(briefLine{kind: briefHead, text: card.title, count: "…", dim: dim})
			add(briefLine{kind: briefQuestion, text: card.ask, dim: dim})
		case b.res != nil:
			hs := b.res.cards[i]
			add(briefLine{kind: briefHead, text: card.title, count: fmt.Sprint(len(hs)), dim: dim || len(hs) == 0})
			add(briefLine{kind: briefQuestion, text: card.ask, dim: dim || len(hs) == 0})
			if len(hs) == 0 {
				add(briefLine{kind: briefNote, text: "nothing found", dim: true})
			}
			addHits(hs, dim)
		}
	}
	return lines, hits
}

// briefTitle heads the column with how far jev has got.
func (a *App) briefTitle() string {
	switch {
	case a.jev == nil:
		return "JEV · OFF"
	case a.brief == nil:
		return "JEV"
	case a.brief.running:
		return "JEV · READING"
	case a.brief.err != "":
		return "JEV · FAILED"
	}
	_, hits := a.briefLines()
	return fmt.Sprintf("JEV · %d LEADS", len(hits))
}

// moveBrief steps the column's cursor from hit to hit, and the diff follows.
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

// enterBrief goes to the hit under the cursor, and into the diff to read it.
func (a *App) enterBrief() {
	_, hits := a.briefLines()
	if a.briefSel < 0 || a.briefSel >= len(hits) {
		return
	}
	a.showHit(hits[a.briefSel])
	a.focus = PaneDiff
}

// briefTag names one hit for the pointer.
type briefTag struct{ index int }

func (a *App) layoutBrief(gtx layout.Context) {
	ui := a.ui
	size := gtx.Constraints.Max
	pad := gtx.Dp(reef.PadInline)

	// The bar is always there: asking is what this column is for.
	barH := 0
	if a.askField != nil {
		fh := ui.FieldHeight(gtx)
		barH = fh + pad*2
		fill(gtx, image.Pt(pad, pad), image.Pt(size.X-pad*2, fh), func(gtx layout.Context) {
			a.askField.Layout(gtx, ui.Theme)
		})
		reef.HLine(gtx, size.X, barH-1, ui.P.Rule)
	}

	lines, _ := a.briefLines()
	row := ui.Row(gtx)
	bodyH := size.Y - barH
	if bodyH <= 0 {
		return
	}
	fill(gtx, image.Pt(0, barH), image.Pt(size.X, bodyH), func(gtx layout.Context) {
		a.briefList.Layout(gtx, len(lines), func(gtx layout.Context, i int) layout.Dimensions {
			gtx.Constraints = layout.Exact(image.Pt(size.X, row))
			a.briefLineView(gtx, lines[i])
			return layout.Dimensions{Size: image.Pt(size.X, row)}
		})
	})
}

func (a *App) briefLineView(gtx layout.Context, l briefLine) {
	ui := a.ui
	size := gtx.Constraints.Max
	pad := gtx.Dp(reef.PadInline)
	cell := ui.Cell(gtx, reef.SizeUI, false)
	right := size.X - pad
	fade := func(c reef.ColorNRGBA) reef.ColorNRGBA {
		if l.dim {
			return ui.P.Faint
		}
		return c
	}

	switch l.kind {
	case briefHead:
		reef.HLine(gtx, size.X, 0, ui.P.RuleFaint)
		if l.count != "" {
			right -= a.cellTextRight(gtx, right, size.Y, reef.WeightLabel, fade(ui.P.Accent), l.count) + cell.X
		}
		a.cellText(gtx, pad, size.Y, right, reef.WeightLabel, fade(ui.P.Strong), l.text)
	case briefQuestion:
		a.cellText(gtx, pad, size.Y, right, font.Normal, fade(ui.P.Muted), l.text)
	case briefNote:
		a.cellText(gtx, pad, size.Y, right, font.Normal, fade(ui.P.Faint), l.text)
	case briefHitRow:
		tag := briefTag{l.index}
		selected := l.index == a.briefSel
		switch {
		case selected:
			ui.Selection(gtx, size, a.focus == PaneFiles)
		case a.hovered(tag):
			reef.Fill(gtx, size, ui.P.Hover)
		}
		a.clickable(gtx, size, tag, func() {
			a.focus = PaneFiles
			a.briefSel = l.index
			a.showHit(l.hit)
		})
		h := l.hit
		pc := ui.P.Muted
		if h.p >= 0.5 {
			pc = ui.P.Action
		}
		x := pad
		x += a.cellText(gtx, x, size.Y, right, reef.WeightLabel, fade(pc), fmt.Sprintf("%2.0f", h.p*100)) + cell.X
		where := fmt.Sprintf("%s:%d", path.Base(h.path), h.num)
		x += a.cellText(gtx, x, size.Y, right, reef.WeightLabel, fade(ui.P.Fg), where) + cell.X
		a.cellText(gtx, x, size.Y, right, font.Normal, fade(a.hitColor(h)), strings.TrimSpace(h.text))
	}
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
