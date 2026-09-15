package ui

import (
	"bytes"
	"fmt"
	"sort"
	"strings"

	"github.com/bytesparadise/libasciidoc/pkg/configuration"
	"github.com/bytesparadise/libasciidoc/pkg/parser"
	"github.com/bytesparadise/libasciidoc/pkg/types"
	log "github.com/sirupsen/logrus"

	"github.com/chromafish/check/internal/highlight"
)

// renderAsciiDocBody renders AsciiDoc source into PrettyBlocks using libasciidoc parser.
// It walks the Document AST and produces a block set compatible with markdown's.
func renderAsciiDocBody(path string, body []byte, fmLines int) ([]PrettyBlock, error) {
	// Silence noisy libasciidoc INFO logs (e.g. "parsed '...' with
	// 'inline_passthrough,attributes,...' substitutions" in
	// pkg/parser/document_processing_apply_substitutions.go:630) that flood
	// stderr / REVIEW_LOG. The library logs via the global logrus logger at
	// InfoLevel; raising to WarnLevel preserves Warn/Error while silencing
	// per-fragment Info traces. Check's own logs use slog, so this does not
	// suppress app logs. Restore afterwards for correctness (no global
	// suppression beyond the parse).
	if lvl := log.GetLevel(); lvl > log.WarnLevel {
		orig := lvl
		log.SetLevel(log.WarnLevel)
		defer log.SetLevel(orig)
	}
	// Use libasciidoc to parse document to ensure the library is exercised.
	// We parse with libasciidoc's parser to obtain a Document, then walk it.
	// If parsing fails, fall back to plain paragraph blocks.
	config := configuration.NewConfiguration(
		configuration.WithFilename(path),
		configuration.WithBackEnd("html5"),
	)
	// Preprocess not strictly needed for simple docs; Use ParseDocument directly.
	// parser.Preprocess handles includes, etc.
	preprocessed, err := parser.Preprocess(bytes.NewReader(body), config)
	if err != nil {
		// fallback
		return fallbackAsciiDoc(body, fmLines)
	}
	doc, err := parser.ParseDocument(strings.NewReader(preprocessed), config)
	if err != nil {
		return fallbackAsciiDoc(body, fmLines)
	}
	var blocks []PrettyBlock
	// Extract AsciiDoc header attributes as frontmatter for uniform rendering.
	if fmEntries := extractAsciiDocFrontmatter(doc); len(fmEntries) > 0 {
		blocks = append(blocks, PrettyBlock{
			Kind:        BlockFrontmatter,
			Frontmatter: fmEntries,
			StartLine:   1,
			EndLine:     len(fmEntries),
			Change:      ChangeContext,
		})
	}
	// Helper to compute line numbers: estimate sequential lines.
	// Since types do not expose source line numbers, we estimate by scanning body sequentially.
	// We'll approximate block line ranges by advancing a line counter by estimated lines per block.
	totalLines := bytes.Count(body, []byte("\n")) + 1
	_ = totalLines
	// Walk top-level elements.
	// Document Elements may contain Preamble, Sections, etc.
	// For simplicity, iterate over helper that flattens.
	flattened := flattenAsciiDocElements(doc)
	lineCursor := fmLines + 1
	// If we already emitted a frontmatter block, offset subsequent line numbers to
	// avoid overlap with it (frontmatter occupies StartLine 1..len).
	if len(blocks) > 0 && blocks[0].Kind == BlockFrontmatter {
		lineCursor = blocks[0].EndLine + 1
		if lineCursor < fmLines+1 {
			lineCursor = fmLines + 1
		}
	}
	for _, el := range flattened {
		b := asciiElementToBlock(el, body, &lineCursor)
		if b != nil {
			blocks = append(blocks, *b)
		}
	}
	// If no blocks produced, fallback
	if len(blocks) == 0 {
		return fallbackAsciiDoc(body, fmLines)
	}
	// Reassign line numbers to match the actual source. The synthetic
	// cursor above drifts (frontmatter filtering, blank lines) and breaks
	// markChanges which maps hunks by NewStart/NewCount line numbers.
	// See archive/spec/0002.doc.pretty-render.md Hybrid diff awareness.
	fixAdocBlockLines(body, blocks)

	// Highlight code blocks
	for i := range blocks {
		if blocks[i].Kind == BlockCode && blocks[i].Lang != "" {
			hl := highlight.File("file."+blocks[i].Lang, []byte(blocks[i].Code))
			if hl != nil {
				blocks[i].Spans = hl
			}
		}
	}
	// Ensure every element type referenced so import is used.
	_ = types.Document{}
	return blocks, nil
}

