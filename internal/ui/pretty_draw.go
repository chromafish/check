package ui

import (
	"fmt"
	"image"
	"strings"
	"unicode"

	"gioui.org/font"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/unit"

	"github.com/chromafish/check/internal/diffparse"
	"github.com/chromafish/check/internal/highlight"
	"github.com/chromafish/check/reef"
)

// prettyRune is one character and the Markdown inline style that owns it.
// Pretty text is deliberately laid out on reef's character grid: it keeps
// measurement and drawing in agreement while still letting emphasis, links,
// and inline code survive a wrap.
type prettyRune struct {
	r     rune
	style Inline
}

type prettyTableLayout struct {
	widths  []int
	heights []int
	total   int
}

// prettyGutterWidth is the left gutter for rendered blocks (no line numbers).
func (a *App) prettyGutterWidth(gtx layout.Context) int {
	cell := a.ui.Cell(gtx, reef.SizeCode, false)
	return cell.X*3/2 + gtx.Dp(reef.PadInline)
}

// prettyContentBounds lays a rendered block out to the diff pane's current
// width. The document stays aligned to its change gutter instead of floating
// as a disconnected page in the pane.
func (a *App) prettyContentBounds(gtx layout.Context, width int) (int, int) {
	x := a.prettyGutterWidth(gtx) + gtx.Dp(reef.PadCard)
	available := max(1, width-x-gtx.Dp(reef.PadCard))
	return x, available
}

func prettyBlockPadding(gtx layout.Context, b *PrettyBlock) (int, int) {
	switch b.Kind {
	case BlockHeading:
		switch b.Level {
		case 1:
			return gtx.Dp(reef.Sp7), gtx.Dp(reef.Sp4)
		case 2:
			return gtx.Dp(reef.Sp6), gtx.Dp(reef.Sp3)
		default:
			return gtx.Dp(reef.Sp5), gtx.Dp(reef.Sp2)
		}
	case BlockPara:
		return gtx.Dp(reef.Sp1), gtx.Dp(reef.Sp5)
	case BlockList, BlockQuote:
		return gtx.Dp(reef.Sp2), gtx.Dp(reef.Sp6)
	case BlockCode, BlockTable, BlockFrontmatter:
		return gtx.Dp(reef.Sp3), gtx.Dp(reef.Sp6)
	case BlockHR:
		return gtx.Dp(reef.Sp5), gtx.Dp(reef.Sp5)
	default:
		return 0, gtx.Dp(reef.Sp4)
	}
}

// prettyHotForText builds a per-rune hot map from diff segments (byte offsets).
// Same logic as `internal/diffparse/refine.go` segments and `internal/ui/diffpane.go:1069` buildCells hot.
func prettyHotForText(text string, segs []diffparse.Segment) []bool {
	if len(segs) == 0 || text == "" {
		return nil
	}
	runes := []rune(text)
	hot := make([]bool, len(runes))
	// Map byte offsets to rune indices
	byteOff := 0
	for i, r := range runes {
		for _, s := range segs {
			if s.Changed && byteOff >= s.Start && byteOff < s.End {
				hot[i] = true
				break
			}
		}
		byteOff += len(string(r))
	}
	// If nothing hot, return nil to avoid extra work
	for _, h := range hot {
		if h {
			return hot
		}
	}
	return nil
}

// prettyOldHeight is the height of the old-text del block shown above a modified pretty block.
func (a *App) prettyOldHeight(gtx layout.Context, b *PrettyBlock, w int) int {
	if b.OldText == "" || b.Change != ChangeAdded {
		return 0
	}
	lh := a.ui.TextRow(gtx, reef.SizeBody)
	cell := max(1, a.ui.Cell(gtx, reef.SizeBody, false).X)
	cols := max(1, w/cell)
	lines := wrapPrettyInlines([]Inline{{Text: b.OldText}}, cols)
	h := max(1, len(lines)) * lh
	top, bottom := prettyBlockPadding(gtx, &PrettyBlock{Kind: BlockPara})
	return top + h + bottom + gtx.Dp(reef.Sp2)
}

