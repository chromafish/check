package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"gioui.org/io/key"

	"github.com/chromafish/check/internal/diffparse"
	"github.com/chromafish/check/internal/jev"
	"github.com/chromafish/check/internal/state"
	"github.com/chromafish/check/reef"
)

// asked is one request the stub service received.
type asked struct {
	ids   []string
	state struct {
		Query string `json:"query"`
		Lines string `json:"lines"`
	}
	criteria map[string]string // of the choice question, whichever it was
}

// stubJev answers with the given reply for each request in turn, and records
// what it was asked. A reply is the raw answers object.
func stubJev(t *testing.T, replies ...string) (*jev.Client, *[]asked) {
	t.Helper()
	var log []asked
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var req struct {
			State     json.RawMessage `json:"state"`
			Questions map[string]struct {
				Type     string            `json:"type"`
				Criteria map[string]string `json:"criteria"`
			} `json:"questions"`
		}
		if err := json.Unmarshal(raw, &req); err != nil {
			t.Errorf("decode request: %v", err)
		}
		var a asked
		json.Unmarshal(req.State, &a.state)
		for id, q := range req.Questions {
			a.ids = append(a.ids, id)
			if q.Type == "choice" {
				a.criteria = q.Criteria
			}
		}
		n := len(log)
		log = append(log, a)
		if n >= len(replies) {
			t.Errorf("service asked %d times, only %d replies prepared", n+1, len(replies))
			w.WriteHeader(500)
			return
		}
		fmt.Fprintf(w, `{"answers":%s}`, replies[n])
	}))
	t.Cleanup(srv.Close)
	return jev.NewAt("k", srv.URL), &log
}

func has(ids []string, want string) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}

// The bar is one field in one of two modes, and moving between them does not
// carry the old text across: a string that was being matched is not a
// question, and the rows a question found are not matches of a string.
func TestMovingBetweenFindAndAskClearsTheField(t *testing.T) {
	h := newHarness(t)
	h.settle()
	h.app.jev = jev.NewAt("k", "http://example.invalid")
	h.app.focus = PaneDiff

	// Once the bar has the caret it is a text field, and shift-F types an F.
	// Switching modes is the two openers, which is what these stand for.
	h.app.startFind(h.gtx())
	h.app.findField.SetText("ctx")
	h.app.findHits, h.app.findAt = []int{3}, 0

	h.app.startAsk(h.gtx())
	if !h.app.askMode {
		t.Fatal("startAsk did not put the bar into asking")
	}
	if got := h.app.findField.Text(); got != "" {
		t.Errorf("field = %q, want the string it was matching cleared", got)
	}
	if len(h.app.findHits) != 0 {
		t.Errorf("hits = %v, want the old matches cleared", h.app.findHits)
	}

	h.app.findField.SetText("where is the budget enforced")
	h.app.startFind(h.gtx())
	if h.app.askMode {
		t.Error("startFind did not take the bar back to matching")
	}
	if got := h.app.findField.Text(); got != "" {
		t.Errorf("field = %q, want the question cleared", got)
	}
}

// Asking again with the bar already open keeps the question, so a query can
// be corrected and re-run rather than retyped.
func TestAskingTwiceKeepsTheQuestion(t *testing.T) {
	h := newHarness(t)
	h.settle()
	h.app.jev = jev.NewAt("k", "http://example.invalid")
	h.app.focus = PaneDiff

	h.app.startAsk(h.gtx())
	h.app.findField.SetText("where is the budget")
	h.app.startAsk(h.gtx())
	if got := h.app.findField.Text(); got != "where is the budget" {
		t.Errorf("field = %q, want the question left alone", got)
	}
}

// The ranking is by probability, and everything the model thought unlikely is
// dropped rather than offered as a match.
func TestSemRankOrdersByProbabilityAndDropsTheTail(t *testing.T) {
	probs := map[string]float64{"a": 0.1, "b": 0.6, "c": 0.01, "d": 0.29}
	got := semRank(probs, 0.05, 0)
	want := []string{"b", "d", "a"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("semRank = %v, want %v", got, want)
	}
	if got := semRank(probs, 0.05, 2); len(got) != 2 || got[0] != "b" {
		t.Errorf("capped semRank = %v, want the best two", got)
	}
}

