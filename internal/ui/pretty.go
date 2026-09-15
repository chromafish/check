package ui

import (
	"bytes"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
	"gopkg.in/yaml.v3"

	"github.com/chromafish/check/internal/diffparse"
	"github.com/chromafish/check/internal/highlight"
)

// FrontmatterEntry preserves key order.
type FrontmatterEntry struct {
	Key      string
	Value    any
	ValueStr string
}

// ChangeKind classifies a block's diff status.
type ChangeKind uint8

const (
	ChangeContext ChangeKind = iota
	ChangeAdded
	ChangeRemoved
)

// BlockKind enumerates pretty block types.
type BlockKind uint8

const (
	BlockPara BlockKind = iota
	BlockHeading
	BlockList
	BlockQuote
	BlockCode
	BlockHR
	BlockTable
	BlockFrontmatter
)

// Inline holds a run of text with a style.
type Inline struct {
	Text   string
	Bold   bool
	Italic bool
	Code   bool
	Strike bool
	Link   string
}

// PrettyBlock is one rendered block of a document.
type PrettyBlock struct {
	Kind      BlockKind
	Level     int // heading level 1..6
	Inlines   []Inline
	Ordered   bool
	ListStart int
	Items     [][]Inline // for lists
	TableHead [][]Inline // header row cells
	TableRows [][][]Inline
	Code      string
	Lang      string
	Spans     highlight.Lines // highlighted lines for code block (one entry per source line)
	StartLine int
	EndLine   int
	Change    ChangeKind
	OldText   string              // for modified blocks: the removed line's raw text (for reference)
	Segments  []diffparse.Segment // changed segments for this block's new text (from diff refine, kept for code)
	OldSegs   []diffparse.Segment // changed segments for OldText
	Hot       []bool              // per-rune hot for new pretty text, word-level (like code's hot)
	OldHot    []bool              // per-rune hot for OldText
	ItemChange []ChangeKind // per-item change for lists, same length as Items
	ItemSegs   [][]diffparse.Segment // per-item segments for exact highlight
	ItemHot    [][]bool // per-item hot, word-level
	// For frontmatter table:
	Frontmatter []FrontmatterEntry
}

// PrettyDoc is a rendered document ready to draw.
type PrettyDoc struct {
	Path        string
	Src         []byte
	Body        []byte
	Frontmatter []FrontmatterEntry
	Blocks      []PrettyBlock
}

// parseFrontmatter handles Markdown frontmatter: optional YAML block starting with ---\n ... ---\n at file start.
// Returns entries, body (without frontmatter if present and valid), has.
func parseFrontmatter(path string, src []byte) ([]FrontmatterEntry, []byte, bool) {
	// Only for .md
	if !strings.HasSuffix(strings.ToLower(path), ".md") {
		return nil, src, false
	}
	norm := strings.ReplaceAll(string(src), "\r\n", "\n")
	if !strings.HasPrefix(norm, "---\n") {
		return nil, src, false
	}
	lines := strings.Split(norm, "\n")
	if len(lines) == 0 || lines[0] != "---" {
		return nil, src, false
	}
	closeIdx := -1
	for i := 1; i < len(lines); i++ {
		if lines[i] == "---" {
			closeIdx = i
			break
		}
	}
	if closeIdx < 0 {
		return nil, src, false
	}
	frontRaw := strings.Join(lines[1:closeIdx], "\n")
	bodyStr := strings.Join(lines[closeIdx+1:], "\n")
	// Parse YAML
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(frontRaw), &doc); err != nil {
		return nil, src, false
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		// Empty frontmatter treated as empty? but not has? We still treat as frontmatter with no entries and body stripped?
		if strings.TrimSpace(frontRaw) == "" {
			return nil, []byte(bodyStr), true
		}
		return nil, src, false
	}
	mapping := doc.Content[0]
	entries := make([]FrontmatterEntry, 0, len(mapping.Content)/2)
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		kNode := mapping.Content[i]
		vNode := mapping.Content[i+1]
		key := kNode.Value
		var v any
		if err := vNode.Decode(&v); err != nil {
			v = vNode.Value
		}
		entries = append(entries, FrontmatterEntry{
			Key:      key,
			Value:    v,
			ValueStr: fmt.Sprintf("%v", v),
		})
	}
	return entries, []byte(bodyStr), true
}