// prettyHeight is the exact height drawPretty consumes. Keeping all block
// padding here, instead of relying on the list's ordinary row pitch, prevents
// tall tables and code listings from collapsing their children onto one line.
func (a *App) prettyHeight(gtx layout.Context, doc *DiffDoc, i int, width, row int) int {
	r := doc.Row(i)
	if r.Kind != rowPretty || r.Pretty == nil {
		return row
	}
	b := r.Pretty
	_, w := a.prettyContentBounds(gtx, width)
	top, bottom := prettyBlockPadding(gtx, b)

	var base int
	switch b.Kind {
	case BlockCode:
		base = top + a.prettyCodeHeight(gtx, b, w) + bottom
	case BlockHeading:
		sz := headingSize(b.Level)
		cell := max(1, a.ui.Cell(gtx, sz, false).X)
		lines := wrapPrettyInlines(b.Inlines, max(1, w/cell))
		base = top + max(1, len(lines))*a.ui.TextRow(gtx, sz) + bottom
	case BlockFrontmatter:
		base = top + a.prettyFrontmatterHeight(gtx, b, w) + bottom
	case BlockTable:
		base = top + a.prettyTableMetrics(gtx, b, w).total + bottom
	case BlockHR:
		base = top + 1 + bottom
	case BlockList:
		base = top + a.prettyListHeight(gtx, b, w) + bottom
	case BlockQuote:
		inset := gtx.Dp(reef.Sp6)
		cell := max(1, a.ui.Cell(gtx, reef.SizeBody, false).X)
		lines := wrapPrettyInlines(b.Inlines, max(1, (w-inset)/cell))
		base = top + max(1, len(lines))*a.ui.TextRow(gtx, reef.SizeBody) + bottom
	default:
		cell := max(1, a.ui.Cell(gtx, reef.SizeBody, false).X)
		lines := wrapPrettyInlines(b.Inlines, max(1, w/cell))
		base = top + max(1, len(lines))*a.ui.TextRow(gtx, reef.SizeBody) + bottom
	}
	return base + a.prettyOldHeight(gtx, b, w)
}

func headingSize(level int) unit.Sp {
	switch level {
	case 1:
		return reef.SizeH1
	case 2:
		return reef.SizeH2
	case 3:
		return reef.SizeH3
	default:
		return reef.SizeH4
	}
}

// inlinesText returns Markdown text exactly as parsed. An inline boundary is
// not whitespace: inserting a space between runs corrupts punctuation and
// makes code/emphasis visibly drift apart from the sentence around it.
func inlinesText(inlines []Inline) string {
	var b strings.Builder
	for _, inl := range inlines {
		b.WriteString(inl.Text)
	}
	return b.String()
}

func inlineRunes(inlines []Inline) []prettyRune {
	var out []prettyRune
	for _, inl := range inlines {
		style := inl
		style.Text = ""
		for _, r := range inl.Text {
			out = append(out, prettyRune{r: r, style: style})
		}
	}
	return out
}

// wrapPrettyInlines collapses Markdown whitespace, preserves explicit hard
// breaks, and carries each run's style onto the visual lines it produces.
func wrapPrettyInlines(inlines []Inline, cols int) [][]prettyRune {
	cols = max(1, cols)
	src := inlineRunes(inlines)
	lines := make([][]prettyRune, 0, 1)
	line := make([]prettyRune, 0, cols)
	var pending *prettyRune

	flush := func(force bool) {
		if len(line) > 0 || force {
			lines = append(lines, line)
			line = nil
		}
	}
	for i := 0; i < len(src); {
		if src[i].r == '\n' {
			flush(true)
			pending = nil
			i++
			continue
		}
		if unicode.IsSpace(src[i].r) {
			if len(line) > 0 {
				sp := src[i]
				sp.r = ' '
				pending = &sp
			}
			for i < len(src) && src[i].r != '\n' && unicode.IsSpace(src[i].r) {
				i++
			}
			continue
		}

		j := i + 1
		for j < len(src) && src[j].r != '\n' && !unicode.IsSpace(src[j].r) {
			j++
		}
		word := src[i:j]
		sep := 0
		if pending != nil && len(line) > 0 {
			sep = 1
		}
		if len(line)+sep+len(word) > cols && len(line) > 0 {
			flush(false)
			sep = 0
		}
		if sep == 1 {
			line = append(line, *pending)
		}
		pending = nil

		for len(word) > 0 {
			room := cols - len(line)
			if room == 0 {
				flush(false)
				room = cols
			}
			n := min(room, len(word))
			line = append(line, word[:n]...)
			word = word[n:]
			if len(word) > 0 {
				flush(false)
			}
		}
		i = j
	}
	flush(len(lines) == 0)
	return lines
}

func sameInlineStyle(a, b Inline) bool {
	return a.Bold == b.Bold && a.Italic == b.Italic && a.Code == b.Code &&
		a.Strike == b.Strike && a.Link == b.Link
}

