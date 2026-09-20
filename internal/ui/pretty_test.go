package ui

import (
	"image"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/chromafish/check/internal/diffparse"
	"github.com/chromafish/check/internal/state"
	"github.com/chromafish/check/internal/vcs"
	"github.com/chromafish/check/reef"
)

func TestIsDocUsesOnlyTheCaseInsensitiveExtension(t *testing.T) {
	for _, path := range []string{"README.md", "guide.ADOC", "nested/book.AsciiDoc"} {
		if !IsDoc(path) {
			t.Errorf("IsDoc(%q) = false", path)
		}
	}
	for _, path := range []string{"README", "notes.markdown", "md/example.go"} {
		if IsDoc(path) {
			t.Errorf("IsDoc(%q) = true", path)
		}
	}
}

func TestDocumentWaitsForPrettyInsteadOfShowingRawHunks(t *testing.T) {
	parsed := diffparse.ParseOne("diff --git a/guide.md b/guide.md\n" +
		"--- a/guide.md\n+++ b/guide.md\n@@ -1 +1 @@\n-before\n+after\n")
	doc := buildDoc([]FileRow{{FileChange: vcs.FileChange{Path: "guide.md"}}}, parsed)
	fd := doc.Files[0]
	if got := fd.base[len(fd.base)-1]; got.Kind != rowNote || got.Text != "RENDERING DOCUMENT…" {
		t.Fatalf("pending document ends with %#v, want the rendering placeholder", got)
	}
	for _, row := range fd.base {
		if row.Kind == rowLine || row.Kind == rowHunk {
			t.Fatalf("pending document exposed raw diff row %#v", row)
		}
	}

	// A failed or unavailable pretty parse still has a useful fallback once
	// the file has been read.
	fd.read = true
	buildFile(fd, 0)
	if got := fd.base[len(fd.base)-1]; got.Kind == rowNote && got.Text == "RENDERING DOCUMENT…" {
		t.Fatal("read document remained stuck on the rendering placeholder")
	}
}

