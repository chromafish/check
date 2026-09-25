package plan

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/chromafish/check/internal/diffparse"
	"github.com/chromafish/check/internal/llm"
	"github.com/chromafish/check/internal/signals"
	"github.com/chromafish/check/internal/triage"
)

const diff = `diff --git a/store.go b/store.go
--- a/store.go
+++ b/store.go
@@ -1,3 +1,6 @@ package store
 func Consolidate() {
+	span := tracer.Start(ctx, "consolidate")
+	repack()
+	upload()
 }
diff --git a/store_test.go b/store_test.go
--- a/store_test.go
+++ b/store_test.go
@@ -1,2 +1,3 @@
 func TestConsolidate(t *testing.T) {
+	Consolidate()
 }
`

func input() Input {
	files := diffparse.Parse(diff)
	inv := &signals.Inventory{
		Signals: []signals.Found{{Signal: signals.Signal{Kind: signals.Span, Name: "consolidate", Detector: "otel-go-span", Path: "store.go", Line: 2}, Status: signals.Added}},
		Dark:    []signals.Dark{{Path: "store.go", Line: 3, Changed: 2}},
	}
	return Input{Commit: "abc", Message: "Consolidate the store", Files: []*diffparse.File{&files[0], &files[1]}, Inv: inv}
}

// model answers every request with reply, and records the user prompt.
func model(t *testing.T, reply string) (*llm.Client, *string) {
	t.Helper()
	var prompt string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct{ Content string } `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		prompt = req.Messages[1].Content
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]string{"role": "assistant", "content": reply}}},
		})
	}))
	t.Cleanup(srv.Close)
	return llm.New(llm.Config{BaseURL: srv.URL, Model: "tiny"}), &prompt
}

// What the model says about signals and code it was not told of is dropped.
func TestWriteChecksTheModel(t *testing.T) {
	c, prompt := model(t, `{
		"summary": "Consolidate now repacks and uploads.",
		"watch": [{"signal": "S1", "expect": "appear", "why": "new span"},
		          {"signal": "S9", "expect": "rise", "why": "invented"}],
		"gaps": [{"where": "D1", "unseen": "upload failures", "kind": "error", "name": "upload_failed", "snippet": "span.RecordError(err)"},
		         {"where": "elsewhere.go:9", "unseen": "?", "kind": "log", "name": "x", "snippet": ""}],
		"regressions": [{"signal": "S1", "proposed": false, "condition": "p99 above 2s"},
		                {"signal": "upload_failed", "proposed": true, "condition": "any"},
		                {"signal": "made_up_total", "proposed": true, "condition": "any"}],
		"rollout": {"risk": "medium", "advice": "canary"}
	}`)
	p, err := Write(context.Background(), c, input())
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Watch) != 1 || p.Watch[0].Signal.Name != "consolidate" {
		t.Errorf("watch %+v", p.Watch)
	}
	if len(p.Gaps) != 1 || p.Gaps[0].Path != "store.go" || p.Gaps[0].Line != 3 {
		t.Errorf("gaps %+v", p.Gaps)
	}
	if len(p.Regressions) != 2 || p.Dropped != 3 {
		t.Errorf("regressions %+v dropped %d", p.Regressions, p.Dropped)
	}
	if p.Model != "tiny" || p.Commit != "abc" || p.Rollout.Risk != "medium" {
		t.Errorf("plan %+v", p)
	}
	for _, want := range []string{"S1 [added span] \"consolidate\" at store.go:2", "D1 store.go:3", "+    2| \tspan :="} {
		if !strings.Contains(*prompt, want) {
			t.Errorf("prompt lacks %q:\n%s", want, *prompt)
		}
	}
	md := Markdown(p, input().Inv)
	for _, want := range []string{"**Rollout risk: medium.**", "`consolidate`", "`upload_failed` (proposed)", "3 unverifiable claims dropped"} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown lacks %q:\n%s", want, md)
		}
	}
}

// Triage leaves test hunks out, and the budget leaves out what does not fit;
// the prompt says which.
func TestPromptLeavesOut(t *testing.T) {
	in := input()
	in.Verdicts = map[string]triage.Verdict{HunkID("store_test.go", 0): {Runtime: 0.1, Op: triage.Tests}}
	in.Budget = DefaultBudget
	b := prompt(in)
	if strings.Contains(b.text, "TestConsolidate") || !strings.Contains(b.text, "store_test.go:1 (tests)") || b.omitted != 1 {
		t.Errorf("omitted %d:\n%s", b.omitted, b.text)
	}
	in.Verdicts = nil
	in.Budget = 10
	if b := prompt(in); b.omitted != 2 || !strings.Contains(b.text, "past the budget") {
		t.Errorf("omitted %d:\n%s", b.omitted, b.text)
	}
}

func TestCache(t *testing.T) {
	dir := t.TempDir()
	p := &Plan{Commit: "abc", Model: "tiny", Summary: "s"}
	if err := Save(dir, p); err != nil {
		t.Fatal(err)
	}
	if got, ok := Load(dir, "abc", "tiny"); !ok || got.Summary != "s" {
		t.Errorf("%+v %v", got, ok)
	}
	if _, ok := Load(dir, "abc", "other"); ok {
		t.Error("a plan by another model was taken")
	}
}