// lineAtOffset returns 1-indexed line number for byte offset in src.
func lineAtOffset(src []byte, off int) int {
	if off < 0 {
		off = 0
	}
	if off > len(src) {
		off = len(src)
	}
	return 1 + bytes.Count(src[:off], []byte("\n"))
}

// blockLineRange computes start/end line numbers (1-indexed inclusive) for a goldmark node.
// If frontmatter had lines, we need to offset by fmLines. Pass offset.
func blockLineRange(src []byte, n ast.Node, fmLines int) (int, int) {
	// Use node Lines() if available. For many block nodes, Lines returns segments.
	// We extract segment positions and compute min/max.
	var startOff, endOff int = -1, -1
	if block, ok := n.(interface{ Lines() *text.Segments }); ok {
		segs := block.Lines()
		for i := 0; i < segs.Len(); i++ {
			seg := segs.At(i)
			if startOff < 0 || seg.Start < startOff {
				startOff = seg.Start
			}
			if endOff < 0 || seg.Stop > endOff {
				endOff = seg.Stop
			}
		}
	}
	// Fallback for nodes without lines but with children: try to derive from first/last child.
	if startOff < 0 {
		// Walk children to find offsets?
		// Use n.Lines if method exists via type assertion to interface with Lines
		// else default to 0.
		return fmLines + 1, fmLines + 1
	}
	// startOff is byte offset in Body src (after frontmatter stripped).
	// The full file offset is fmBytes = bytes before body. But we approximate lines offset.
	// For line numbers we need to count based on full src including frontmatter.
	// So startLine = fmLines + lineAtOffset(body, startOff)
	// However we have body src separate. To compute correctly, we need body.
	// This helper will be called with body src, so lineAtOffset uses body.
	// Then add fmLines.
	start := fmLines + lineAtOffset(src, startOff)
	end := fmLines + lineAtOffset(src, endOff)
	// endOff points past last char; lineAtOffset for endOff may give line after block if block ends with newline?
	// Clamp: if end line > start line and block ends with newline, the next line is not part of block.
	// We estimate end line as startLine + linesInBlock -1.
	// For segments, the Stop is at end of text without trailing newline; so line count ok.
	// But for code blocks, Lines includes content.
	if end < start {
		end = start
	}
	return start, end
}