// Only diff lines are candidates: a heading or a gap is not somewhere a
// cursor should land, and an id that names one would send the cursor there.
func TestCollectSemOffersEveryDiffLineAndNothingElse(t *testing.T) {
	_, doc := threeFileDoc(t)
	hunks := collectSem(doc)
	if len(hunks) != 3 {
		t.Fatalf("collected %d hunks, want 3", len(hunks))
	}
	n := 0
	for _, h := range hunks {
		if !strings.Contains(h.label, "@@") {
			t.Errorf("hunk label %q does not carry its header", h.label)
		}
		for _, c := range h.cands {
			if doc.Row(c.row).Kind != rowLine {
				t.Errorf("candidate %s points at a %v row", c.id, doc.Row(c.row).Kind)
			}
			n++
		}
	}
	if want := 9; n != want {
		t.Errorf("collected %d lines, want %d", n, want)
	}
}

// A change small enough to ask about whole is asked about whole: paying for a
// ranking stage that would rank three hunks is wasted latency.
func TestSmallChangeIsSearchedInOneRequest(t *testing.T) {
	a, doc := threeFileDoc(t)
	client, log := stubJev(t, `{"line":{"choice":"L0002","probabilities":{"L0002":0.8,"L0005":0.1}},"present":{"noul":0.95}}`)
	a.jev = client

	got, err := semSearch(context.Background(), a.jev, "where did new0 arrive", collectSem(doc))
	if err != nil {
		t.Fatalf("semSearch: %v", err)
	}
	if len(*log) != 1 {
		t.Fatalf("made %d requests, want 1", len(*log))
	}
	if !has((*log)[0].ids, "line") || !has((*log)[0].ids, "present") {
		t.Errorf("asked %v, want the line and presence questions together", (*log)[0].ids)
	}
	if len(got) != 2 || got[0].id != "L0002" {
		t.Fatalf("hits = %v, want L0002 first then L0005", got)
	}
	if doc.Row(got[0].row).Line.Text != "new0" {
		t.Errorf("L0002 resolved to %q, want the line the model chose", doc.Row(got[0].row).Line.Text)
	}
}

// bigDoc is a change past the request budget, so that the hunk stage has to
// run. Each file is one hunk of long lines.
func bigDoc(t *testing.T) (*App, *DiffDoc) {
	t.Helper()
	var b strings.Builder
	body := strings.Repeat("x", 120)
	for f := range 40 {
		fmt.Fprintf(&b, "diff --git a/f%02d.go b/f%02d.go\n--- a/f%02d.go\n+++ b/f%02d.go\n", f, f, f, f)
		fmt.Fprintf(&b, "@@ -1,6 +1,6 @@ func f%02d()\n", f)
		for l := range 6 {
			fmt.Fprintf(&b, "+%s%02d%02d\n", body, f, l)
		}
	}
	parsed := diffparse.Parse(b.String())
	rows := make([]FileRow, len(parsed))
	for i := range parsed {
		rows[i].Path = parsed[i].Path()
	}
	doc := buildDoc(rows, parsed)
	a := &App{store: state.New(), diff: doc}
	a.rev.ChangeIDFull = "change"
	a.rebuildRows()
	return a, doc
}

// Past the budget the hunks are ranked first, and only the winners' lines are
// sent — which is what keeps the cost flat as a change grows.
func TestLargeChangeRanksHunksBeforeLines(t *testing.T) {
	a, doc := bigDoc(t)
	hunks := collectSem(doc)
	total := 0
	for _, h := range hunks {
		total += h.chars
	}
	if total <= jev.Budget {
		t.Fatalf("fixture is %d chars, which is inside the budget of %d", total, jev.Budget)
	}

	client, log := stubJev(t,
		`{"hunk":{"choice":"H007","probabilities":{"H007":0.7,"H011":0.2}},"present":{"noul":0.9}}`,
		`{"line":{"choice":"L0040","probabilities":{"L0040":0.9}},"present":{"noul":0.9}}`,
	)
	a.jev = client

	got, err := semSearch(context.Background(), a.jev, "which file has f07", hunks)
	if err != nil {
		t.Fatalf("semSearch: %v", err)
	}
	if len(*log) != 2 {
		t.Fatalf("made %d requests, want 2", len(*log))
	}
	if !has((*log)[0].ids, "hunk") {
		t.Errorf("first request asked %v, want the hunk stage", (*log)[0].ids)
	}
	if n := len((*log)[0].criteria); n != len(hunks) {
		t.Errorf("hunk stage offered %d options, want all %d hunks", n, len(hunks))
	}
	// The second request carries the lines of the two hunks that won, not the
	// whole change.
	if n := len((*log)[1].criteria); n != 12 {
		t.Errorf("line stage offered %d options, want the 12 lines of the two chosen hunks", n)
	}
	if len(got) != 1 || got[0].id != "L0040" {
		t.Fatalf("hits = %v, want the single chosen line", got)
	}
}

