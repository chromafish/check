// Package jev asks TypeSafe's System One model a small set of typed questions
// about state the caller supplies, and hands back the answers as numbers.
//
// The model does not write prose. A question names the answers it will accept
// and comes back with a probability over them, which is why this is worth
// reaching for inside an interface: the result drops into a slice of row
// indices rather than into a pane someone has to read.
//
// There is no Go SDK, and the API is one POST, so this is the whole client.
package jev

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

// Endpoint is the System One request path. Model is the pinned alias; the
// service resolves it to whichever Jev is current.
const (
	Endpoint = "https://api.typesafe.ai/v1/systemone"
	Model    = "jev-latest"

	// EnvKey is the environment variable a key may be given in, which takes
	// precedence over the stored one.
	EnvKey = "CHECK_TYPESAFE_KEY"
)

// Budget is what one request may carry. The service allows 64k tokens per
// request and 32k for the state plus the longest question; this sits well
// under that, in characters, because the caller is assembling candidates out
// of a diff and needs a ceiling it can apply while it counts.
//
// Three characters to the token is about right for source code, so this is
// roughly 8k tokens: a third of the state allowance, which leaves room for
// the questions and for the estimate being wrong.
const Budget = 24_000

// MaxChoices is the most options a choice may offer. Past it the service
// refuses the request, so the caller has to divide its candidates and ask
// again rather than discover the limit as a 400.
const MaxChoices = 255

// Question is one judgment. Type is "noul", "choice" or "score".
//
// Instructions carry the whole meaning: the id a question is filed under is
// for the caller's benefit and is not sent to the model. Criteria defines the
// answers a choice or score will accept, and is omitted for a noul, whose
// answer is already the probability that its instruction holds.
type Question struct {
	Type         string `json:"type"`
	Instructions string `json:"instructions"`
	Criteria     any    `json:"criteria,omitempty"`
}

// Noul asks for the probability that something is so.
func Noul(instructions string) Question {
	return Question{Type: "noul", Instructions: instructions}
}

// Choice asks which of the given options is the one, where each option is
// named by a key and described by its value. The model cannot answer with an
// option that is not here, so the caller's candidate list is the whole of
// what it can say.
func Choice(instructions string, criteria map[string]string) Question {
	return Question{Type: "choice", Instructions: instructions, Criteria: criteria}
}

// Request is one call: the state every question is asked about, and the
// questions themselves, which are answered independently and in parallel.
type Request struct {
	State     any                 `json:"state"`
	Model     string              `json:"model"`
	Questions map[string]Question `json:"questions"`
}

// Answer is what came back for one question. Which fields are filled depends
// on the type: Noul for a noul, Choice and Probabilities for a choice, Score
// for a score. Confidence describes how peaked a choice or score distribution
// is, and says nothing about whether the answer is true.
type Answer struct {
	Type          string             `json:"type"`
	Noul          float64            `json:"noul"`
	Choice        string             `json:"choice"`
	Probabilities map[string]float64 `json:"probabilities"`
	Confidence    float64            `json:"confidence"`
	Score         float64            `json:"score"`
}

// Response holds one answer per question, under the ids they were asked with.
type Response struct {
	Model   string            `json:"model"`
	Answers map[string]Answer `json:"answers"`
	Usage   struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

// Error is a request the service refused. Status is kept because the caller's
// response differs by kind: a 401 is a key to fix and worth saying out loud,
// where a 429 or 529 is worth nothing more than falling back quietly.
type Error struct {
	Status int
	Body   string
}

func (e *Error) Error() string {
	return fmt.Sprintf("typesafe: %d: %s", e.Status, e.Body)
}

// Temporary reports whether the same request might succeed later.
func (e *Error) Temporary() bool {
	return e.Status == http.StatusTooManyRequests || e.Status >= 500
}

// Client talks to the service. The zero value is not usable; use New or
// FromEnv.
type Client struct {
	key      string
	endpoint string
	http     *http.Client
}

// New builds a client against the live endpoint.
func New(key string) *Client {
	return &Client{
		key:      key,
		endpoint: Endpoint,
		http:     &http.Client{Timeout: 15 * time.Second},
	}
}

// NewAt builds a client against a different endpoint, which is how a test
// stands a stub in front of the service.
func NewAt(key, endpoint string) *Client {
	c := New(key)
	c.endpoint = endpoint
	return c
}

// Resolve builds a client from the first key it finds, returning nil when
// there is none. A nil client is the ordinary state of the application — the
// feature is off — so every caller must handle it, and Ask on nil says so
// rather than panicking.
//
// The environment comes first so that a key can be put in front of one run
// without editing anything, and the settings hold the key someone keeps.
func Resolve(stored string) *Client {
	if key := os.Getenv(EnvKey); key != "" {
		return New(key)
	}
	if stored != "" {
		return New(stored)
	}
	return nil
}

// FromEnv is Resolve with nothing stored.
func FromEnv() *Client { return Resolve("") }

// ErrNoKey is what a nil client answers with.
var ErrNoKey = fmt.Errorf("typesafe: no API key in %s", EnvKey)

// Ask puts one request and returns its answers. State is marshalled as JSON,
// so a struct or a map with named fields is preferable to one long string
// when the state has several parts: the questions can then refer to them by
// name.
func (c *Client) Ask(ctx context.Context, state any, questions map[string]Question) (*Response, error) {
	if c == nil {
		return nil, ErrNoKey
	}
	if len(questions) == 0 {
		return nil, fmt.Errorf("typesafe: no questions")
	}
	// Caught here rather than at the service, so that the caller's own
	// division of its candidates is what is wrong and not the network.
	for id, q := range questions {
		if c, ok := q.Criteria.(map[string]string); ok && len(c) > MaxChoices {
			return nil, fmt.Errorf("typesafe: question %q offers %d options, the most is %d", id, len(c), MaxChoices)
		}
	}
	body, err := json.Marshal(Request{State: state, Model: Model, Questions: questions})
	if err != nil {
		return nil, fmt.Errorf("typesafe: encode: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.key)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("typesafe: %w", err)
	}
	defer resp.Body.Close()

	// The error body is bounded because it is only ever logged or shown in a
	// one line status: a service having a bad day should not be able to hand
	// the interface a megabyte to hold.
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return nil, &Error{Status: resp.StatusCode, Body: string(bytes.TrimSpace(msg))}
	}
	// An answer is a few numbers per question; a body past this is not one.
	var out Response
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return nil, fmt.Errorf("typesafe: decode: %w", err)
	}
	return &out, nil
}