// drawPrettyLine draws one visual line of styled prose. All fonts in this
// surface share the same Plex grid, so run backgrounds and link rules can be
// placed from character counts without remeasuring shaped text.
func (a *App) drawPrettyLine(gtx layout.Context, line []prettyRune, x, y, w, lh int, size unit.Sp, weight font.Weight, style font.Style, color reef.ColorNRGBA) {
	if len(line) == 0 || w <= 0 {
		return
	}
	cell := max(1, a.ui.Cell(gtx, size, false).X)
	for i := 0; i < len(line); {
		j := i + 1
		for j < len(line) && sameInlineStyle(line[i].style, line[j].style) {
			j++
		}
		var text strings.Builder
		for _, c := range line[i:j] {
			text.WriteRune(c.r)
		}
		run := line[i].style
		rx := x + i*cell
		rw := (j - i) * cell
		if run.Code {
			pad := gtx.Dp(reef.Sp1)
			reef.FillRect(gtx, image.Rect(rx-pad, y+pad, min(x+w, rx+rw+pad), y+lh-pad), a.ui.P.BgSunken)
		}
		runWeight := weight
		if run.Bold && runWeight == font.Normal {
			runWeight = reef.WeightLabel
		}
		runStyle := style
		if run.Italic {
			runStyle = font.Italic
		}
		runColor := color
		if run.Link != "" {
			runColor = a.ui.P.Action
		} else if run.Code {
			runColor = a.ui.P.Strong
		}
		off := op.Offset(image.Pt(rx, y)).Push(gtx.Ops)
		a.ui.TextAt(gtx, reef.Run{Size: size, Weight: runWeight, Style: runStyle, Color: runColor}, 0, lh, min(rw, x+w-rx), text.String())
		off.Pop()
		if run.Link != "" {
			reef.FillRect(gtx, image.Rect(rx, y+lh-2, min(x+w, rx+rw), y+lh-1), a.ui.P.Action)
		}
		if run.Strike {
			reef.FillRect(gtx, image.Rect(rx, y+lh/2, min(x+w, rx+rw), y+lh/2+1), runColor)
		}
		i = j
	}
}

