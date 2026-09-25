package triage

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/chromafish/check/internal/jev"
)

// stub answers as if every hunk whose text mentions "test" were tests, and
// every other one a request handler that runs in production.
func stub(t *testing.T) (*jev.Client, *atomic.Int32) {
	t.Helper()
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		var req struct {
			State     map[string]string `json:"state"`
			Questions map[string]any    `json:"questions"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		parts := map[string]string{}
		for _, p := range strings.Split(req.State["hunks"], "=== ")[1:] {
			id, _, _ := strings.Cut(p, " ")
			parts[id] = p
		}
		answers := map[string]any{}
		for id := range req.Questions {
			kind, h, _ := strings.Cut(id, "_")
			test := strings.Contains(parts[h], "test")
			switch kind {
			case "run":
				p := 0.9
				if test {
					p = 0.1
				}
				answers[id] = map[string]any{"noul": p}
			case "op":
				c := string(Request)
				if test {
					c = string(Tests)
				}
				answers[id] = map[string]any{"choice": c, "probabilities": map[string]float64{c: 0.9}}
			}
		}
		json.NewEncoder(w).Encode(map[string]any{"answers": answers})
	}))
	t.Cleanup(srv.Close)
	return jev.NewAt("k", srv.URL), &n
}

func TestTriage(t *testing.T) {
	c, n := stub(t)
	var hunks []Hunk
	for i := range 30 {
		text := "+serve(req)"
		if i%2 == 1 {
			text = "+assert test passes"
		}
		hunks = append(hunks, Hunk{ID: fmt.Sprint(i), Label: "f.go", Text: text})
	}
	got, err := Run(context.Background(), c, hunks)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 30 {
		t.Fatalf("%d verdicts", len(got))
	}
	if v := got["0"]; v.Skip() || v.Op != Request {
		t.Errorf("hunk 0 = %+v", v)
	}
	if v := got["1"]; !v.Skip() || v.Op != Tests {
		t.Errorf("hunk 1 = %+v", v)
	}
	if n.Load() != 2 {
		t.Errorf("%d requests, want 30 hunks in two", n.Load())
	}
}
