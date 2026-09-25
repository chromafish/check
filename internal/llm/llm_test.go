package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type seen struct {
	format string
	msgs   int
	auth   string
}

// server answers each request with the next of answers, refusing a
// response_format type named in refuse with a 400.
func server(t *testing.T, refuse map[string]bool, answers ...string) (*Client, func() []seen) {
	t.Helper()
	var (
		mu  sync.Mutex
		got []seen
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path %s", r.URL.Path)
		}
		var req struct {
			Messages       []json.RawMessage `json:"messages"`
			ResponseFormat struct {
				Type string `json:"type"`
			} `json:"response_format"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		mu.Lock()
		got = append(got, seen{req.ResponseFormat.Type, len(req.Messages), r.Header.Get("Authorization")})
		n := len(got)
		mu.Unlock()
		if refuse[req.ResponseFormat.Type] {
			http.Error(w, "response_format not supported", http.StatusBadRequest)
			return
		}
		answer := answers[min(n-1, len(answers)-1)]
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]string{"role": "assistant", "content": answer}}},
			"usage":   map[string]int{"prompt_tokens": 10, "completion_tokens": 5},
		})
	}))
	t.Cleanup(srv.Close)
	return New(Config{BaseURL: srv.URL + "/v1", Key: "k", Model: "m"}), func() []seen {
		mu.Lock()
		defer mu.Unlock()
		return append([]seen(nil), got...)
	}
}

var schema = map[string]any{"type": "object", "properties": map[string]any{"risk": map[string]any{"type": "string"}}}

type answer struct {
	Risk string `json:"risk"`
}

func TestSchemaConstrained(t *testing.T) {
	c, calls := server(t, nil, `{"risk":"low"}`)
	var a answer
	u, err := c.JSON(context.Background(), "sys", "user", "plan", schema, &a)
	if err != nil || a.Risk != "low" {
		t.Fatalf("%v %+v", err, a)
	}
	got := calls()
	if len(got) != 1 || got[0].format != "json_schema" || got[0].auth != "Bearer k" || u.Prompt != 10 {
		t.Errorf("calls %+v usage %+v", got, u)
	}
}

// A server without schema support is asked for a JSON object instead, and is
// not asked for a schema again.
func TestFallsBackToAJSONObject(t *testing.T) {
	c, calls := server(t, map[string]bool{"json_schema": true}, "", `{"risk":"high"}`, `{"risk":"med"}`)
	var a answer
	if _, err := c.JSON(context.Background(), "sys", "user", "plan", schema, &a); err != nil || a.Risk != "high" {
		t.Fatalf("%v %+v", err, a)
	}
	if _, err := c.JSON(context.Background(), "sys", "user", "plan", schema, &a); err != nil || a.Risk != "med" {
		t.Fatalf("%v %+v", err, a)
	}
	var formats []string
	for _, s := range calls() {
		formats = append(formats, s.format)
	}
	if strings.Join(formats, " ") != "json_schema json_object json_object" {
		t.Errorf("formats %v", formats)
	}
}

// An answer wrapped in prose parses; one that does not parse is sent back
// once for repair.
func TestRepairsABadAnswer(t *testing.T) {
	c, calls := server(t, nil, "Sure! ```json\n{\"risk\": \"low\",}\n```", `Here: {"risk":"low"}`)
	var a answer
	if _, err := c.JSON(context.Background(), "sys", "user", "plan", schema, &a); err != nil || a.Risk != "low" {
		t.Fatalf("%v %+v", err, a)
	}
	got := calls()
	if len(got) != 2 || got[1].msgs != 4 {
		t.Errorf("calls %+v, want the repair to carry the bad answer back", got)
	}
}

func TestResolve(t *testing.T) {
	t.Setenv(EnvURL, "")
	t.Setenv(EnvModel, "qwen")
	c := Resolve(Config{BaseURL: "http://x/v1/", Model: "stored"})
	if c.BaseURL != "http://x/v1" || c.Model != "qwen" || !c.Ready() {
		t.Errorf("%+v", c)
	}
	if c := Resolve(Config{}); c.BaseURL != DefaultURL {
		t.Errorf("default %q", c.BaseURL)
	}
}