// buildPrettyDoc renders src as a PrettyDoc (with frontmatter handling).
func buildPrettyDoc(path string, src []byte) (*PrettyDoc, error) {
	entries, body, hasFM := parseFrontmatter(path, src)
	if !hasFM {
		body = src
	}
	var blocks []PrettyBlock
	fmLines := 0
	if hasFM {
		// count lines in frontmatter block including delimiters: "---\n" + interior lines + "---\n"
		// For line mapping, we need to know how many lines frontmatter occupies in original src.
		// Compute by counting newlines in src up to body start offset.
		// Simplistic: fmLines = number of lines removed from src prefix until body.
		// The body is after close delimiter. The original src lines before body = totalLines - linesInBody - maybe?
		norm := strings.ReplaceAll(string(src), "\r\n", "\n")
		// Find bodyNorm in norm to determine offset lines: but body may appear elsewhere.
		// Simpler: lines in norm minus lines in bodyNorm minus 1? Actually body is tail after second delimiter.
		// We can compute fmLines directly from lines split earlier.
		lines := strings.Split(norm, "\n")
		for i, l := range lines {
			if i == 0 {
				continue
			}
			if l == "---" {
				fmLines = i + 1 // lines up to and including closing delimiter
				break
			}
		}
		if len(entries) > 0 {
			blocks = append(blocks, PrettyBlock{
				Kind:        BlockFrontmatter,
				Frontmatter: entries,
				StartLine:   1,
				EndLine:     fmLines,
				Change:      ChangeContext,
			})
		}
	} else {
		// No frontmatter, check if parse had interior but invalid yaml -> treat as content, fmLines stays 0
	}

	ext := strings.ToLower(path)
	isMD := strings.HasSuffix(ext, ".md")
	isADoc := strings.HasSuffix(ext, ".adoc") || strings.HasSuffix(ext, ".asciidoc")

	var mdBlocks []PrettyBlock
	var err error
	switch {
	case isMD:
		mdBlocks, err = renderMarkdownBody(path, body, fmLines)
		if err != nil {
			return nil, err
		}
	case isADoc:
		mdBlocks, err = renderAsciiDocBody(path, body, fmLines)
		if err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("not a doc")
	}
	blocks = append(blocks, mdBlocks...)
	if len(blocks) == 0 {
		// Empty doc yields empty? Still produce at least no blocks.
	}
	return &PrettyDoc{
		Path:        path,
		Src:         src,
		Body:        body,
		Frontmatter: entries,
		Blocks:      blocks,
	}, nil
}

func renderMarkdownBody(path string, body []byte, fmLines int) ([]PrettyBlock, error) {
	md := goldmark.New(
		goldmark.WithExtensions(extension.GFM),
		goldmark.WithParserOptions(
			parser.WithAutoHeadingID(),
		),
	)
	reader := text.NewReader(body)
	doc := md.Parser().Parse(reader)

	var blocks []PrettyBlock
	// Walk top-level children of doc
	for n := doc.FirstChild(); n != nil; n = n.NextSibling() {
		block := nodeToBlock(body, n, fmLines)
		if block != nil {
			// For list, table etc., block already expanded
			if block.Kind == BlockList && len(block.Items) == 0 {
				// empty list skip
				continue
			}
			blocks = append(blocks, *block)
			// For headings that contain inline, already captured.
		}
	}
	// Highlight code blocks
	for i := range blocks {
		if blocks[i].Kind == BlockCode && blocks[i].Lang != "" {
			src := []byte(blocks[i].Code)
			hl := highlight.File("file."+blocks[i].Lang, src)
			if hl != nil {
				blocks[i].Spans = hl
			}
		}
	}
	return blocks, nil
}