// drawPretty paints one rendered document block.
func (a *App) drawPretty(gtx layout.Context, doc *DiffDoc, i int, cell image.Point, _ int) {
	ui := a.ui
	size := gtx.Constraints.Max
	r := doc.Row(i)
	if r.Kind != rowPretty || r.Pretty == nil {
		return
	}
	b := r.Pretty
	cursor := i == doc.Cursor && a.focus == PaneDiff

	// For modified paras/headings (not lists), show old (Del) stacked above new (Add) with hot for exact words - like code.
	if b.Kind != BlockList && b.OldText != "" && b.Change == ChangeAdded {
		_, wOld := a.prettyContentBounds(gtx, size.X)
		oldH := a.prettyOldHeight(gtx, b, wOld)
		if oldH > 0 && oldH < size.Y {
			// Old part - Del
			reef.FillRect(gtx, image.Rect(0, 0, size.X, oldH), ui.P.DelBg)
			reef.FillRect(gtx, image.Rect(0, 0, 2, oldH), ui.P.DelGutter)
			markerH := ui.CodeRow(gtx)
			gut := a.prettyGutterWidth(gtx)
			mx := max(gtx.Dp(reef.Sp1), (gut-cell.X)/2)
			off := op.Offset(image.Pt(0, gtx.Dp(reef.Sp1))).Push(gtx.Ops)
			a.codeText(gtx, mx, markerH, gut, reef.WeightLabel, ui.P.DelFg, "−")
			off.Pop()
			xOld, wOld2 := a.prettyContentBounds(gtx, size.X)
			topOld, _ := prettyBlockPadding(gtx, &PrettyBlock{Kind: BlockPara})
			lh := ui.TextRow(gtx, reef.SizeBody)
			cellBody := max(1, ui.Cell(gtx, reef.SizeBody, false).X)
			cols := max(1, wOld2/cellBody)
			oldInlines := []Inline{{Text: b.OldText}}
			lines := wrapPrettyInlines(oldInlines, cols)
			hotForOld := b.OldHot
			if hotForOld == nil && len(b.OldSegs) > 0 {
				hotForOld = prettyHotForText(b.OldText, b.OldSegs)
			}
			globalBase := 0
			for li, line := range lines {
				y := topOld + li*lh
				if hotForOld != nil {
					for ri := range line {
						gi := globalBase + ri
						if gi < len(hotForOld) && hotForOld[gi] {
							rx := xOld + ri*cellBody
							reef.FillRect(gtx, image.Rect(rx, y, rx+cellBody, y+lh), ui.P.DelBgHot)
						}
					}
				}
				a.drawPrettyLine(gtx, line, xOld, y, wOld2, lh, reef.SizeBody, font.Normal, font.Regular, ui.P.Fg)
				globalBase += len(line)
			}
			if r.Noted {
				reef.VLine(gtx, gut-gtx.Dp(reef.Sp3), oldH, ui.P.Action)
			} else {
				reef.VLine(gtx, gut-gtx.Dp(reef.Sp3), oldH, ui.P.RuleFaint)
			}
			reef.FillRect(gtx, image.Rect(gut, oldH-1, size.X, oldH), ui.P.RuleFaint)
			offNew := op.Offset(image.Pt(0, oldH)).Push(gtx.Ops)
			newSize := image.Pt(size.X, size.Y-oldH)
			reef.FillRect(gtx, image.Rect(0, 0, newSize.X, newSize.Y), ui.P.AddBg)
			reef.FillRect(gtx, image.Rect(0, 0, 2, newSize.Y), ui.P.AddGutter)
			off2 := op.Offset(image.Pt(0, gtx.Dp(reef.Sp1))).Push(gtx.Ops)
			a.codeText(gtx, mx, markerH, gut, reef.WeightLabel, ui.P.AddFg, "+")
			off2.Pop()
			if r.Noted {
				reef.VLine(gtx, gut-gtx.Dp(reef.Sp3), newSize.Y, ui.P.Action)
			} else {
				reef.VLine(gtx, gut-gtx.Dp(reef.Sp3), newSize.Y, ui.P.RuleFaint)
			}
			x, w := a.prettyContentBounds(gtx, size.X)
			if w > 0 {
				top, _ := prettyBlockPadding(gtx, b)
				switch b.Kind {
				case BlockFrontmatter:
					a.drawFrontmatter(gtx, b, x, w, top)
				case BlockHeading:
					a.drawHeading(gtx, b, x, w, top, newSize.Y)
				case BlockPara:
					if b.Segments != nil {
						a.drawParaWithHot(gtx, b, x, w, top)
					} else {
						a.drawParaLike(gtx, b, x, w, top)
					}
				case BlockQuote, BlockList:
					a.drawParaLike(gtx, b, x, w, top)
				case BlockCode:
					a.drawPrettyCode(gtx, b, x, w, top, cell, i, ui.P.AddBgHot)
				case BlockHR:
					reef.FillRect(gtx, image.Rect(x, top, x+w, top+1), ui.P.Rule)
				case BlockTable:
					a.drawPrettyTable(gtx, b, x, w, top)
				default:
					a.drawParaLike(gtx, b, x, w, top)
				}
			}
			if cursor {
				reef.HLine(gtx, size.X, 0, ui.P.Focus)
				reef.HLine(gtx, size.X, size.Y-1, ui.P.Focus)
			}
			gut2 := a.prettyGutterWidth(gtx)
			a.hoverable(gtx, image.Rect(0, 0, gut2, size.Y), diffTag{row: i, gutter: true}, i, func() {
				doc.Cursor = i
				a.focus = PaneDiff
				a.after(a.startComment)
			})
			if size.X > gut2 {
				a.selectArea(gtx, image.Rect(gut2, 0, size.X, size.Y), i, gut2, cell)
			}
			offNew.Pop()
			return
		}
	}

	bg, hot, marker, markerColor := ui.P.Bg, ui.P.Bg, "", ui.P.Faint
	gutterColor := reef.ColorNRGBA{}
	switch b.Change {
	case ChangeAdded:
		bg, hot, marker, markerColor = ui.P.AddBg, ui.P.AddBgHot, "+", ui.P.AddFg
		gutterColor = ui.P.AddGutter
	case ChangeRemoved:
		bg, hot, marker, markerColor = ui.P.DelBg, ui.P.DelBgHot, "−", ui.P.DelFg
		gutterColor = ui.P.DelGutter
	}
	reef.Fill(gtx, size, bg)
	if gutterColor.A != 0 {
		reef.Edge(gtx, size.Y, gutterColor)
	}

	gut := a.prettyGutterWidth(gtx)
	a.hoverable(gtx, image.Rect(0, 0, gut, size.Y), diffTag{row: i, gutter: true}, i, func() {
		doc.Cursor = i
		a.focus = PaneDiff
		a.after(a.startComment)
	})
	if size.X > gut {
		a.selectArea(gtx, image.Rect(gut, 0, size.X, size.Y), i, gut, cell)
	}
	if cursor {
		reef.HLine(gtx, size.X, 0, ui.P.Focus)
		reef.HLine(gtx, size.X, size.Y-1, ui.P.Focus)
		if gutterColor.A == 0 {
			reef.Edge(gtx, size.Y, ui.P.Focus)
		}
	}

	margin := ui.P.RuleFaint
	if r.Noted {
		margin = ui.P.Action
	}
	reef.VLine(gtx, gut-gtx.Dp(reef.Sp3), size.Y, margin)

	top, _ := prettyBlockPadding(gtx, b)
	if marker != "" {
		markerH := ui.CodeRow(gtx)
		mx := max(gtx.Dp(reef.Sp1), (gut-cell.X)/2)
		off := op.Offset(image.Pt(0, top)).Push(gtx.Ops)
		a.codeText(gtx, mx, markerH, gut, reef.WeightLabel, markerColor, marker)
		off.Pop()
	}

	x, w := a.prettyContentBounds(gtx, size.X)
	if w <= 0 {
		return
	}
	switch b.Kind {
	case BlockFrontmatter:
		a.drawFrontmatter(gtx, b, x, w, top)
	case BlockHeading:
		a.drawHeading(gtx, b, x, w, top, size.Y)
	case BlockPara:
		if b.Segments != nil && b.Change == ChangeAdded {
			a.drawParaWithHot(gtx, b, x, w, top)
		} else {
			a.drawParaLike(gtx, b, x, w, top)
		}
	case BlockQuote, BlockList:
		a.drawParaLike(gtx, b, x, w, top)
	case BlockCode:
		a.drawPrettyCode(gtx, b, x, w, top, cell, i, hot)
	case BlockHR:
		reef.FillRect(gtx, image.Rect(x, top, x+w, top+1), ui.P.Rule)
	case BlockTable:
		a.drawPrettyTable(gtx, b, x, w, top)
	default:
		a.drawParaLike(gtx, b, x, w, top)
	}
}