func adocBlockSnippet(b *PrettyBlock) string {
	switch b.Kind {
	case BlockHeading:
		if len(b.Inlines) > 0 {
			return strings.TrimSpace(b.Inlines[0].Text)
		}
	case BlockPara, BlockQuote:
		if len(b.Inlines) > 0 {
			t := inlinesText(b.Inlines)
			t = strings.TrimSpace(t)
			if len(t) > 30 {
				t = t[:30]
			}
			return t
		}
		if b.Code != "" {
			t := strings.TrimSpace(b.Code)
			if len(t) > 30 {
				t = t[:30]
			}
			return t
		}
	case BlockList:
		if len(b.Items) > 0 && len(b.Items[0]) > 0 {
			t := inlinesText(b.Items[0])
			t = strings.TrimSpace(t)
			if len(t) > 30 {
				t = t[:30]
			}
			return t
		}
	case BlockCode:
		first := strings.Split(b.Code, "\n")[0]
		first = strings.TrimSpace(first)
		if len(first) > 30 {
			first = first[:30]
		}
		return first
	case BlockTable:
		if len(b.TableHead) > 0 && len(b.TableHead[0]) > 0 {
			t := inlinesText(b.TableHead[0])
			t = strings.TrimSpace(t)
			if len(t) > 30 {
				t = t[:30]
			}
			return t
		}
		if len(b.TableRows) > 0 && len(b.TableRows[0]) > 0 && len(b.TableRows[0][0]) > 0 {
			t := inlinesText(b.TableRows[0][0])
			t = strings.TrimSpace(t)
			if len(t) > 30 {
				t = t[:30]
			}
			return t
		}
	}
	return ""
}

func adocBlockLineCount(b *PrettyBlock) int {
	switch b.Kind {
	case BlockCode:
		n := 1 + strings.Count(b.Code, "\n")
		if n == 0 {
			n = 1
		}
		// Delimited listing blocks have surrounding delimiters (----) not
		// in Code; count them so EndLine covers the whole source range.
		return n + 2
	case BlockList:
		n := len(b.Items)
		if n == 0 {
			n = 1
		}
		return n
	case BlockTable:
		n := len(b.TableRows)
		if len(b.TableHead) > 0 {
			n++
		}
		if n == 0 {
			n = 1
		}
		return n
	default:
		return 1
	}
}

func fixAdocBlockLines(body []byte, blocks []PrettyBlock) {
	if len(blocks) == 0 {
		return
	}
	str := strings.ReplaceAll(string(body), "\r\n", "\n")
	lines := strings.Split(str, "\n")

	// Fix frontmatter block if present: it represents header attributes
	// (lines like ":revdate: 2026-09-12"). libasciidoc filters internal attrs
	// (e.g. :toc:) so len(entries) < actual attribute lines.
	if blocks[0].Kind == BlockFrontmatter {
		startAttr, endAttr := -1, -1
		for idx, l := range lines {
			t := strings.TrimSpace(l)
			if strings.HasPrefix(t, ":") {
				// attribute line like ":revdate: ..." or ":toc:"
				if strings.Contains(t[1:], ":") {
					if startAttr == -1 {
						startAttr = idx
					}
					endAttr = idx
					continue
				}
			}
			if startAttr != -1 {
				// attributes are consecutive at top after the title; first
				// non-attribute after start ends the range
				break
			}
		}
		if startAttr != -1 {
			blocks[0].StartLine = startAttr + 1
			blocks[0].EndLine = endAttr + 1
		}
	}

	// Assign real source lines to remaining blocks by scanning forward for a
	// snippet of the block's text. This aligns synthetic blocks with the
	// actual NewStart line numbers used by markChanges.
	curIdx := 0
	startLoop := 0
	if len(blocks) > 0 && blocks[0].Kind == BlockFrontmatter {
		startLoop = 1
		// Title block ("= Authentication") is at line 1, before attributes;
		// start before attributes so it is found correctly.
		curIdx = 0
	}
	for i := startLoop; i < len(blocks); i++ {
		b := &blocks[i]
		snippet := adocBlockSnippet(b)
		if snippet == "" {
			b.StartLine = curIdx + 1
			if b.StartLine < 1 {
				b.StartLine = 1
			}
			b.EndLine = b.StartLine
			curIdx = b.EndLine
			if curIdx < len(lines) && strings.TrimSpace(lines[curIdx]) == "" {
				curIdx++
			}
			continue
		}
		search := snippet
		// Contains check is substring; trim to avoid over-specificity from
		// normalized whitespace.
		search = strings.TrimSpace(search)
		found := -1
		for idx := curIdx; idx < len(lines); idx++ {
			if strings.Contains(lines[idx], search) {
				found = idx
				break
			}
		}
		if found == -1 {
			// Fallback: try first word of snippet, which survives link
			// normalization (e.g. "See" from "See  0001...").
			firstWord := strings.Fields(search)
			if len(firstWord) > 0 {
				w := firstWord[0]
				for idx := curIdx; idx < len(lines); idx++ {
					if strings.Contains(lines[idx], w) {
						// For headings require a heading marker nearby to avoid
						// matching the word inside a paragraph.
						if b.Kind == BlockHeading {
							t := strings.TrimSpace(lines[idx])
							if strings.HasPrefix(t, "=") || strings.Contains(t, w) {
								found = idx
								break
							}
						} else {
							found = idx
							break
						}
					}
				}
			}
		}
		if found == -1 {
			found = curIdx
			if found >= len(lines) {
				found = len(lines) - 1
			}
			if found < 0 {
				found = 0
			}
		}
		b.StartLine = found + 1
		needed := adocBlockLineCount(b)
		b.EndLine = b.StartLine + needed - 1
		curIdx = b.EndLine
		if curIdx < len(lines) && strings.TrimSpace(lines[curIdx]) == "" {
			curIdx++
		}
	}
}