func nodeToBlock(src []byte, n ast.Node, fmLines int) *PrettyBlock {
	switch n.Kind() {
	case ast.KindHeading:
		h := n.(*ast.Heading)
		inlines := extractInlines(src, n)
		start, end := blockLineRange(src, n, fmLines)
		return &PrettyBlock{
			Kind:      BlockHeading,
			Level:     h.Level,
			Inlines:   inlines,
			StartLine: start,
			EndLine:   end,
		}
	case ast.KindParagraph:
		inlines := extractInlines(src, n)
		start, end := blockLineRange(src, n, fmLines)
		return &PrettyBlock{
			Kind:      BlockPara,
			Inlines:   inlines,
			StartLine: start,
			EndLine:   end,
		}
	case ast.KindBlockquote:
		// Collect inner paragraphs as single quoted block? For simplicity flatten to one quote block with inlines from children.
		var inlines []Inline
		for c := n.FirstChild(); c != nil; c = c.NextSibling() {
			if c.Kind() == ast.KindParagraph {
				inlines = append(inlines, extractInlines(src, c)...)
				// Add space between paragraphs? Use line break inline?
				inlines = append(inlines, Inline{Text: " "})
			}
		}
		if len(inlines) > 0 && inlines[len(inlines)-1].Text == " " {
			inlines = inlines[:len(inlines)-1]
		}
		start, end := blockLineRange(src, n, fmLines)
		return &PrettyBlock{
			Kind:      BlockQuote,
			Inlines:   inlines,
			StartLine: start,
			EndLine:   end,
		}
	case ast.KindFencedCodeBlock:
		fcb := n.(*ast.FencedCodeBlock)
		lang := string(fcb.Language(src))
		var codeBuf strings.Builder
		lines := fcb.Lines()
		for i := 0; i < lines.Len(); i++ {
			seg := lines.At(i)
			codeBuf.Write(seg.Value(src))
		}
		code := codeBuf.String()
		start, end := blockLineRange(src, n, fmLines)
		return &PrettyBlock{
			Kind:      BlockCode,
			Code:      code,
			Lang:      strings.TrimSpace(lang),
			StartLine: start,
			EndLine:   end,
		}
	case ast.KindCodeBlock:
		cb := n.(*ast.CodeBlock)
		var codeBuf strings.Builder
		lines := cb.Lines()
		for i := 0; i < lines.Len(); i++ {
			seg := lines.At(i)
			codeBuf.Write(seg.Value(src))
		}
		code := codeBuf.String()
		start, end := blockLineRange(src, n, fmLines)
		return &PrettyBlock{
			Kind:      BlockCode,
			Code:      code,
			Lang:      "",
			StartLine: start,
			EndLine:   end,
		}
	case ast.KindThematicBreak:
		start, end := blockLineRange(src, n, fmLines)
		return &PrettyBlock{
			Kind:      BlockHR,
			StartLine: start,
			EndLine:   end,
		}
	case ast.KindList:
		list := n.(*ast.List)
		ordered := list.IsOrdered()
		startNum := list.Start
		var items [][]Inline
		for item := n.FirstChild(); item != nil; item = item.NextSibling() {
			// ListItem: may contain paragraph(s), lists etc. For simple, extract text from its first paragraph.
			var inlines []Inline
			for c := item.FirstChild(); c != nil; c = c.NextSibling() {
				if c.Kind() == ast.KindParagraph || c.Kind() == ast.KindTextBlock {
					inlines = append(inlines, extractInlines(src, c)...)
				} else if c.Kind() == ast.KindList {
					// nested list: ignore for now? Could flatten.
					// Append as text? Skip.
				} else {
					// Fallback: try extract inlines if it's a block with inlines?
					inlines = append(inlines, extractInlines(src, c)...)
				}
			}
			if len(inlines) == 0 {
				inlines = extractInlines(src, item)
			}
			items = append(items, inlines)
		}
		start, end := blockLineRange(src, n, fmLines)
		return &PrettyBlock{
			Kind:      BlockList,
			Ordered:   ordered,
			ListStart: startNum,
			Items:     items,
			StartLine: start,
			EndLine:   end,
		}
	default:
		// Check for GFM table etc. Use string compare for Kind.
		kindStr := n.Kind().String()
		switch kindStr {
		case "Table":
			// Extension table
			return tableToBlock(src, n, fmLines)
		default:
			// Fallback: treat as paragraph if it has text?
			// For unknown container, walk children and create blocks?
			// If node is Document or other container without direct representation, skip.
			// For safety, try to extract inlines and make para if any.
			inlines := extractInlines(src, n)
			if len(inlines) > 0 {
				start, end := blockLineRange(src, n, fmLines)
				if start == 0 {
					start = fmLines + 1
					end = start
				}
				return &PrettyBlock{
					Kind:      BlockPara,
					Inlines:   inlines,
					StartLine: start,
					EndLine:   end,
				}
			}
			return nil
		}
	}
}

