package ui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"gioui.org/io/key"

	"github.com/chromafish/check/internal/llm"
	"github.com/chromafish/check/internal/signals"
)

// newServiceRepo is a git repository whose newest commit adds a traced
// function, drops a log line, and adds code nothing observes.
func newServiceRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	dir := newGitRepo(t)
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("store.go", `package main

func Open() {
	slog.Info("opening")
}

func Old() {
	slog.Warn("old path taken")
}
`)
	commitAll(t, dir, "store")
	var far strings.Builder
	for range 30 {
		far.WriteString("\n")
	}
	write("store.go", `package main

func Open() {
	slog.Info("opening")
}

func Consolidate(ctx context.Context) {
	ctx, span := tracer.Start(ctx, "consolidate")
	defer span.End()
}
`+far.String()+`
func Upload() {
	send()
}
`)
	commitAll(t, dir, "Consolidate the store")
	return dir
}

// modelStub answers every request with reply, counting them.
func modelStub(t *testing.T, reply string) (*llm.Client, *atomic.Int32) {
	t.Helper()
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]string{"role": "assistant", "content": reply}}},
			"usage":   map[string]int{"prompt_tokens": 900, "completion_tokens": 120},
		})
	}))
	t.Cleanup(srv.Close)
	return llm.New(llm.Config{BaseURL: srv.URL, Model: "tiny"}), &n
}

func rowTexts(rows []planRow) string {
	var b strings.Builder
	for _, r := range rows {
		b.WriteString(r.text + " | " + r.right + "\n")
	}
	return b.String()
}

// A change opens on its plan view: its telemetry is read at once, and each
// item opens the diff at its line, which Esc comes back from.
func TestThePlanViewReadsTheTelemetry(t *testing.T) {
	h := openHarnessView(t, newServiceRepo(t), true)
	if h.app.inv == nil || h.app.inv.inv == nil {
		t.Fatal("no inventory")
	}
	inv := h.app.inv.inv
	if inv.Count(signals.Added) != 1 || inv.Count(signals.Removed) != 1 || len(inv.Dark) != 1 {
		t.Fatalf("inventory %+v", inv)
	}
	text := rowTexts(h.app.planRows(80))
	for _, want := range []string{"TELEMETRY", "added    span   consolidate | store.go:8", "removed  log    old path taken", "UNOBSERVED", "No model is named"} {
		if !strings.Contains(text, want) {
			t.Errorf("plan view lacks %q:\n%s", want, text)
		}
	}

	h.app.focus = PaneDiff
	h.press(key.NameReturn, 0)
	if !h.app.drilled || !h.app.classic() {
		t.Fatal("enter did not open the diff")
	}
	if r := h.app.diff.Row(h.app.diff.Cursor); r.Line.NewNum != 8 || h.app.diff.PathAt(h.app.diff.Cursor) != "store.go" {
		t.Errorf("cursor on %s:%d", h.app.diff.PathAt(h.app.diff.Cursor), r.Line.NewNum)
	}
	h.press(key.NameEscape, 0)
	if h.app.drilled || h.app.classic() {
		t.Error("esc did not come back to the plan")
	}
}

// M writes the plan with the model named; it is shown, copied as Markdown,
// and kept, so the change opened again costs nothing.
func TestWritingAPlan(t *testing.T) {
	dir := newServiceRepo(t)
	h := openHarnessView(t, dir, true)
	c, calls := modelStub(t, `{"summary":"Adds consolidation.","watch":[{"signal":"S1","expect":"appear","why":"new span"}],
		"gaps":[{"where":"D1","unseen":"upload failures","kind":"error","name":"upload_failed","snippet":"span.RecordError(err)"}],
		"regressions":[{"signal":"S1","proposed":false,"condition":"p99 over 2s"}],"rollout":{"risk":"medium","advice":"canary it"}}`)
	h.app.model = c
	h.app.focus = PaneDiff
	h.press("M", 0)
	h.settle()
	if h.app.pl == nil || h.app.pl.p == nil {
		t.Fatalf("no plan: %+v", h.app.pl)
	}
	text := rowTexts(h.app.planRows(80))
	for _, want := range []string{"Adds consolidation.", "ROLLOUT RISK MEDIUM", "WATCH", "span consolidate — expect appear", "GAPS", "upload failures", "REGRESSIONS", "written by tiny · 900 in, 120 out tokens"} {
		if !strings.Contains(text, want) {
			t.Errorf("plan view lacks %q:\n%s", want, text)
		}
	}
	h.press("Y", 0)
	if md := h.clipboard(); !strings.Contains(md, "## Observability plan") || !strings.Contains(md, "upload_failed") {
		t.Errorf("clipboard:\n%s", md)
	}

	// Opened again, in a fresh window, the kept plan is shown with no call.
	h2 := openHarnessViewKeeping(t, dir)
	h2.app.model = c
	h2.app.planOnScreen()
	if h2.app.pl == nil || h2.app.pl.p == nil || calls.Load() != 1 {
		t.Errorf("plan %+v after %d calls, want the kept one after one", h2.app.pl, calls.Load())
	}
}