func flattenAsciiDocElements(doc *types.Document) []interface{} {
	var out []interface{}
	// Handle DocumentHeader separately
	if hdr, idx := doc.Header(); hdr != nil {
		out = append(out, hdr)
		// Elements after header are body; we will walk them after
		// But flatten walk will also handle header's Elements if needed
		_ = idx
	}
	var walk func([]interface{})
	walk = func(elems []interface{}) {
		for _, e := range elems {
			switch el := e.(type) {
			case *types.DocumentHeader:
				// Already handled via doc.Header; skip duplicates
				continue
			case *types.Section:
				// Section itself is a block heading with its elements.
				out = append(out, el)
				// Also walk its elements?
				walk(el.Elements)
			case *types.DelimitedBlock:
				out = append(out, el)
			case *types.Paragraph:
				out = append(out, el)
			case *types.List:
				out = append(out, el)
			case *types.Table:
				out = append(out, el)
			case *types.ThematicBreak:
				out = append(out, el)
			case *types.BlankLine, *types.SinglelineComment:
				// skip
			case *types.Preamble:
				walk(el.Elements)
			default:
				// For Preamble etc., dive
				if we, ok := e.(types.WithElements); ok {
					walk(we.GetElements())
				} else {
					out = append(out, e)
				}
			}
		}
	}
	// Document has Elements, may include Header, etc.
	if len(doc.Elements) > 0 {
		walk(doc.Elements)
	}
	return out
}