func (a *App) prettyFrontmatterHeight(gtx layout.Context, b *PrettyBlock, w int) int {
	headerH := gtx.Dp(reef.ControlHSm)
	lh := a.ui.TextRow(gtx, reef.SizeUI)
	pad := gtx.Dp(reef.Sp3)
	cell := max(1, a.ui.Cell(gtx, reef.SizeUI, false).X)
	keyW := min(w*2/5, max(10*cell, w/3))
	valueW := max(cell, w-keyW-2*pad)
	total := headerH
	for _, e := range b.Frontmatter {
		keyLines := wrapPrettyInlines([]Inline{{Text: e.Key, Bold: true}}, max(1, (keyW-2*pad)/cell))
		valueLines := wrapPrettyInlines([]Inline{{Text: e.ValueStr}}, max(1, valueW/cell))
		total += max(len(keyLines), len(valueLines))*lh + 2*gtx.Dp(reef.Sp1)
	}
	return max(headerH+lh, total)
}

func (a *App) drawFrontmatter(gtx layout.Context, b *PrettyBlock, x, w, y int) {
	ui := a.ui
	h := a.prettyFrontmatterHeight(gtx, b, w)
	headerH := gtx.Dp(reef.ControlHSm)
	lh := ui.TextRow(gtx, reef.SizeUI)
	pad := gtx.Dp(reef.Sp3)
	cell := max(1, ui.Cell(gtx, reef.SizeUI, false).X)
	keyW := min(w*2/5, max(10*cell, w/3))
	valueW := max(cell, w-keyW-2*pad)

	reef.FillRect(gtx, image.Rect(x, y, x+w, y+h), ui.P.BgAlt)
	reef.FillRect(gtx, image.Rect(x, y, x+w, y+headerH), ui.P.BgSunken)
	reef.Outline(gtx, image.Rect(x, y, x+w, y+h), gtx.Dp(reef.BorderHair), ui.P.Rule)
	off := op.Offset(image.Pt(x+pad, y)).Push(gtx.Ops)
	ui.LabelAt(gtx, ui.P.Faint, 0, headerH, w-2*pad, "DOCUMENT METADATA")
	off.Pop()
	reef.FillRect(gtx, image.Rect(x, y+headerH-1, x+w, y+headerH), ui.P.Rule)

	cy := y + headerH
	for _, e := range b.Frontmatter {
		keyLines := wrapPrettyInlines([]Inline{{Text: e.Key, Bold: true}}, max(1, (keyW-2*pad)/cell))
		valueLines := wrapPrettyInlines([]Inline{{Text: e.ValueStr}}, max(1, valueW/cell))
		lines := max(len(keyLines), len(valueLines))
		rowH := lines*lh + 2*gtx.Dp(reef.Sp1)
		for li := 0; li < lines; li++ {
			lineY := cy + gtx.Dp(reef.Sp1) + li*lh
			if li < len(keyLines) {
				a.drawPrettyLine(gtx, keyLines[li], x+pad, lineY, keyW-pad, lh, reef.SizeUI, reef.WeightLabel, font.Regular, ui.P.Faint)
			}
			if li < len(valueLines) {
				a.drawPrettyLine(gtx, valueLines[li], x+keyW+pad, lineY, valueW, lh, reef.SizeUI, font.Normal, font.Regular, ui.P.Fg)
			}
		}
		cy += rowH
		if cy < y+h {
			reef.FillRect(gtx, image.Rect(x, cy-1, x+w, cy), ui.P.RuleFaint)
		}
	}
}