func tableToBlock(src []byte, n ast.Node, fmLines int) *PrettyBlock {
	start, end := blockLineRange(src, n, fmLines)
	// Table node children are TableRow, first row is header etc. In GFM extension, Table has rows; header is first row but marked differently?
	var head [][]Inline
	var rows [][][]Inline
	rowIdx := 0
	for r := n.FirstChild(); r != nil; r = r.NextSibling() {
		// r Kind TableRow or TableHeader etc? Actually extension defines Table, TableRow, TableCell
		var cells [][]Inline
		for cell := r.FirstChild(); cell != nil; cell = cell.NextSibling() {
			cells = append(cells, extractInlines(src, cell))
		}
		if rowIdx == 0 {
			head = cells
		} else {
			rows = append(rows, cells)
		}
		rowIdx++
	}
	return &PrettyBlock{
		Kind:      BlockTable,
		TableHead: head,
		TableRows: rows,
		StartLine: start,
		EndLine:   end,
	}
}

func extractInlines(src []byte, n ast.Node) []Inline {
	var inlines []Inline
	walk := func(node ast.Node) {
		for child := node.FirstChild(); child != nil; child = child.NextSibling() {
			switch child.Kind() {
			case ast.KindText:
				t := child.(*ast.Text)
				seg := t.Segment
				txt := string(seg.Value(src))
				if t.HardLineBreak() {
					txt += "\n"
				} else if t.SoftLineBreak() {
					// Markdown folds an ordinary source newline into a word
					// boundary. Preserve hard breaks above, but do not run the
					// last word of one source line into the first of the next.
					txt += " "
				}
				inlines = append(inlines, Inline{Text: txt})
			case ast.KindCodeSpan, ast.KindEmphasis:
				if child.Kind() == ast.KindCodeSpan {
					// CodeSpan?
					// In newer goldmark, code is Code node? But we handle.
					txt := ""
					// Code span text is in child.Text? Actually Code node holds text.
					// Use heuristic: gather text from child
					for gc := child.FirstChild(); gc != nil; gc = gc.NextSibling() {
						if gc.Kind() == ast.KindText {
							txt += string(gc.(*ast.Text).Segment.Value(src))
						}
					}
					if txt == "" {
						// Fallback: use segment?
						// The Code node may store text differently; we can extract from child segment
					}
					inlines = append(inlines, Inline{Text: txt, Code: true})
				} else {
					em := child.(*ast.Emphasis)
					level := em.Level
					inner := extractInlines(src, child)
					for i := range inner {
						if level == 2 {
							inner[i].Bold = true
						} else {
							inner[i].Italic = true
						}
					}
					inlines = append(inlines, inner...)
				}
			case ast.KindLink:
				link := child.(*ast.Link)
				dest := string(link.Destination)
				inner := extractInlines(src, child)
				for i := range inner {
					inner[i].Link = dest
				}
				inlines = append(inlines, inner...)
			case ast.KindImage:
				img := child.(*ast.Image)
				dest := string(img.Destination)
				alt := ""
				for gc := child.FirstChild(); gc != nil; gc = gc.NextSibling() {
					if gc.Kind() == ast.KindText {
						alt += string(gc.(*ast.Text).Segment.Value(src))
					}
				}
				inlines = append(inlines, Inline{Text: alt, Link: dest})
			case ast.KindEmphasis:
				// Duplicates? Already handled but for completeness fallback to walk
				fallthrough
			default:
				kindStr := child.Kind().String()
				switch kindStr {
				case "Strikethrough":
					inner := extractInlines(src, child)
					for i := range inner {
						inner[i].Strike = true
					}
					inlines = append(inlines, inner...)
				case "AutoLink":
					al := child.(*ast.AutoLink)
					url := string(al.URL(src))
					// For autolink, text is url itself
					txt := url
					// Strip mailto:?
					inlines = append(inlines, Inline{Text: txt, Link: url})
				default:
					// For container nodes like Emphasis nested above, we've already handled.
					// Otherwise recurse.
					if child.FirstChild() != nil {
						// Recursively extract
						// To avoid double handling for Text etc., we recurse
						sub := extractInlines(src, child)
						inlines = append(inlines, sub...)
					} else {
						// Possibly a raw string element?
						// Try to get segment value if implements interface with Segment
						// Skip.
					}
				}
			}
		}
	}
	// If n itself is a text node (e.g., TableCell may contain directly Text?), handle n's kind
	// But the above walks children of n, so for Text node directly under paragraph we already handled.
	// For safety, if n.Kind() == Text etc., we could handle.
	// Instead just call walk(n)
	walk(n)
	// Post-process to merge consecutive inlines with same style?
	return mergeInlines(inlines)
}