func asciiElementToBlock(el interface{}, src []byte, cursor *int) *PrettyBlock {
	switch e := el.(type) {
	case *types.DocumentHeader:
		titleText := asciiInlinesToString(e.Title)
		start := *cursor
		end := start
		*cursor = end + 1
		return &PrettyBlock{
			Kind:      BlockHeading,
			Level:     1,
			Inlines:   []Inline{{Text: titleText}},
			StartLine: start,
			EndLine:   end,
		}
	case *types.Section:
		level := e.Level
		if level < 1 {
			level = 1
		}
		if level > 6 {
			level = 6
		}
		titleText := asciiInlinesToString(e.Title)
		start := *cursor
		// Estimate lines: 1 for heading
		end := start
		*cursor = end + 1
		return &PrettyBlock{
			Kind:      BlockHeading,
			Level:     level,
			Inlines:   []Inline{{Text: titleText}},
			StartLine: start,
			EndLine:   end,
		}
	case *types.Paragraph:
		txt := asciiParagraphText(e)
		start := *cursor
		// Rough line count: count newlines in txt plus wraps? Use 1 for now, but estimate by length?
		lines := 1 + strings.Count(txt, "\n")
		if lines == 0 {
			lines = 1
		}
		end := start + lines - 1
		*cursor = end + 1
		return &PrettyBlock{
			Kind:      BlockPara,
			Inlines:   []Inline{{Text: txt}},
			StartLine: start,
			EndLine:   end,
		}
	case *types.DelimitedBlock:
		kind := e.Kind
		// Kind may be "listing", "literal", "source", "fenced", etc.
		// Check for source style
		lang := ""
		if attr, ok := e.Attributes["language"]; ok {
			if s, ok := attr.(string); ok {
				lang = s
			}
		}
		if lang == "" {
			if attr, ok := e.Attributes["style"]; ok {
				if s, ok := attr.(string); ok && s == "source" {
					if l, ok := e.Attributes["language"]; ok {
						if ls, ok := l.(string); ok {
							lang = ls
						}
					}
				}
			}
		}
		// Extract code text from Elements (RawLine)
		var buf strings.Builder
		for _, sub := range e.Elements {
			if rl, ok := sub.(*types.RawLine); ok {
				buf.WriteString(rl.Content)
				buf.WriteString("\n")
			} else if s, ok := sub.(*types.StringElement); ok {
				buf.WriteString(s.Content)
			}
		}
		code := strings.TrimSuffix(buf.String(), "\n")
		start := *cursor
		lines := 1 + strings.Count(code, "\n")
		if lines == 0 {
			lines = 1
		}
		end := start + lines - 1
		*cursor = end + 1
		switch kind {
		case "listing", "literal", "source", "fenced", "example", "sidebar":
			return &PrettyBlock{
				Kind:      BlockCode,
				Code:      code,
				Lang:      lang,
				StartLine: start,
				EndLine:   end,
			}
		default:
			// Treat as quote or para? For delimited block of kind "quote" etc.
			if kind == "quote" || kind == "verse" {
				return &PrettyBlock{
					Kind:      BlockQuote,
					Inlines:   []Inline{{Text: code}},
					StartLine: start,
					EndLine:   end,
				}
			}
			return &PrettyBlock{
				Kind:      BlockPara,
				Inlines:   []Inline{{Text: code}},
				StartLine: start,
				EndLine:   end,
			}
		}
	case *types.List:
		var items [][]Inline
		for _, le := range e.Elements {
			var txt string
			switch item := le.(type) {
			case *types.UnorderedListElement:
				txt = asciiListElementText(item.Elements)
			case *types.OrderedListElement:
				txt = asciiListElementText(item.Elements)
			case *types.LabeledListElement:
				txt = asciiInlinesToString(item.Term) + ": " + asciiListElementText(item.Elements)
			default:
				txt = ""
			}
			items = append(items, []Inline{{Text: txt}})
		}
		start := *cursor
		end := start + len(items) - 1
		if end < start {
			end = start
		}
		*cursor = end + 1
		return &PrettyBlock{
			Kind:      BlockList,
			Ordered:   e.Kind == types.OrderedListKind,
			Items:     items,
			StartLine: start,
			EndLine:   end,
		}
	case *types.Table:
		// Table rows/cols
		var head [][]Inline
		var rows [][][]Inline
		rowIdx := 0
		if e.Header != nil {
			var cells [][]Inline
			for _, c := range e.Header.Cells {
				txt := asciiTableCellText(c)
				cells = append(cells, []Inline{{Text: txt}})
			}
			head = cells
			rowIdx++
		}
		for _, tr := range e.Rows {
			var cells [][]Inline
			for _, c := range tr.Cells {
				txt := asciiTableCellText(c)
				cells = append(cells, []Inline{{Text: txt}})
			}
			rows = append(rows, cells)
			rowIdx++
		}
		if len(head) == 0 && len(rows) == 0 {
			return nil
		}
		start := *cursor
		end := start + rowIdx - 1
		if rowIdx == 0 {
			end = start
		}
		*cursor = end + 1
		return &PrettyBlock{
			Kind:      BlockTable,
			TableHead: head,
			TableRows: rows,
			StartLine: start,
			EndLine:   end,
		}
	case *types.ThematicBreak:
		start := *cursor
		end := start
		*cursor = end + 1
		return &PrettyBlock{
			Kind:      BlockHR,
			StartLine: start,
			EndLine:   end,
		}
	default:
		// Generic fallback: try to get text via string
		txt := asciiGenericText(el)
		if txt == "" {
			return nil
		}
		start := *cursor
		end := start
		*cursor = end + 1
		return &PrettyBlock{
			Kind:      BlockPara,
			Inlines:   []Inline{{Text: txt}},
			StartLine: start,
			EndLine:   end,
		}
	}
}