func (a *App) drawHeading(gtx layout.Context, b *PrettyBlock, x, w, y, blockH int) {
	ui := a.ui
	sz := headingSize(b.Level)
	lh := ui.TextRow(gtx, sz)
	cell := max(1, ui.Cell(gtx, sz, false).X)
	lines := wrapPrettyInlines(b.Inlines, max(1, w/cell))
	weight := reef.WeightLabel
	if b.Level == 1 {
		weight = reef.WeightDisplay
	}
	for i, line := range lines {
		a.drawPrettyLine(gtx, line, x, y+i*lh, w, lh, sz, weight, font.Regular, ui.P.Strong)
	}
	_, bottom := prettyBlockPadding(gtx, b)
	ruleY := blockH - max(1, bottom/2)
	if b.Level == 1 {
		ruleW := min(w, gtx.Dp(reef.Sp10))
		reef.FillRect(gtx, image.Rect(x, ruleY, x+ruleW, ruleY+gtx.Dp(reef.BorderThick)), ui.P.Action)
	} else if b.Level == 2 {
		reef.FillRect(gtx, image.Rect(x, ruleY, x+w, ruleY+1), ui.P.RuleFaint)
	}
}

func (a *App) prettyListHeight(gtx layout.Context, b *PrettyBlock, w int) int {
	lh := a.ui.TextRow(gtx, reef.SizeBody)
	cell := max(1, a.ui.Cell(gtx, reef.SizeBody, false).X)
	prefixCells := 3
	if b.Ordered {
		prefixCells = len(fmt.Sprintf("%d.", b.ListStart+max(0, len(b.Items)-1))) + 1
	}
	cols := max(1, (w-prefixCells*cell)/cell)
	gap := gtx.Dp(reef.Sp2)
	total := 0
	for _, item := range b.Items {
		total += max(1, len(wrapPrettyInlines(item, cols)))*lh + gap
	}
	if total > 0 {
		total -= gap
	}
	return max(lh, total)
}

func (a *App) drawParaLike(gtx layout.Context, b *PrettyBlock, x, w, y int) {
	ui := a.ui
	lh := ui.TextRow(gtx, reef.SizeBody)
	cell := max(1, ui.Cell(gtx, reef.SizeBody, false).X)

	switch b.Kind {
	case BlockList:
		prefixCells := 3
		if b.Ordered {
			prefixCells = len(fmt.Sprintf("%d.", b.ListStart+max(0, len(b.Items)-1))) + 1
		}
		prefixW := prefixCells * cell
		cols := max(1, (w-prefixW)/cell)
		gap := gtx.Dp(reef.Sp2)
		cy := y
		for idx, item := range b.Items {
			prefix := "•"
			if b.Ordered {
				prefix = fmt.Sprintf("%d.", b.ListStart+idx)
			}
			off := op.Offset(image.Pt(x, cy)).Push(gtx.Ops)
			ui.TextAt(gtx, reef.Run{Size: reef.SizeBody, Weight: reef.WeightLabel, Color: ui.P.Action}, 0, lh, prefixW-cell/2, prefix)
			off.Pop()
			lines := wrapPrettyInlines(item, cols)
			isAdded := idx < len(b.ItemChange) && b.ItemChange[idx] == ChangeAdded
			// Per-item tint like code, avoids tinting whole list when only one bullet changed
			if isAdded {
				itemH := max(1, len(lines)) * lh
				reef.FillRect(gtx, image.Rect(x, cy, x+w, cy+itemH), ui.P.AddBg)
			}
			var hot []bool
			if isAdded && idx < len(b.ItemHot) && b.ItemHot[idx] != nil {
				hot = b.ItemHot[idx]
			} else if isAdded && idx < len(b.ItemSegs) {
				hot = prettyHotForText(inlinesText(item), b.ItemSegs[idx])
			}
			offItem := 0
			for li, line := range lines {
				yLine := cy + li*lh
				if hot != nil {
					for ri := range line {
						gi := offItem + ri
						if gi < len(hot) && hot[gi] {
							rx := x + prefixW + ri*cell
							reef.FillRect(gtx, image.Rect(rx, yLine, rx+cell, yLine+lh), ui.P.AddBgHot)
						}
					}
				}
				a.drawPrettyLine(gtx, line, x+prefixW, yLine, w-prefixW, lh, reef.SizeBody, font.Normal, font.Regular, ui.P.Fg)
				offItem += len(line)
			}
			cy += max(1, len(lines))*lh + gap
		}
	case BlockQuote:
		inset := gtx.Dp(reef.Sp6)
		txtW := max(1, w-inset)
		lines := wrapPrettyInlines(b.Inlines, max(1, txtW/cell))
		height := max(1, len(lines)) * lh
		reef.FillRect(gtx, image.Rect(x, y, x+w, y+height), ui.P.BgAlt)
		reef.FillRect(gtx, image.Rect(x, y, x+gtx.Dp(reef.BorderThick), y+height), ui.P.Action)
		for li, line := range lines {
			a.drawPrettyLine(gtx, line, x+inset, y+li*lh, txtW-inset/2, lh, reef.SizeBody, font.Normal, font.Italic, ui.P.Muted)
		}
	default:
		lines := wrapPrettyInlines(b.Inlines, max(1, w/cell))
		for li, line := range lines {
			a.drawPrettyLine(gtx, line, x, y+li*lh, w, lh, reef.SizeBody, font.Normal, font.Regular, ui.P.Fg)
		}
	}
}

