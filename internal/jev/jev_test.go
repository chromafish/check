package jev

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// stub stands in for the service and records what it was sent.
func stub(t *testing.T, status int, body string) (*Client, *Request) {
	t.Helper()
	var got Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Errorf("request is not the documented shape: %v", err)
		}
		if auth := r.Header.Get("Authorization"); auth != "Bearer k" {
			t.Errorf("Authorization = %q, want the key as a bearer token", auth)
		}
		w.WriteHeader(status)
		io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return NewAt("k", srv.URL), &got
}

// The request carries the model, the state and every question, because a
// question the service does not receive is one the caller silently loses.
func TestAskSendsTheStateAndEveryQuestion(t *testing.T) {
	c, got := stub(t, 200, `{"answers":{"a":{"type":"noul","noul":0.9}}}`)
	_, err := c.Ask(context.Background(), map[string]string{"query": "q"}, map[string]Question{
		"a": Noul("is it so?"),
		"b": Choice("which one?", map[string]string{"x": "the first", "y": "the second"}),
	})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if got.Model != Model {
		t.Errorf("model = %q, want %q", got.Model, Model)
	}
	if len(got.Questions) != 2 {
		t.Fatalf("sent %d questions, want 2", len(got.Questions))
	}
	if got.Questions["a"].Criteria != nil {
		t.Error("a noul was sent with criteria, which it has no use for")
	}
	if got.Questions["b"].Criteria == nil {
		t.Error("a choice was sent without its options, so the model has nothing to pick from")
	}
	state, ok := got.State.(map[string]any)
	if !ok || state["query"] != "q" {
		t.Errorf("state = %#v, want the caller's named fields", got.State)
	}
}

// Every field of an answer is read back, since which ones are filled depends
// on the question and the caller reads them by type.
func TestAskReadsEveryKindOfAnswer(t *testing.T) {
	c, _ := stub(t, 200, `{"model":"jev-1.13.0","answers":{
		"n":{"type":"noul","noul":0.92},
		"c":{"type":"choice","choice":"x","probabilities":{"x":0.7,"y":0.3},"confidence":0.8}
	},"usage":{"input_tokens":312,"output_tokens":48}}`)
	resp, err := c.Ask(context.Background(), "s", map[string]Question{"n": Noul("?")})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if resp.Answers["n"].Noul != 0.92 {
		t.Errorf("noul = %v, want 0.92", resp.Answers["n"].Noul)
	}
	if resp.Answers["c"].Choice != "x" || resp.Answers["c"].Probabilities["y"] != 0.3 {
		t.Errorf("choice answer = %#v, want the winner and its distribution", resp.Answers["c"])
	}
	if resp.Usage.InputTokens != 312 {
		t.Errorf("input tokens = %d, want 312", resp.Usage.InputTokens)
	}
}

// A refusal keeps its status, because the caller answers a bad key and a busy
// service differently.
func TestAskKeepsTheStatusOfARefusal(t *testing.T) {
	for _, tc := range []struct {
		status int
		temp   bool
	}{
		{http.StatusUnauthorized, false},
		{http.StatusUnprocessableEntity, false},
		{http.StatusTooManyRequests, true},
		{529, true},
	} {
		c, _ := stub(t, tc.status, `{"error":"no"}`)
		_, err := c.Ask(context.Background(), "s", map[string]Question{"n": Noul("?")})
		var e *Error
		if !errors.As(err, &e) {
			t.Fatalf("status %d: error = %v, want a *jev.Error", tc.status, err)
		}
		if e.Status != tc.status {
			t.Errorf("status = %d, want %d", e.Status, tc.status)
		}
		if e.Temporary() != tc.temp {
			t.Errorf("status %d: Temporary() = %v, want %v", tc.status, e.Temporary(), tc.temp)
		}
	}
}

// The feature being off is the ordinary state, so a nil client answers rather
// than crashing the frame that called it.
func TestNilClientAnswersInsteadOfPanicking(t *testing.T) {
	var c *Client
	if _, err := c.Ask(context.Background(), "s", map[string]Question{"n": Noul("?")}); !errors.Is(err, ErrNoKey) {
		t.Errorf("err = %v, want ErrNoKey", err)
	}
}

func TestFromEnvIsNilWithoutAKey(t *testing.T) {
	t.Setenv(EnvKey, "")
	if FromEnv() != nil {
		t.Error("a client was built without a key")
	}
	t.Setenv(EnvKey, "k")
	if FromEnv() == nil {
		t.Error("no client was built although a key is set")
	}
}

// The environment wins over the stored key, so a key can be put in front of
// one run without the settings being touched.
func TestResolvePrefersTheEnvironment(t *testing.T) {
	t.Setenv(EnvKey, "from-env")
	if c := Resolve("from-settings"); c == nil || c.key != "from-env" {
		t.Errorf("key = %q, want the environment's", c.key)
	}
	t.Setenv(EnvKey, "")
	if c := Resolve("from-settings"); c == nil || c.key != "from-settings" {
		t.Errorf("key = %q, want the stored one", c.key)
	}
	if Resolve("") != nil {
		t.Error("a client was built with no key anywhere")
	}
}

// Too many options is caught before the request, since the service answers it
// with a 400 and the caller cannot tell that from any other refusal.
func TestAskRefusesMoreOptionsThanTheServiceTakes(t *testing.T) {
	c, got := stub(t, 200, `{"answers":{}}`)
	criteria := make(map[string]string, MaxChoices+1)
	for i := range MaxChoices + 1 {
		criteria[fmt.Sprintf("o%03d", i)] = "an option"
	}
	_, err := c.Ask(context.Background(), "s", map[string]Question{"c": Choice("which?", criteria)})
	if err == nil {
		t.Fatal("a question with too many options was sent")
	}
	if !strings.Contains(err.Error(), "255") {
		t.Errorf("err = %v, want it to name the limit", err)
	}
	if got.Model != "" {
		t.Error("the request went out anyway")
	}
	// The limit itself is allowed.
	delete(criteria, "o000")
	if _, err := c.Ask(context.Background(), "s", map[string]Question{"c": Choice("which?", criteria)}); err != nil {
		t.Errorf("exactly %d options was refused: %v", MaxChoices, err)
	}
}