func mergeInlines(inlines []Inline) []Inline {
	if len(inlines) == 0 {
		return inlines
	}
	var out []Inline
	for _, cur := range inlines {
		if len(out) > 0 && out[len(out)-1].Bold == cur.Bold && out[len(out)-1].Italic == cur.Italic && out[len(out)-1].Code == cur.Code && out[len(out)-1].Strike == cur.Strike && out[len(out)-1].Link == cur.Link {
			out[len(out)-1].Text += cur.Text
		} else {
			out = append(out, cur)
		}
	}
	return out
}

// markChanges annotates each block with its diff status by line number overlap.
func markChanges(doc *PrettyDoc, file *diffparse.File) {
	if file == nil || len(file.Hunks) == 0 {
		for i := range doc.Blocks {
			doc.Blocks[i].Change = ChangeContext
		}
		return
	}
	var adds []prettyInterval
	var mappedDels []prettyInterval
	for _, h := range file.Hunks {
		delta := h.NewStart - h.OldStart
		for _, l := range h.Lines {
			switch l.Kind {
			case diffparse.Added:
				if l.NewNum > 0 {
					adds = append(adds, prettyInterval{s: l.NewNum, e: l.NewNum + 1})
				}
			case diffparse.Removed:
				if l.OldNum > 0 {
					mappedDels = append(mappedDels, prettyInterval{s: l.OldNum + delta, e: l.OldNum + delta + 1})
				}
			}
		}
	}
	// Use adds vs mappedDels for determining block change.
	// If a block overlaps any add interval -> Added, else if overlaps any mapped del interval -> Removed, else Context.
	// For added blocks that are modifications, also store the old text and segments for inline diff / old view.
	for i := range doc.Blocks {
		b := &doc.Blocks[i]
		if b.Kind == BlockFrontmatter {
			b.Change = ChangeContext
			continue
		}
		// Lists are handled per-item to avoid tinting the whole list when only one bullet changed
		if b.Kind == BlockList {
			b.Change = ChangeContext
			b.ItemChange = make([]ChangeKind, len(b.Items))
			b.ItemSegs = make([][]diffparse.Segment, len(b.Items))
			b.ItemHot = make([][]bool, len(b.Items))
			for idx, item := range b.Items {
				snippet := inlinesText(item)
				snippet = strings.TrimSpace(snippet)
				if len(snippet) > 30 {
					snippet = snippet[:30]
				}
				for _, h := range file.Hunks {
					for _, l := range h.Lines {
						if l.Kind != diffparse.Added {
							continue
						}
						if strings.Contains(l.Text, snippet) {
							b.ItemChange[idx] = ChangeAdded
							b.ItemSegs[idx] = l.Segments
							// Word-level hot for this item vs its old counterpart
							delta := h.NewStart - h.OldStart
							for _, r := range h.Lines {
								if r.Kind == diffparse.Removed && r.OldNum+delta == l.NewNum {
									// Old item text is r.Text without bullet prefix
									oldItemText := strings.TrimPrefix(strings.TrimSpace(r.Text), "* ")
									oldItemText = strings.TrimPrefix(oldItemText, "- ")
									newItemText := inlinesText(item)
									_, newHot := prettyHotForPretty(oldItemText, newItemText)
									b.ItemHot[idx] = newHot
									break
								}
							}
							if b.ItemHot[idx] == nil {
								// Fallback to word-level from raw segments
								b.ItemHot[idx] = prettyHotForText(inlinesText(item), l.Segments)
							}
							break
						}
					}
					if b.ItemChange[idx] != ChangeContext {
						break
					}
				}
			}
			continue
		}
		overAdded := overlapsAny(b.StartLine, b.EndLine, adds)
		overDel := overlapsAny(b.StartLine, b.EndLine, mappedDels)
		switch {
		case overAdded && overDel:
			b.Change = ChangeAdded
		case overAdded:
			b.Change = ChangeAdded
		case overDel:
			b.Change = ChangeRemoved
		default:
			b.Change = ChangeContext
		}
		if b.Change == ChangeAdded {
			// Find the Added line that overlaps this block to attach segments and OldText.
			// Reuses the same refine segments as regular code diff (diffparse/refine.go) so
			// pretty highlights exactly what changed, not the whole line.
			for _, h := range file.Hunks {
				delta := h.NewStart - h.OldStart
				for _, l := range h.Lines {
					if l.Kind != diffparse.Added {
						continue
					}
					if l.NewNum < b.StartLine || l.NewNum > b.EndLine {
						continue
					}
					b.Segments = l.Segments
					// Find paired Removed in same hunk at same mapped position
					for _, r := range h.Lines {
						if r.Kind != diffparse.Removed {
							continue
						}
						if r.OldNum+delta == l.NewNum {
							b.OldText = r.Text
							b.OldSegs = r.Segments
							break
						}
					}
					if b.OldText == "" {
						// Fallback: first Removed in hunk if hunk is a modify (1 del 1 add)
						for _, r := range h.Lines {
							if r.Kind == diffparse.Removed {
								b.OldText = r.Text
								b.OldSegs = r.Segments
								break
							}
						}
					}
					// Word-level hot for pretty, like code but on rendered text (not raw byte offsets)
					if b.OldText != "" {
						// Use pretty texts for word-level diff (user requested words not characters)
						newPretty := inlinesText(b.Inlines)
						oldHot, newHot := prettyHotForPretty(b.OldText, newPretty)
						b.OldHot = oldHot
						b.Hot = newHot
						// Fallback to raw segments if pretty diff found nothing (e.g. large)
						if b.Hot == nil && b.Segments != nil {
							b.Hot = prettyHotForText(newPretty, b.Segments)
						}
						if b.OldHot == nil && b.OldSegs != nil {
							b.OldHot = prettyHotForText(b.OldText, b.OldSegs)
						}
					} else if b.Segments != nil {
						newPretty := inlinesText(b.Inlines)
						b.Hot = prettyHotForText(newPretty, b.Segments)
					}
					break
				}
				if b.Segments != nil || b.OldText != "" {
					break
				}
			}
		}
	}
}