// drawParaWithHot draws a paragraph with exact-change hot highlights, word-level.
// Reuses same word-level logic as code via prettyHotForPretty so pretty and code share logic.
func (a *App) drawParaWithHot(gtx layout.Context, b *PrettyBlock, x, w, y int) {
	ui := a.ui
	lh := ui.TextRow(gtx, reef.SizeBody)
	cell := max(1, ui.Cell(gtx, reef.SizeBody, false).X)
	cols := max(1, w/cell)
	lines := wrapPrettyInlines(b.Inlines, cols)
	hot := b.Hot
	if hot == nil && len(b.Segments) > 0 {
		text := inlinesText(b.Inlines)
		hot = prettyHotForText(text, b.Segments)
	}
	// Map from global rune index to hot; lines are sequential slices of the same runes
	runes := inlineRunes(b.Inlines)
	// Build index map: each rune in runes corresponds to hot index
	off := 0
	for li, line := range lines {
		yLine := y + li*lh
		// Hot background for this visual line, like `drawCode` `hotColor`
		if hot != nil {
			for ri := range line {
				gi := off + ri
				if gi < len(hot) && hot[gi] {
					rx := x + ri*cell
					reef.FillRect(gtx, image.Rect(rx, yLine, rx+cell, yLine+lh), ui.P.AddBgHot)
				}
			}
		}
		a.drawPrettyLine(gtx, line, x, yLine, w, lh, reef.SizeBody, font.Normal, font.Regular, ui.P.Fg)
		off += len(line)
		// Wrap skips collapsed spaces, but for plain text off matches; for safety clamp
		if off > len(runes) {
			off = len(runes)
		}
	}
}

func prettyCodeLines(code string) []string {
	code = strings.ReplaceAll(code, "\r\n", "\n")
	code = strings.TrimSuffix(code, "\n")
	if code == "" {
		return []string{""}
	}
	return strings.Split(code, "\n")
}

func (a *App) prettyCodeHeight(gtx layout.Context, b *PrettyBlock, w int) int {
	lh := a.ui.CodeRow(gtx)
	headerH := 0
	if b.Lang != "" {
		headerH = gtx.Dp(reef.ControlHSm)
	}
	pad := gtx.Dp(reef.Sp3)
	codeW := max(1, w-2*gtx.Dp(reef.PadInline))
	cols := max(1, codeW/max(1, a.ui.Cell(gtx, reef.SizeCode, false).X))
	visual := 0
	for _, line := range prettyCodeLines(b.Code) {
		if a.wrapping() {
			visual += wrappedLineCount(line, cols)
		} else {
			visual++
		}
	}
	return headerH + 2*pad + max(1, visual)*lh
}

func (a *App) drawPrettyCode(gtx layout.Context, b *PrettyBlock, x, w, y int, cell image.Point, idx int, hot reef.ColorNRGBA) {
	ui := a.ui
	panelH := a.prettyCodeHeight(gtx, b, w)
	headerH := 0
	if b.Lang != "" {
		headerH = gtx.Dp(reef.ControlHSm)
	}
	padY := gtx.Dp(reef.Sp3)
	padX := gtx.Dp(reef.PadInline)
	panel := image.Rect(x, y, x+w, y+panelH)
	panelBg := ui.P.BgSunken
	if b.Change != ChangeContext {
		panelBg = hot
	}
	reef.FillRect(gtx, panel, panelBg)
	reef.Outline(gtx, panel, gtx.Dp(reef.BorderHair), ui.P.Rule)
	if headerH > 0 {
		off := op.Offset(image.Pt(x+padX, y)).Push(gtx.Ops)
		ui.LabelAt(gtx, ui.P.Faint, 0, headerH, w-2*padX, strings.ToUpper(b.Lang))
		off.Pop()
		reef.FillRect(gtx, image.Rect(x, y+headerH-1, x+w, y+headerH), ui.P.RuleFaint)
	}

	codeX := x + padX
	codeY := y + headerH + padY
	codeW := max(1, w-2*padX)
	cellX := max(1, ui.Cell(gtx, reef.SizeCode, false).X)
	cols := max(1, codeW/cellX)
	lh := ui.CodeRow(gtx)
	area := clip.Rect(image.Rect(codeX, codeY, x+w-padX, y+panelH-padY)).Push(gtx.Ops)
	cy := codeY
	for li, line := range prettyCodeLines(b.Code) {
		spans := []highlight.Span(nil)
		if li < len(b.Spans) {
			spans = b.Spans[li]
		}
		tmp := Row{Line: diffparse.Line{Text: line}, Spans: spans}
		if a.wrapping() {
			visual := wrappedLineCount(line, cols)
			fill(gtx, image.Pt(codeX, cy), image.Pt(codeW, visual*lh), func(sub layout.Context) {
				a.drawCodeWrapped(sub, tmp, cell, lh, idx, hot, cols)
			})
			cy += visual * lh
			continue
		}
		offsetX := a.diffX * cellX
		fit(gtx, image.Pt(codeX-offsetX, cy), image.Pt(codeW+offsetX, lh), func(sub layout.Context) {
			a.drawCode(sub, tmp, cell, lh, idx, hot)
		})
		cy += lh
	}
	area.Pop()
}

