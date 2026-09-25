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

// briefCall is one request the brief stub received.
type briefCall struct {
	ids  []string
	size int // the state's lines plus the longest criteria
}

// briefJev answers every choice with its first option and every presence
// question with yes, except the ones absent names, and calls the change not
// mechanical. It is safe for the brief's parallel requests.
func briefJev(t *testing.T, absent ...string) (*jev.Client, func() []briefCall) {
	t.Helper()
	var (
		mu    sync.Mutex
		calls []briefCall
	)
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
		call := briefCall{size: len(req.State.Lines)}
		longest := 0
		answers := map[string]any{}
		for id, q := range req.Questions {
			call.ids = append(call.ids, id)
			if q.Type != "choice" {
				p := 0.9
				if id == "mechanical" || has(absent, id) {
					p = 0.1
				}
				answers[id] = map[string]any{"noul": p}
				continue
			}
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

// A change small enough to send whole is briefed in one request: every card,
// its presence question and the mechanical question together.
func TestBriefAsksEveryCardInOneRequest(t *testing.T) {
	_, doc := threeFileDoc(t)
	client, calls := briefJev(t)
	res, err := runBrief(context.Background(), client, collectSem(doc))
	if err != nil {
		t.Fatalf("runBrief: %v", err)
	}
	got := calls()
	if len(got) != 1 {
		t.Fatalf("made %d requests, want 1", len(got))
	}
	for _, card := range briefCards {
		if !has(got[0].ids, card.id) || !has(got[0].ids, card.id+"_present") {
			t.Errorf("request asked %v, missing card %s", got[0].ids, card.id)
		}
	}
	if !has(got[0].ids, "mechanical") {
		t.Error("the mechanical question was not asked")
	}
	if res.mechanical >= briefMechanical {
		t.Errorf("mechanical = %v, want the stub's no", res.mechanical)
	}
	for i, card := range briefCards {
		if len(res.cards[i]) != 1 {
			t.Errorf("card %s has %d hits, want the stub's one", card.id, len(res.cards[i]))
			continue
		}
		if row := doc.briefRow(res.cards[i][0]); row < 0 || doc.Row(row).Kind != rowLine {
			t.Errorf("card %s's hit does not resolve to a diff line", card.id)
		}
	}
}

// A card whose question the change does not answer lists nothing, however its
// choice fell.
func TestBriefCardTheChangeDoesNotAnswerIsEmpty(t *testing.T) {
	_, doc := threeFileDoc(t)
	client, _ := briefJev(t, "trust_present")
	res, err := runBrief(context.Background(), client, collectSem(doc))
	if err != nil {
		t.Fatalf("runBrief: %v", err)
	}
	for i, card := range briefCards {
		want := 1
		if card.id == "trust" {
			want = 0
		}
		if len(res.cards[i]) != want {
			t.Errorf("card %s has %d hits, want %d", card.id, len(res.cards[i]), want)
		}
	}
}

// Past the budget every card ranks hunks in one shared request, and then each
// reads the lines of its own; nothing sent is past the budget.
func TestBriefOfALargeChangeRanksHunksThenLines(t *testing.T) {
	_, doc := bigDoc(t)
	client, calls := briefJev(t)
	res, err := runBrief(context.Background(), client, collectSem(doc))
	if err != nil {
		t.Fatalf("runBrief: %v", err)
	}
	got := calls()
	if len(got) != 1+len(briefCards) {
		t.Fatalf("made %d requests, want one for the hunks and one per card", len(got))
	}
	hunkStage := 0
	for _, c := range got {
		if has(c.ids, "mechanical") {
			hunkStage++
			for _, card := range briefCards {
				if !has(c.ids, card.id) {
					t.Errorf("hunk stage is missing card %s", card.id)
				}
			}
		}
		if c.size > jev.Budget {
			t.Errorf("a request carried %d chars, the budget is %d", c.size, jev.Budget)
		}
	}
	if hunkStage != 1 {
		t.Errorf("%d requests asked the mechanical question, want the hunk stage alone", hunkStage)
	}
	for i, card := range briefCards {
		if len(res.cards[i]) == 0 {
			t.Errorf("card %s found nothing", card.id)
		}
	}
}

// newBriefRepo is the harness repository with a bookmark on the first change,
// which is a branch of one commit off the root.
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

// The default view opens on the working copy with jev's brief of it, lists
// the branches beside it, and walks from a branch as a whole into one of its
// commits.
func TestBriefViewReviewsTheWorkingCopyThenABranch(t *testing.T) {
	h := openHarnessView(t, newBriefRepo(t), true)
	if h.app.classic() {
		t.Fatal("the default view is classic, want the brief")
	}
	client, _ := briefJev(t)
	h.app.jev = client
	h.app.reload(false)
	h.settle()

	rows := h.app.reviewRows()
	if len(rows) != 2 || rows[0].kind != reviewWorking || rows[1].kind != reviewBranch || rows[1].name != "feat" {
		t.Fatalf("review rows = %+v, want the working copy then feat", rows)
	}
	if h.app.reviewKey != "@" || !h.app.rev.WorkingCopy {
		t.Fatalf("under review: %q %+v, want the working copy", h.app.reviewKey, h.app.rev)
	}
	if h.app.brief == nil || h.app.brief.res == nil {
		t.Fatal("no brief of the working copy")
	}
	if len(h.app.heat) == 0 {
		t.Error("the brief put no heat on any file")
	}

	// Walking the brief walks the diff.
	h.app.focus = PaneFiles
	h.press("J", 0)
	_, hits := h.app.briefLines()
	if len(hits) < 2 {
		t.Fatalf("brief has %d leads, want one per card", len(hits))
	}
	if want := h.app.diff.briefRow(hits[1]); h.app.diff.Cursor != want {
		t.Errorf("diff cursor = %d, want the second lead's row %d", h.app.diff.Cursor, want)
	}

	// The branch, whole.
	h.app.focus = PaneRevs
	h.press("J", 0)
	h.settle()
	if h.app.reviewKey != "b:feat" || h.app.spec.Kind != vcs.DiffRange {
		t.Fatalf("under review: %q %+v, want feat as a range", h.app.reviewKey, h.app.spec)
	}
	if got := h.app.diffTitle(); got != "DIFF · FEAT" {
		t.Errorf("diff title = %q", got)
	}
	if h.app.brief == nil || h.app.brief.res == nil {
		t.Error("no brief of the branch")
	}
	rows = h.app.reviewRows()
	if len(rows) != 3 || rows[2].kind != reviewCommit {
		t.Fatalf("review rows = %+v, want feat opened onto its commit", rows)
	}

	// Then its one commit.
	h.press("J", 0)
	h.settle()
	if h.app.spec.Kind != vcs.DiffChange || h.app.rev.Description != "first" {
		t.Errorf("under review: %+v %q, want the first change", h.app.spec, h.app.rev.Description)
	}
}

// The bar at the head of the brief takes a question and lists its answer
// above the cards.
func TestBriefBarAsksAQuestion(t *testing.T) {
	h := openHarnessView(t, newBriefRepo(t), true)
	client, _ := briefJev(t)
	h.app.jev = client
	h.app.reload(false)
	h.settle()

	h.press("A", 0)
	if !h.app.askField.Focused() {
		t.Fatal("A did not put the caret in the jev bar")
	}
	h.typeText("where is main printed")
	h.press(key.NameReturn, 0)
	h.settle()
	if h.app.jevAsk == nil || h.app.jevAsk.running {
		t.Fatal("the question was not asked")
	}
	if len(h.app.jevAsk.hits) == 0 {
		t.Fatal("the question found nothing")
	}
	lines, _ := h.app.briefLines()
	if lines[0].text != "ASKED" || lines[1].text != "where is main printed" {
		t.Errorf("column starts %q / %q, want the question at the top", lines[0].text, lines[1].text)
	}
	if want := h.app.diff.briefRow(h.app.jevAsk.hits[0]); h.app.diff.Cursor != want {
		t.Errorf("diff cursor = %d, want the answer's row %d", h.app.diff.Cursor, want)
	}
}

// Without a key the column says how to get one rather than standing empty.
func TestBriefWithoutAKeySaysHowToGetOne(t *testing.T) {
	h := openHarnessView(t, newBriefRepo(t), true)
	if h.app.jev != nil {
		t.Fatal("a key leaked into the test")
	}
	lines, hits := h.app.briefLines()
	if len(hits) != 0 || lines[0].text != "NO KEY" {
		t.Errorf("column = %+v, want the no-key note", lines)
	}
	if !strings.HasPrefix(h.app.briefTitle(), "JEV · OFF") {
		t.Errorf("title = %q", h.app.briefTitle())
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