func TestOffscreenDocumentIsPrettyRenderedBeforeSelection(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	run("init", "-b", "main")
	write("a.go", "package example\n\nconst Name = \"before\"\n")
	write("z-guide.md", "# Guide\n\nBefore.\n")
	run("add", ".")
	run("-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "base")
	write("a.go", "package example\n\nconst Name = \"after\"\n")
	write("z-guide.md", "# Guide\n\nAfter, with **style**.\n\n```go\nconst ready = true\n```\n")

	h := openHarness(t, dir)
	if h.app.diff == nil {
		t.Fatal("diff did not load")
	}
	var docIndex = -1
	for i, fd := range h.app.diff.Files {
		if fd.Path == "z-guide.md" {
			docIndex = i
			break
		}
	}
	if docIndex < 0 {
		t.Fatal("document is missing from the diff")
	}
	if docIndex == h.app.fileSel {
		t.Fatalf("document is selected at index %d; test needs it offscreen", docIndex)
	}
	fd := h.app.diff.Files[docIndex]
	if !fd.read || fd.Pretty == nil {
		t.Fatalf("offscreen document was not pre-rendered: read=%v pretty=%v", fd.read, fd.Pretty != nil)
	}
	for _, row := range fd.base {
		if row.Kind == rowPretty {
			return
		}
	}
	t.Fatal("offscreen document has no pretty rows")
}

func TestMarkdownPrettyDocPreservesStructureAndInlineStyles(t *testing.T) {
	src := []byte("---\ntitle: Example\nstatus: draft\n---\n" +
		"# Pretty docs\n\n" +
		"Plain **bold**, *italic*, `code`, and [linked](https://example.com) text\n" +
		"continues on the next source line.\n\n" +
		"| Name | Meaning |\n| --- | --- |\n| `PORT` | Listener port |\n\n" +
		"```go\npackage main\nfunc main() {}\n```\n")
	doc, err := buildPrettyDoc("README.md", src)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := len(doc.Frontmatter), 2; got != want {
		t.Fatalf("frontmatter entries = %d, want %d", got, want)
	}
	if doc.Frontmatter[0].Key != "title" || doc.Frontmatter[1].Key != "status" {
		t.Fatalf("frontmatter order = %#v", doc.Frontmatter)
	}

	var para, table, code *PrettyBlock
	for i := range doc.Blocks {
		switch doc.Blocks[i].Kind {
		case BlockPara:
			if para == nil {
				para = &doc.Blocks[i]
			}
		case BlockTable:
			table = &doc.Blocks[i]
		case BlockCode:
			code = &doc.Blocks[i]
		}
	}
	if para == nil || table == nil || code == nil {
		t.Fatalf("missing parsed block: para=%v table=%v code=%v", para != nil, table != nil, code != nil)
	}
	text := inlinesText(para.Inlines)
	if !strings.Contains(text, "Plain bold, italic, code, and linked text continues") {
		t.Fatalf("paragraph text lost boundaries: %q", text)
	}
	var bold, italic, inlineCode, link bool
	for _, in := range para.Inlines {
		bold = bold || in.Bold
		italic = italic || in.Italic
		inlineCode = inlineCode || in.Code
		link = link || in.Link != ""
	}
	if !bold || !italic || !inlineCode || !link {
		t.Fatalf("inline styles: bold=%v italic=%v code=%v link=%v", bold, italic, inlineCode, link)
	}
	if len(table.TableHead) != 2 || len(table.TableRows) != 1 {
		t.Fatalf("table shape = %d header cells, %d rows", len(table.TableHead), len(table.TableRows))
	}
	if code.Lang != "go" || strings.Join(prettyCodeLines(code.Code), "|") != "package main|func main() {}" {
		t.Fatalf("code block = lang %q, body %q", code.Lang, code.Code)
	}
	if len(code.Spans) == 0 {
		t.Fatal("go code block was not syntax highlighted")
	}
}

func TestInvalidFrontmatterRemainsDocumentContent(t *testing.T) {
	src := []byte("---\nbroken: [\n---\n\nBody\n")
	entries, body, ok := parseFrontmatter("README.md", src)
	if ok || entries != nil || string(body) != string(src) {
		t.Fatalf("invalid frontmatter was stripped: ok=%v entries=%v body=%q", ok, entries, body)
	}
}

func TestPrettyInlineWrappingKeepsRunBoundariesWithoutAddingSpaces(t *testing.T) {
	inlines := []Inline{
		{Text: "call "},
		{Text: "thing", Code: true},
		{Text: ". then "},
		{Text: "follow", Link: "https://example.com"},
	}
	lines := wrapPrettyInlines(inlines, 12)
	var got strings.Builder
	var sawCode, sawLink bool
	for i, line := range lines {
		if i > 0 {
			got.WriteByte('|')
		}
		for _, c := range line {
			got.WriteRune(c.r)
			sawCode = sawCode || c.style.Code
			sawLink = sawLink || c.style.Link != ""
		}
	}
	if got.String() != "call thing.|then follow" {
		t.Fatalf("wrapped text = %q", got.String())
	}
	if !sawCode || !sawLink {
		t.Fatalf("wrapped styles: code=%v link=%v", sawCode, sawLink)
	}
}

func TestPrettyContentWidthFollowsTheDiffPane(t *testing.T) {
	a, gtx := prettyTestApp()
	for _, paneWidth := range []int{420, 900, 1600} {
		x, width := a.prettyContentBounds(gtx, paneWidth)
		want := max(1, paneWidth-x-gtx.Dp(reef.PadCard))
		if width != want {
			t.Errorf("%dpx pane gives %dpx of pretty content, want %dpx", paneWidth, width, want)
		}
	}
}

func TestPrettyTableMetricsGiveEveryRowItsOwnHeight(t *testing.T) {
	a, gtx := prettyTestApp()
	b := &PrettyBlock{
		Kind:      BlockTable,
		TableHead: [][]Inline{{{Text: "Variable"}}, {{Text: "Meaning"}}},
		TableRows: [][][]Inline{
			{{{Text: "PORT"}}, {{Text: "A short value"}}},
			{{{Text: "DATABASE_URL"}}, {{Text: strings.Repeat("wrapped description ", 8)}}},
		},
	}
	metrics := a.prettyTableMetrics(gtx, b, 360)
	if len(metrics.heights) != 3 {
		t.Fatalf("table row heights = %v", metrics.heights)
	}
	if metrics.heights[2] <= metrics.heights[1] {
		t.Fatalf("wrapped row height %d did not exceed short row %d", metrics.heights[2], metrics.heights[1])
	}
	width := 0
	for _, w := range metrics.widths {
		width += w
	}
	if width != 360 {
		t.Fatalf("column widths total %d, want 360", width)
	}
	height := 0
	for _, h := range metrics.heights {
		height += h
	}
	if height != metrics.total {
		t.Fatalf("row heights total %d, metrics total %d", height, metrics.total)
	}
}

func TestPrettyCodeHeightCountsSourceAndWrappedLines(t *testing.T) {
	a, gtx := prettyTestApp()
	b := &PrettyBlock{Kind: BlockCode, Lang: "sh", Code: "one\ntwo\nthree\n"}
	lh := a.ui.CodeRow(gtx)
	header := gtx.Dp(reef.ControlHSm)
	pad := 2 * gtx.Dp(reef.Sp3)
	if got, want := a.prettyCodeHeight(gtx, b, 600), header+pad+3*lh; got != want {
		t.Fatalf("code height = %d, want %d", got, want)
	}
	a.noWrap = true
	if got, want := a.prettyCodeHeight(gtx, b, 80), header+pad+3*lh; got != want {
		t.Fatalf("unwrapped code height = %d, want %d", got, want)
	}
}

func prettyTestApp() (*App, layout.Context) {
	var ops op.Ops
	return &App{ui: reef.New()}, layout.Context{
		Ops:         &ops,
		Metric:      unit.Metric{PxPerDp: 1, PxPerSp: 1},
		Constraints: layout.Exact(image.Pt(800, 600)),
	}
}

func TestNoteCanBeStartedOnAPrettyBlock(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	run("init", "-b", "main")
	write("guide.md", "# Guide\n\nBefore.\n")
	run("add", ".")
	run("-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "base")
	write("guide.md", "# Guide\n\nAfter, and\nwrapped.\n")

	h := openHarness(t, dir)
	doc := h.app.diff
	if doc == nil {
		t.Fatal("diff did not load")
	}
	at := -1
	for i := range doc.Rows {
		if r := doc.Row(i); r.Kind == rowPretty && r.Pretty != nil && r.Pretty.Kind == BlockPara {
			at = i
			break
		}
	}
	if at < 0 {
		t.Fatal("document has no pretty paragraph row")
	}
	doc.Cursor = at
	h.app.startComment()

	d := h.app.draft
	if d == nil {
		t.Fatal("no draft was opened on the pretty block")
	}
	if d.path != "guide.md" || d.side != state.SideNew || d.line != 3 || d.endLine != 4 {
		t.Fatalf("draft = %s %s %d-%d, want guide.md new 3-4", d.path, d.side, d.line, d.endLine)
	}
	for i := range h.app.diff.Rows {
		if h.app.diff.Row(i).Kind == rowDraft {
			return
		}
	}
	t.Fatal("draft row is not shown under the pretty block")
}