func tableRows(b *PrettyBlock) [][][]Inline {
	rows := make([][][]Inline, 0, len(b.TableRows)+1)
	if len(b.TableHead) > 0 {
		rows = append(rows, b.TableHead)
	}
	return append(rows, b.TableRows...)
}

func tableColumnCount(b *PrettyBlock) int {
	n := len(b.TableHead)
	for _, row := range b.TableRows {
		n = max(n, len(row))
	}
	return max(1, n)
}

func (a *App) prettyTableMetrics(gtx layout.Context, b *PrettyBlock, w int) prettyTableLayout {
	cols := tableColumnCount(b)
	cell := max(1, a.ui.Cell(gtx, reef.SizeUI, false).X)
	pad := gtx.Dp(reef.Sp3)
	inner := max(cols, w-2*pad*cols)
	weights := make([]int, cols)
	for i := range weights {
		weights[i] = 10
	}
	for _, row := range tableRows(b) {
		for col, value := range row {
			weights[col] = max(weights[col], min(48, len([]rune(inlinesText(value)))))
		}
	}
	weightTotal := 0
	for _, weight := range weights {
		weightTotal += weight
	}
	widths := make([]int, cols)
	remaining := inner
	for i, weight := range weights {
		if i == cols-1 {
			widths[i] = remaining + 2*pad
			break
		}
		share := max(cell+2*pad, remaining*weight/max(1, weightTotal))
		share = min(share, remaining-(cols-i-1)*cell)
		widths[i] = share + 2*pad
		remaining -= share
		weightTotal -= weight
	}

	lh := a.ui.TextRow(gtx, reef.SizeUI)
	heights := make([]int, 0, len(b.TableRows)+1)
	total := 0
	for _, row := range tableRows(b) {
		lines := 1
		for col := 0; col < cols; col++ {
			if col >= len(row) {
				continue
			}
			cellCols := max(1, (widths[col]-2*pad)/cell)
			lines = max(lines, len(wrapPrettyInlines(row[col], cellCols)))
		}
		h := lines*lh + 2*gtx.Dp(reef.Sp2)
		heights = append(heights, h)
		total += h
	}
	if len(heights) == 0 {
		heights = []int{lh + 2*gtx.Dp(reef.Sp2)}
		total = heights[0]
	}
	return prettyTableLayout{widths: widths, heights: heights, total: total}
}

func (a *App) drawPrettyTable(gtx layout.Context, b *PrettyBlock, x, w, y int) {
	ui := a.ui
	metrics := a.prettyTableMetrics(gtx, b, w)
	rows := tableRows(b)
	if len(rows) == 0 {
		return
	}
	lh := ui.TextRow(gtx, reef.SizeUI)
	cell := max(1, ui.Cell(gtx, reef.SizeUI, false).X)
	padX := gtx.Dp(reef.Sp3)
	padY := gtx.Dp(reef.Sp2)
	cy := y
	for ri, rowCells := range rows {
		rh := metrics.heights[ri]
		if ri == 0 && len(b.TableHead) > 0 {
			reef.FillRect(gtx, image.Rect(x, cy, x+w, cy+rh), ui.P.BgSunken)
		}
		cx := x
		for col, colW := range metrics.widths {
			if col > 0 {
				reef.FillRect(gtx, image.Rect(cx, cy, cx+1, cy+rh), ui.P.RuleFaint)
			}
			if col < len(rowCells) {
				cols := max(1, (colW-2*padX)/cell)
				lines := wrapPrettyInlines(rowCells[col], cols)
				for li, line := range lines {
					weight := font.Normal
					color := ui.P.Fg
					if ri == 0 && len(b.TableHead) > 0 {
						weight = reef.WeightLabel
						color = ui.P.Strong
					}
					a.drawPrettyLine(gtx, line, cx+padX, cy+padY+li*lh, colW-2*padX, lh, reef.SizeUI, weight, font.Regular, color)
				}
			}
			cx += colW
		}
		cy += rh
		if cy < y+metrics.total {
			color := ui.P.RuleFaint
			if ri == 0 && len(b.TableHead) > 0 {
				color = ui.P.Rule
			}
			reef.FillRect(gtx, image.Rect(x, cy-1, x+w, cy), color)
		}
	}
	reef.Outline(gtx, image.Rect(x, y, x+w, y+metrics.total), gtx.Dp(reef.BorderHair), ui.P.Rule)
}