// prettyHotForPretty computes word-level hot for pretty texts, like refine but on rendered text.
// Returns per-rune hot maps, expanded to whole words for readability (user requested words not characters).
func prettyHotForPretty(oldText, newText string) (oldHot, newHot []bool) {
	if oldText == "" || newText == "" {
		return nil, nil
	}
	if len(oldText) > 4000 || len(newText) > 4000 {
		return nil, nil
	}
	oldToks := prettyTokenize(oldText)
	newToks := prettyTokenize(newText)
	if len(oldToks) > 800 || len(newToks) > 800 {
		return nil, nil
	}
	oldChanged, newChanged := prettyTokenDiff(oldText, oldToks, newText, newToks)
	// Expand to whole words for readability
	oldHot = prettySegmentsToHot(oldText, oldToks, oldChanged)
	newHot = prettySegmentsToHot(newText, newToks, newChanged)
	return oldHot, newHot
}

func prettyTokenize(s string) []diffparseToken {
	var toks []diffparseToken
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		start := i
		switch {
		case isSpaceRune(r):
			for i < len(s) {
				r, size = utf8.DecodeRuneInString(s[i:])
				if !isSpaceRune(r) {
					break
				}
				i += size
			}
			toks = append(toks, diffparseToken{start, i, true})
		case isWordRune(r):
			for i < len(s) {
				r, size = utf8.DecodeRuneInString(s[i:])
				if !isWordRune(r) {
					break
				}
				i += size
			}
			toks = append(toks, diffparseToken{start, i, false})
		default:
			i += size
			toks = append(toks, diffparseToken{start, i, false})
		}
	}
	return toks
}