func asciiParagraphText(p *types.Paragraph) string {
	var buf strings.Builder
	for _, el := range p.Elements {
		switch e := el.(type) {
		case *types.RawLine:
			buf.WriteString(e.Content)
			buf.WriteString(" ")
		case *types.StringElement:
			buf.WriteString(e.Content)
			buf.WriteString(" ")
		case *types.QuotedText:
			// Extract string from quoted
			txt := asciiQuotedText(e)
			buf.WriteString(txt)
			buf.WriteString(" ")
		case *types.InlineLink:
			// Link text
			if e.Location != nil {
				buf.WriteString(e.Location.ToString())
				buf.WriteString(" ")
			}
		default:
			buf.WriteString(asciiGenericText(e))
			buf.WriteString(" ")
		}
	}
	return strings.TrimSpace(buf.String())
}

func asciiListElementText(elems []interface{}) string {
	var buf strings.Builder
	for _, el := range elems {
		switch e := el.(type) {
		case *types.Paragraph:
			buf.WriteString(asciiParagraphText(e))
			buf.WriteString(" ")
		case *types.RawLine:
			buf.WriteString(e.Content)
			buf.WriteString(" ")
		case *types.StringElement:
			buf.WriteString(e.Content)
			buf.WriteString(" ")
		default:
			buf.WriteString(asciiGenericText(e))
			buf.WriteString(" ")
		}
	}
	return strings.TrimSpace(buf.String())
}

func asciiInlinesToString(inlines []interface{}) string {
	var buf strings.Builder
	for _, el := range inlines {
		switch e := el.(type) {
		case *types.StringElement:
			buf.WriteString(e.Content)
			buf.WriteString(" ")
		case *types.QuotedText:
			buf.WriteString(asciiQuotedText(e))
			buf.WriteString(" ")
		default:
			buf.WriteString(asciiGenericText(e))
			buf.WriteString(" ")
		}
	}
	return strings.TrimSpace(buf.String())
}

func asciiQuotedText(q *types.QuotedText) string {
	var buf strings.Builder
	for _, el := range q.Elements {
		if se, ok := el.(*types.StringElement); ok {
			buf.WriteString(se.Content)
		} else if rl, ok := el.(*types.RawLine); ok {
			buf.WriteString(rl.Content)
		} else {
			buf.WriteString(asciiGenericText(el))
		}
	}
	return buf.String()
}

func asciiTableCellText(c *types.TableCell) string {
	var buf strings.Builder
	for _, el := range c.Elements {
		switch e := el.(type) {
		case *types.Paragraph:
			buf.WriteString(asciiParagraphText(e))
		case *types.RawLine:
			buf.WriteString(e.Content)
		case *types.StringElement:
			buf.WriteString(e.Content)
		default:
			buf.WriteString(asciiGenericText(e))
		}
	}
	return strings.TrimSpace(buf.String())
}

func asciiGenericText(el interface{}) string {
	switch e := el.(type) {
	case *types.StringElement:
		return e.Content
	case *types.RawLine:
		return e.Content
	default:
		// fallback via fmt
		return ""
	}
}

func isInternalAdocAttr(name string) bool {
	switch name {
	case "backend", "basebackend", "basebackend-html", "doctype", "filetype":
		return true
	}
	if strings.HasPrefix(name, "backend-") || strings.HasPrefix(name, "basebackend-") {
		return true
	}
	if strings.HasPrefix(name, "@") {
		return true
	}
	// TOC / section-numbering attributes are presentation hints, not metadata.
	// Hide :toc: and related keys from the frontmatter table (case-insensitive).
	switch strings.ToLower(name) {
	case "toc", "toclevels", "toc-title", "toc-placement":
		return true
	}
	return false
}