// The choice always has a winner, so the presence question is what stands
// between a question about something absent and a confident jump to a wrong
// line. It also saves the second request.
func TestAbsentAnswerYieldsNoHitsAndNoSecondRequest(t *testing.T) {
	a, doc := bigDoc(t)
	client, log := stubJev(t, `{"hunk":{"choice":"H001","probabilities":{"H001":0.9}},"present":{"noul":0.04}}`)
	a.jev = client

	got, err := semSearch(context.Background(), a.jev, "where is the database migration", collectSem(doc))
	if err != nil {
		t.Fatalf("semSearch: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("hits = %v, want none when the change does not contain an answer", got)
	}
	if len(*log) != 1 {
		t.Errorf("made %d requests, want 1: a change with no answer is not worth a second", len(*log))
	}
}

// The same guard applies once the lines are in view, where the hunks that won
// won only against each other.
func TestAbsentAnswerAtTheLineStageYieldsNoHits(t *testing.T) {
	a, doc := threeFileDoc(t)
	client, _ := stubJev(t, `{"line":{"choice":"L0001","probabilities":{"L0001":0.99}},"present":{"noul":0.02}}`)
	a.jev = client

	got, err := semSearch(context.Background(), a.jev, "where is the retry logic", collectSem(doc))
	if err != nil {
		t.Fatalf("semSearch: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("hits = %v, want none", got)
	}
}

// Typing a question does not search as it is typed: it would cost a request
// per keystroke and ask half a question every time.
func TestTypingAQuestionDoesNotMatchAsAString(t *testing.T) {
	a, _ := threeFileDoc(t)
	a.findField = reef.NewField("where is new0")
	a.askMode = true
	a.findHits = []int{4}
	a.findAt = 0
	a.askDoc, a.askSpots = a.diff, []lineSpot{a.diff.lineSpot(4)}
	a.rebuildFind()
	if len(a.findHits) != 1 || a.findAt != 0 {
		t.Errorf("hits = %v at %d, want the previous hits left alone", a.findHits, a.findAt)
	}
	a.askMode = false
	a.findField.SetText("new0")
	a.rebuildFind()
	if len(a.findHits) == 0 {
		t.Error("an ordinary query stopped matching")
	}
}

// Asking has its own way in, because the prefix cannot be typed to reach it:
// the bar is only drawn once it has focus, and "?" alone is the help sheet.
func TestShiftFOpensTheBarReadyForAQuestion(t *testing.T) {
	h := newHarness(t)
	h.settle()
	h.app.jev = jev.NewAt("k", "http://example.invalid")
	h.app.focus = PaneDiff

	h.press("F", key.ModShift)
	if !h.app.findOpen {
		t.Fatal("the bar is not open, so there is nowhere to type")
	}
	if !h.app.findField.Focused() {
		t.Fatal("the bar did not take the caret")
	}
	// Whatever is typed is the question, whole and in order.
	h.typeText("where is the budget enforced")
	if got := h.app.findField.Text(); got != "where is the budget enforced" {
		t.Errorf("field = %q, want exactly what was typed", got)
	}
}

// Shift-F with no key says so rather than opening a bar that cannot answer.
func TestShiftFWithoutAKeySaysSo(t *testing.T) {
	h := newHarness(t)
	h.settle()
	h.app.jev = nil
	h.app.focus = PaneDiff

	h.press("F", key.ModShift)
	if h.app.findOpen || h.app.askMode {
		t.Error("the bar opened although there is no key to answer with")
	}
	if !strings.Contains(h.app.status, "key") {
		t.Errorf("status = %q, want it to mention the missing key", h.app.status)
	}
}

// Plain F is still find, and the manifest's F is still the filter.
func TestPlainFIsStillFind(t *testing.T) {
	h := newHarness(t)
	h.settle()
	h.app.jev = jev.NewAt("k", "http://example.invalid")
	h.app.focus = PaneDiff

	h.press("F", 0)
	if !h.app.findOpen {
		t.Fatal("F did not open the bar")
	}
	if h.app.askMode {
		t.Error("F opened a question rather than a find")
	}
}

// manyDoc is a change of n files, each one hunk of lines lines.
func manyDoc(t *testing.T, files, lines int) (*App, *DiffDoc) {
	t.Helper()
	var b strings.Builder
	body := strings.Repeat("y", 40)
	for f := range files {
		fmt.Fprintf(&b, "diff --git a/g%03d.go b/g%03d.go\n--- a/g%03d.go\n+++ b/g%03d.go\n", f, f, f, f)
		fmt.Fprintf(&b, "@@ -1,%d +1,%d @@ func g%03d()\n", lines, lines, f)
		for l := range lines {
			fmt.Fprintf(&b, "+%s%03d%03d\n", body, f, l)
		}
	}
	parsed := diffparse.Parse(b.String())
	rows := make([]FileRow, len(parsed))
	for i := range parsed {
		rows[i].Path = parsed[i].Path()
	}
	doc := buildDoc(rows, parsed)
	a := &App{store: state.New(), diff: doc}
	a.rev.ChangeIDFull = "change"
	a.rebuildRows()
	return a, doc
}

// A choice may offer only so many options, and a diff runs past that long
// before it runs past what a request may carry. No stage may exceed it.
func TestNoStageOffersMoreOptionsThanAChoiceTakes(t *testing.T) {
	a, doc := manyDoc(t, 300, 1)
	// 300 files are two choices in turn, and the second of them has nothing.
	client, log := stubJev(t,
		`{"file":{"choice":"F001","probabilities":{"F001":0.9}},"present":{"noul":0.9}}`,
		`{"file":{"choice":"F256","probabilities":{"F256":0.9}},"present":{"noul":0.1}}`,
		`{"line":{"choice":"L0001","probabilities":{"L0001":0.9}},"present":{"noul":0.9}}`,
	)
	a.jev = client

	if _, err := semSearch(context.Background(), a.jev, "which file has g000", collectSem(doc)); err != nil {
		t.Fatalf("semSearch: %v", err)
	}
	for i, r := range *log {
		if n := len(r.criteria); n > jev.MaxChoices {
			t.Errorf("request %d offered %d options, the most is %d", i, n, jev.MaxChoices)
		}
	}
	if !has((*log)[0].ids, "file") {
		t.Errorf("first request asked %v, want the file stage: 300 hunks cannot be one choice", (*log)[0].ids)
	}
}

// A single hunk past the option limit — a file added whole — is divided and
// narrowed again rather than offered entire.
func TestAnOversizedHunkIsWindowed(t *testing.T) {
	a, doc := manyDoc(t, 1, 400)
	hunks := collectSem(doc)
	if len(hunks) != 1 || len(hunks[0].cands) != 400 {
		t.Fatalf("fixture is %d hunks of %d lines, want one of 400", len(hunks), len(hunks[0].cands))
	}
	client, log := stubJev(t,
		`{"hunk":{"choice":"H001","probabilities":{"H001":0.95}},"present":{"noul":0.9}}`,
		`{"w00":{"noul":0.2},"w01":{"noul":0.88}}`,
		`{"line":{"choice":"L0300","probabilities":{"L0300":0.9}},"present":{"noul":0.9}}`,
	)
	a.jev = client

	got, err := semSearch(context.Background(), a.jev, "where is the three hundredth line", hunks)
	if err != nil {
		t.Fatalf("semSearch: %v", err)
	}
	if len(*log) != 3 {
		t.Fatalf("made %d requests, want three: hunks, windows, lines", len(*log))
	}
	// The windows are asked about together, over one copy of the hunk.
	if n := len((*log)[1].ids); n < 2 {
		t.Errorf("the window stage asked %d questions, want one per window", n)
	}
	for i, r := range *log {
		if n := len(r.criteria); n > jev.MaxChoices {
			t.Errorf("request %d offered %d options, the most is %d", i, n, jev.MaxChoices)
		}
	}
	// The second window won, so the lines offered come from its end of the hunk.
	if len(got) != 1 || got[0].id != "L0300" {
		t.Fatalf("hits = %v, want the line from the winning window", got)
	}
}

// A window nothing was found in is not narrowed further.
func TestAnEmptyWindowStageYieldsNoHits(t *testing.T) {
	a, doc := manyDoc(t, 1, 400)
	client, log := stubJev(t,
		`{"hunk":{"choice":"H001","probabilities":{"H001":0.95}},"present":{"noul":0.9}}`,
		`{"w00":{"noul":0.05},"w01":{"noul":0.03}}`,
	)
	a.jev = client

	got, err := semSearch(context.Background(), a.jev, "where is the database migration", collectSem(doc))
	if err != nil {
		t.Fatalf("semSearch: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("hits = %v, want none", got)
	}
	if len(*log) != 2 {
		t.Errorf("made %d requests, want two: no window is worth a third", len(*log))
	}
}

// echoJev answers every request as if the first option offered were the
// answer and every region held one, and records how large each request was.
func echoJev(t *testing.T) (*jev.Client, *[]int) {
	t.Helper()
	var sizes []int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			State struct {
				Lines string `json:"lines"`
			} `json:"state"`
			Questions map[string]struct {
				Type     string            `json:"type"`
				Criteria map[string]string `json:"criteria"`
			} `json:"questions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
		}
		size := len(req.State.Lines)
		answers := map[string]any{}
		for id, q := range req.Questions {
			if q.Type != "choice" {
				answers[id] = map[string]any{"noul": 0.9}
				continue
			}
			first := ""
			for k, v := range q.Criteria {
				size += len(k) + len(v)
				if first == "" || k < first {
					first = k
				}
			}
			answers[id] = map[string]any{"choice": first, "probabilities": map[string]float64{first: 0.9}}
		}
		sizes = append(sizes, size)
		json.NewEncoder(w).Encode(map[string]any{"answers": answers})
	}))
	t.Cleanup(srv.Close)
	return jev.NewAt("k", srv.URL), &sizes
}

// A hunk of long lines is past the budget well before it is past the option
// limit, and no request made about it may be past the budget either.
func TestAHunkOfLongLinesStaysInsideTheBudget(t *testing.T) {
	var b strings.Builder
	b.WriteString("diff --git a/min.js b/min.js\n--- a/min.js\n+++ b/min.js\n@@ -1,200 +1,200 @@\n")
	for l := range 200 {
		fmt.Fprintf(&b, "+%s%03d\n", strings.Repeat("z", 1000), l)
	}
	parsed := diffparse.Parse(b.String())
	doc := buildDoc([]FileRow{{Path: parsed[0].Path()}}, parsed)
	a := &App{store: state.New(), diff: doc}
	a.rev.ChangeIDFull = "change"
	a.rebuildRows()

	client, sizes := echoJev(t)
	got, err := semSearch(context.Background(), client, "where is line 150", collectSem(doc))
	if err != nil {
		t.Fatalf("semSearch: %v", err)
	}
	if len(got) == 0 {
		t.Error("found nothing, want the line the stub always picks")
	}
	for i, n := range *sizes {
		if n > jev.Budget {
			t.Errorf("request %d carried %d chars, the budget is %d", i, n, jev.Budget)
		}
	}
}

// A question's hits are places in the change, not row numbers: filing a note
// above them renumbers every row, and n must still land on the lines found.
func TestAskHitsFollowTheirLinesThroughARebuild(t *testing.T) {
	a, doc := threeFileDoc(t)
	a.findField = reef.NewField("where is new0")
	a.askMode = true
	row := -1
	for i := range doc.Rows {
		if r := doc.Row(i); r.Kind == rowLine && r.Line.Text == "new0" {
			row = i
		}
	}
	if row < 0 {
		t.Fatal("fixture has no new0 line")
	}
	a.askDoc, a.askSpots = doc, []lineSpot{doc.lineSpot(row)}
	a.findHits, a.findAt = []int{row}, 0

	// A note opened on the first line of the change pushes everything below it down.
	for i := range doc.Rows {
		if doc.Row(i).Kind == rowLine {
			doc.Cursor = i
			break
		}
	}
	a.startComment()
	if a.draft == nil {
		t.Fatal("startComment opened no draft")
	}
	if len(a.findHits) != 1 {
		t.Fatalf("hits = %v, want the one hit kept", a.findHits)
	}
	if got := doc.Row(a.findHits[0]).Line.Text; got != "new0" {
		t.Errorf("hit now lands on %q, want new0", got)
	}
}

func TestSemClipKeepsCharactersWhole(t *testing.T) {
	s := strings.Repeat("é", semTextMax)
	got := semClip(s)
	if len(got) > semTextMax+len("…") {
		t.Errorf("clipped to %d bytes, want at most %d", len(got), semTextMax+len("…"))
	}
	if !utf8.ValidString(got) {
		t.Errorf("clip split a character: %q", got)
	}
	if semClip("short") != "short" {
		t.Error("a short string was changed")
	}
}