type diffparseToken struct{ start, end int; space bool }

func isSpaceRune(r rune) bool { return r == ' ' || r == '\t' || r == '\r' }
func isWordRune(r rune) bool {
	return r == '_' || r >= 128 || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
}

func prettyTokenDiff(oldStr string, oldToks []diffparseToken, newStr string, newToks []diffparseToken) (oldChanged, newChanged []bool) {
	n, m := len(oldToks), len(newToks)
	oldChanged = make([]bool, n)
	newChanged = make([]bool, m)
	lo := 0
	for lo < n && lo < m && oldStr[oldToks[lo].start:oldToks[lo].end] == newStr[newToks[lo].start:newToks[lo].end] {
		lo++
	}
	hiOld, hiNew := n, m
	for hiOld > lo && hiNew > lo && oldStr[oldToks[hiOld-1].start:oldToks[hiOld-1].end] == newStr[newToks[hiNew-1].start:newToks[hiNew-1].end] {
		hiOld--
		hiNew--
	}
	a, b := oldToks[lo:hiOld], newToks[lo:hiNew]
	rows, cols := len(a)+1, len(b)+1
	lcs := make([]int, rows*cols)
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			if oldStr[a[i].start:a[i].end] == newStr[b[j].start:b[j].end] {
				lcs[i*cols+j] = lcs[(i+1)*cols+j+1] + 1
			} else if lcs[(i+1)*cols+j] >= lcs[i*cols+j+1] {
				lcs[i*cols+j] = lcs[(i+1)*cols+j]
			} else {
				lcs[i*cols+j] = lcs[i*cols+j+1]
			}
		}
	}
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		switch {
		case oldStr[a[i].start:a[i].end] == newStr[b[j].start:b[j].end]:
			i++
			j++
		case lcs[(i+1)*cols+j] >= lcs[i*cols+j+1]:
			oldChanged[lo+i] = true
			i++
		default:
			newChanged[lo+j] = true
			j++
		}
	}
	for ; i < len(a); i++ {
		oldChanged[lo+i] = true
	}
	for ; j < len(b); j++ {
		newChanged[lo+j] = true
	}
	return oldChanged, newChanged
}

func prettySegmentsToHot(text string, toks []diffparseToken, changed []bool) []bool {
	runes := []rune(text)
	hot := make([]bool, len(runes))
	// Map byte offsets to rune indices
	byteToRune := make([]int, len(text)+1)
	ri := 0
	for bi := 0; bi < len(text); {
		byteToRune[bi] = ri
		_, size := utf8.DecodeRuneInString(text[bi:])
		bi += size
		ri++
	}
	byteToRune[len(text)] = len(runes)
	for i, tok := range toks {
		if !changed[i] || tok.space {
			continue
		}
		// Expand to whole word: find word boundaries
		startRune := byteToRune[tok.start]
		endRune := byteToRune[tok.end]
		// Expand to include whole word if token is part of a word (already is word)
		for j := startRune; j < endRune && j < len(hot); j++ {
			hot[j] = true
		}
	}
	// If nothing hot, return nil
	for _, h := range hot {
		if h {
			return hot
		}
	}
	return nil
}

type prettyInterval struct{ s, e int } // [s,e) 1-indexed

func overlapsAny(s, e int, intervals []prettyInterval) bool {
	// s/e inclusive
	// intervals are [s,e) exclusive end, so convert block to [s, e+1)
	bS := s
	bE := e + 1
	for _, iv := range intervals {
		if bS < iv.e && bE > iv.s {
			return true
		}
	}
	return false
}

// Placeholder for asciiDoc rendering; defined in pretty_adoc.go