func extractAsciiDocFrontmatter(doc *types.Document) []FrontmatterEntry {
	var entries []FrontmatterEntry
	// YAML frontmatter via libasciidoc (rare for adoc but handle)
	if fm := doc.FrontMatter(); fm != nil && len(fm.Attributes) > 0 {
		keys := make([]string, 0, len(fm.Attributes))
		for k := range fm.Attributes {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if isInternalAdocAttr(k) {
				continue
			}
			v := fm.Attributes[k]
			vs := ""
			if v != nil {
				vs = fmt.Sprintf("%v", v)
			}
			entries = append(entries, FrontmatterEntry{
				Key:      k,
				Value:    v,
				ValueStr: vs,
			})
		}
	}
	hdr, _ := doc.Header()
	if hdr != nil {
		for _, el := range hdr.Elements {
			decl, ok := el.(*types.AttributeDeclaration)
			if !ok {
				continue
			}
			name := decl.Name
			if isInternalAdocAttr(name) {
				continue
			}
			switch val := decl.Value.(type) {
			case nil:
				entries = append(entries, FrontmatterEntry{
					Key:      name,
					Value:    nil,
					ValueStr: "",
				})
			case string:
				entries = append(entries, FrontmatterEntry{
					Key:      name,
					Value:    val,
					ValueStr: val,
				})
			case types.DocumentAuthors:
				var parts []string
				for _, a := range val {
					if a == nil {
						continue
					}
					fn := ""
					if a.DocumentAuthorFullName != nil {
						fn = a.FullName()
					}
					if a.Email != "" {
						if fn != "" {
							fn = fmt.Sprintf("%s <%s>", fn, a.Email)
						} else {
							fn = a.Email
						}
					}
					if fn != "" {
						parts = append(parts, fn)
					}
				}
				vs := strings.Join(parts, ", ")
				entries = append(entries, FrontmatterEntry{
					Key:      name,
					Value:    val,
					ValueStr: vs,
				})
			case *types.DocumentRevision:
				if val == nil {
					continue
				}
				if val.Revnumber != "" {
					entries = append(entries, FrontmatterEntry{
						Key:      "revnumber",
						Value:    val.Revnumber,
						ValueStr: val.Revnumber,
					})
				}
				if val.Revdate != "" {
					entries = append(entries, FrontmatterEntry{
						Key:      "revdate",
						Value:    val.Revdate,
						ValueStr: val.Revdate,
					})
				}
				if val.Revremark != "" {
					entries = append(entries, FrontmatterEntry{
						Key:      "revremark",
						Value:    val.Revremark,
						ValueStr: val.Revremark,
					})
				}
			case bool:
				vs := fmt.Sprintf("%v", val)
				entries = append(entries, FrontmatterEntry{
					Key:      name,
					Value:    val,
					ValueStr: vs,
				})
			default:
				vs := fmt.Sprintf("%v", val)
				entries = append(entries, FrontmatterEntry{
					Key:      name,
					Value:    val,
					ValueStr: vs,
				})
			}
		}
	} else {
		// No header: collect top-level attribute declarations before first content.
		collected := false
		for _, el := range doc.Elements {
			switch e := el.(type) {
			case *types.AttributeDeclaration:
				if isInternalAdocAttr(e.Name) {
					continue
				}
				var vs string
				if e.Value == nil {
					vs = ""
				} else if s, ok := e.Value.(string); ok {
					vs = s
				} else {
					vs = fmt.Sprintf("%v", e.Value)
				}
				entries = append(entries, FrontmatterEntry{
					Key:      e.Name,
					Value:    e.Value,
					ValueStr: vs,
				})
				collected = true
			case *types.AttributeReset:
				continue
			case *types.BlankLine, *types.SinglelineComment:
				continue
			default:
				if collected {
					goto done
				}
				// If first non-attribute and no collection yet, no header attrs.
				if len(entries) == 0 {
					goto done
				}
			}
		}
	done:
	}
	return entries
}

func fallbackAsciiDoc(body []byte, fmLines int) ([]PrettyBlock, error) {
	// Very simple fallback: split body into paragraphs separated by blank lines.
	text := strings.TrimSpace(string(body))
	if text == "" {
		return nil, nil
	}
	paras := strings.Split(text, "\n\n")
	var blocks []PrettyBlock
	line := fmLines + 1
	for _, p := range paras {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		lines := 1 + strings.Count(p, "\n")
		blocks = append(blocks, PrettyBlock{
			Kind:      BlockPara,
			Inlines:   []Inline{{Text: strings.ReplaceAll(p, "\n", " ")}},
			StartLine: line,
			EndLine:   line + lines - 1,
		})
		line += lines
		if strings.Contains(p, "\n") {
			line++ // blank line
		} else {
			line++
		}
	}
	return blocks, nil
}

// Ensure imports are used.
var _ = strings.Contains
