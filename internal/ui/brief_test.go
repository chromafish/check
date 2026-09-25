package ui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"testing"

	"gioui.org/io/key"

	"github.com/chromafish/check/internal/jev"
	"github.com/chromafish/check/internal/state"
	"github.com/chromafish/check/internal/vcs"
)

// briefCall is one request the stub received.
type briefCall struct {
	ids   []string
	size  int // the state's lines plus the longest criteria
	state map[string]string
}

// stubMind is what the stub service makes of every change: what kind it is,
// what it touches, and which presence questions it answers no to.
type stubMind struct {
	kind   string
	topics []string
	absent []string
}

// briefJev answers the first step with mind's kind and topics, every choice
// after it with its first option, and every presence question yes except
// those mind calls absent. It is safe for the brief's parallel requests.
func briefJev(t *testing.T, mind stubMind) (*jev.Client, func() []briefCall) {
	t.Helper()
	if mind.kind == "" {
		mind.kind = "feature"
	}
	var (
		mu    sync.Mutex
		calls []briefCall
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			State     map[string]string `json:"state"`
			Questions map[string]struct {
				Type     string            `json:"type"`
				Criteria map[string]string `json:"criteria"`
			} `json:"questions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
		}
		call := briefCall{size: len(req.State["lines"]), state: req.State}
		longest := 0
		answers := map[string]any{}
		for id, q := range req.Questions {
			call.ids = append(call.ids, id)
			switch {
			case id == "kind":
				answers[id] = map[string]any{"choice": mind.kind, "probabilities": map[string]float64{mind.kind: 0.9}}
			case strings.HasPrefix(id, "t_"):
				p := 0.1
				if has(mind.topics, strings.TrimPrefix(id, "t_")) {
					p = 0.9
				}
				answers[id] = map[string]any{"noul": p}
			case q.Type != "choice":
				p := 0.9
				if has(mind.absent, id) {
					p = 0.1
				}
				answers[id] = map[string]any{"noul": p}
			default:
				keys := make([]string, 0, len(q.Criteria))
				n := 0
				for k, v := range q.Criteria {
					keys = append(keys, k)
					n += len(k) + len(v)
				}
				longest = max(longest, n)
				sort.Strings(keys)
				answers[id] = map[string]any{"choice": keys[0], "probabilities": map[string]float64{keys[0]: 0.9}}
			}
		}
		call.size += longest
		sort.Strings(call.ids)
		mu.Lock()
		calls = append(calls, call)
		mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{"answers": answers})
	}))
	t.Cleanup(srv.Close)
	return jev.NewAt("k", srv.URL), func() []briefCall {
		mu.Lock()
		defer mu.Unlock()
		return append([]briefCall(nil), calls...)
	}
}

func cardIDs(cards []briefCard) []string {
	ids := make([]string, len(cards))
	for i, c := range cards {
		ids[i] = c.id
	}
	return ids
}

// A change of tests is asked about tests, and what it touches: not about
// races it has nothing to do with.
func TestBriefAsksWhatFitsTheChange(t *testing.T) {
	_, doc := threeFileDoc(t)
	client, calls := briefJev(t, stubMind{kind: "tests", topics: []string{"storage", "secrets"}})
	base := semState{Languages: "Rust", Files: "A tests/minio.rs +224 -0\n"}
	res, err := runBrief(context.Background(), client, collectSem(doc), base)
	if err != nil {
		t.Fatalf("runBrief: %v", err)
	}
	if res.kind.id != "tests" {
		t.Errorf("kind = %q, want tests", res.kind.id)
	}
	asked := cardIDs(res.asked)
	for _, want := range []string{"vacuous", "flaky", "residue", "dataloss", "secrets"} {
		if !has(asked, want) {
			t.Errorf("asked %v, want %s among them", asked, want)
		}
	}
	for _, not := range []string{"races", "behaviour", "breaking"} {
		if has(asked, not) {
			t.Errorf("asked %s of a change of tests that touches no such thing", not)
		}
	}
	got := calls()
	if len(got) != 2 {
		t.Fatalf("made %d requests, want the first step and one for the questions", len(got))
	}
	if got[0].state["languages"] != "Rust" || !strings.Contains(got[0].state["files"], "minio.rs") {
		t.Errorf("first step's state = %v, want the languages and files beside the lines", got[0].state)
	}
}

// Nothing the brief can ask names one language's constructs: a Rust change
// asked about goroutines reads as a tool that has not looked.
func TestBriefBankIsLanguageNeutral(t *testing.T) {
	for _, c := range briefBank {
		low := strings.ToLower(c.ask)
		for _, word := range []string{"goroutine", "channel", "unwrap", "panic(", "nil", "promise", "async fn"} {
			if strings.Contains(low, word) {
				t.Errorf("question %s says %q", c.id, word)
			}
		}
	}
}

// A rename or reformat is not asked anything after the first step.
func TestBriefOfAMechanicalChangeAsksNothingMore(t *testing.T) {
	_, doc := threeFileDoc(t)
	client, calls := briefJev(t, stubMind{kind: "mechanical"})
	res, err := runBrief(context.Background(), client, collectSem(doc), semState{})
	if err != nil {
		t.Fatalf("runBrief: %v", err)
	}
	if len(res.asked) != 0 || len(calls()) != 1 {
		t.Errorf("asked %v in %d requests, want nothing past the first step", cardIDs(res.asked), len(calls()))
	}
}

// A line more than one question points at is listed once, under the question
// it answers best, and the questions that found nothing are named as such.
func TestBriefListsALineOnce(t *testing.T) {
	_, doc := threeFileDoc(t)
	client, _ := briefJev(t, stubMind{kind: "feature", topics: []string{"errors"}, absent: []string{"untested_present"}})
	res, err := runBrief(context.Background(), client, collectSem(doc), semState{})
	if err != nil {
		t.Fatalf("runBrief: %v", err)
	}
	groups, empty := res.findings()
	total := 0
	for _, g := range groups {
		total += len(g)
	}
	// The stub points every question at the same first line.
	if total != 1 {
		t.Errorf("listed %d lines, want the one line every question chose, once", total)
	}
	if len(empty) == 0 {
		t.Error("no question is named as having found nothing")
	}
}

// Past the budget the questions rank hunks together, then each reads its own
// lines; nothing sent is past the budget.
func TestBriefOfALargeChangeStaysInsideTheBudget(t *testing.T) {
	_, doc := bigDoc(t)
	client, calls := briefJev(t, stubMind{kind: "feature", topics: []string{"errors"}})
	res, err := runBrief(context.Background(), client, collectSem(doc), semState{})
	if err != nil {
		t.Fatalf("runBrief: %v", err)
	}
	if len(res.asked) == 0 {
		t.Fatal("nothing was asked")
	}
	got := calls()
	if want := 2 + len(res.asked); len(got) != want {
		t.Errorf("made %d requests, want the first step, the hunks, and one per question: %d", len(got), want)
	}
	for _, c := range got {
		if c.size > jev.Budget {
			t.Errorf("a request carried %d chars, the budget is %d", c.size, jev.Budget)
		}
	}
}

// commitAll commits whatever the working tree holds, leaving it clean.
func commitAll(t *testing.T, dir, msg string) {
	t.Helper()
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-qm", msg}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

// A repository where everything is on main, and nothing is uncommitted, opens
// on main's history, reviewing its newest commit; there is no working copy
// row pretending to be something to review.
func TestNavigatorOnATrunkOnlyRepository(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	dir := newGitRepo(t)
	commitAll(t, dir, "second")
	h := openHarnessView(t, dir, true)

	rows := h.app.reviewRows()
	if len(rows) != 3 || rows[0].kind != reviewBranch || rows[0].name != "main" {
		t.Fatalf("rows = %+v, want main then its two commits", rows)
	}
	if rows[1].kind != reviewCommit || rows[1].rev.Description != "second" || rows[2].rev.Description != "first" {
		t.Errorf("main's commits = %q, %q; want second then first", rows[1].rev.Description, rows[2].rev.Description)
	}
	if h.app.spec.Kind != vcs.DiffChange || h.app.rev.Description != "second" {
		t.Errorf("under review: %+v %q, want the newest commit", h.app.spec, h.app.rev.Description)
	}
	h.app.focus = PaneRevs
	h.press("J", 0)
	h.press("J", 0)
	h.settle()
	if h.app.rev.Description != "first" {
		t.Errorf("under review: %q, want first after moving down", h.app.rev.Description)
	}
}

// newBriefRepo is the harness repository with a bookmark on the first change,
// which the working copy sits above.
func newBriefRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("jj"); err != nil {
		t.Skip("jj is not installed")
	}
	dir := newRepo(t)
	cmd := exec.Command("jj", "bookmark", "create", "feat", "-r", "subject(first)")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "JJ_USER=Test", "JJ_EMAIL=test@example.com")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("jj bookmark: %v\n%s", err, out)
	}
	return dir
}

// Uncommitted work comes first and is what opens; the branch beneath it is
// listed, current and open, and reviewed whole when chosen.
func TestNavigatorWithUncommittedWorkAndABranch(t *testing.T) {
	h := openHarnessView(t, newBriefRepo(t), true)
	client, _ := briefJev(t, stubMind{kind: "feature", topics: []string{"errors"}})
	h.app.jev = client
	h.app.reload(false)
	h.settle()

	rows := h.app.reviewRows()
	if len(rows) < 2 || rows[0].kind != reviewWorking || rows[1].name != "feat" {
		t.Fatalf("rows = %+v, want uncommitted work then feat", rows)
	}
	if h.app.current != "feat" || !h.app.opened["feat"] {
		t.Errorf("current = %q, open = %v; want feat, open", h.app.current, h.app.opened)
	}
	if h.app.reviewKey != "@" {
		t.Fatalf("under review: %q, want the uncommitted work", h.app.reviewKey)
	}
	if h.app.brief == nil || h.app.brief.res == nil {
		t.Fatal("no brief of the uncommitted work")
	}

	h.app.focus = PaneRevs
	h.press("J", 0)
	h.settle()
	if h.app.reviewKey != branchKey("feat") || h.app.spec.Kind != vcs.DiffRange {
		t.Errorf("under review: %q %+v, want feat as a range", h.app.reviewKey, h.app.spec)
	}
	if got := h.app.diffTitle(); got != "DIFF · FEAT" {
		t.Errorf("diff title = %q", got)
	}
}

// A opens jev's sheet over the review; walking it walks the diff, Enter puts
// it away on the line, and the same lines are marked in the gutter.
func TestJevSheet(t *testing.T) {
	h := openHarnessView(t, newBriefRepo(t), true)
	client, _ := briefJev(t, stubMind{kind: "feature", topics: []string{"errors", "leftovers"}})
	h.app.jev = client
	h.app.reload(false)
	h.settle()

	if label, _ := h.app.jevControl(); !strings.HasPrefix(label, "JEV ") {
		t.Errorf("header control = %q, want the count of lines", label)
	}
	h.press("A", 0)
	if !h.app.jevOpen {
		t.Fatal("A did not open the sheet")
	}
	kind, _ := h.app.briefSummary()
	if !strings.HasPrefix(kind, "FEATURE") {
		t.Errorf("sheet heading = %q, want the kind of change", kind)
	}
	_, hits := h.app.briefLines()
	if len(hits) == 0 {
		t.Fatal("the sheet lists no lines")
	}
	row := h.app.diff.briefRow(hits[0])
	if !h.app.marked(h.app.diff, row) {
		t.Error("the line the sheet lists is not marked in the gutter")
	}
	h.press(key.NameReturn, 0)
	if h.app.jevOpen {
		t.Error("Enter left the sheet up")
	}
	if h.app.diff.Cursor != row || h.app.focus != PaneDiff {
		t.Errorf("cursor %d focus %v, want row %d in the diff", h.app.diff.Cursor, h.app.focus, row)
	}
}

// The sheet's bar takes a question and lists its answer at the top.
func TestJevSheetAsksAQuestion(t *testing.T) {
	h := openHarnessView(t, newBriefRepo(t), true)
	client, _ := briefJev(t, stubMind{})
	h.app.jev = client
	h.app.reload(false)
	h.settle()

	h.press("A", 0)
	h.press("/", 0)
	if !h.app.askField.Focused() {
		t.Fatal("/ did not put the caret in the bar")
	}
	h.typeText("where is main printed")
	h.press(key.NameReturn, 0)
	h.settle()
	if h.app.jevAsk == nil || h.app.jevAsk.running || len(h.app.jevAsk.hits) == 0 {
		t.Fatalf("the question was not answered: %+v", h.app.jevAsk)
	}
	lines, _ := h.app.briefLines()
	if lines[0].text != "ASKED" || lines[1].text != "where is main printed" {
		t.Errorf("sheet starts %q / %q, want the question at the top", lines[0].text, lines[1].text)
	}
}

// Without a key the sheet says how to get one, and the header says jev is off.
func TestJevWithoutAKey(t *testing.T) {
	h := openHarnessView(t, newBriefRepo(t), true)
	if h.app.jev != nil {
		t.Fatal("a key leaked into the test")
	}
	lines, hits := h.app.briefLines()
	if len(hits) != 0 || !strings.HasPrefix(lines[0].text, "jev is off") {
		t.Errorf("sheet = %+v, want the no-key note", lines)
	}
	if label, _ := h.app.jevControl(); label != "JEV OFF" {
		t.Errorf("header control = %q", label)
	}
}

// B moves between the views and the choice is kept.
func TestBSwitchesToTheClassicViewAndRemembersIt(t *testing.T) {
	h := openHarnessView(t, newBriefRepo(t), true)
	h.press("B", 0)
	if !h.app.classic() {
		t.Fatal("B did not switch to the classic view")
	}
	if !state.LoadSettings().Classic {
		t.Error("the classic view was not recorded")
	}
	h.press("B", 0)
	if h.app.classic() {
		t.Error("B did not switch back")
	}
}
