// Package llm asks a language model for structured answers over the OpenAI
// chat completions API, which local servers (Ollama, llama.cpp, LM Studio,
// vLLM) and hosted ones alike speak. Which model answers is the person's
// choice; this package only needs a base URL, a key and a name.
//
// Every answer is JSON matching a schema the caller gives. A server that
// constrains output to a schema is asked to; one that does not is asked for a
// JSON object with the schema written into the prompt; and an answer that
// still does not parse is sent back once to be repaired. Small models get
// JSON wrong often enough that each step earns its place.
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"time"
)

// Environment variables that override the stored configuration for one run.
const (
	EnvURL   = "CHECK_MODEL_URL"
	EnvKey   = "CHECK_MODEL_KEY"
	EnvModel = "CHECK_MODEL"
)

// DefaultURL is a local Ollama server's OpenAI-compatible endpoint.
const DefaultURL = "http://localhost:11434/v1"

// Config names a model.
type Config struct {
	BaseURL string
	Key     string
	Model   string
}

// Resolve is stored with the environment laid over it and the default base
// URL filled in. A config with no model is not usable: there is no sensible
// default model to guess.
func Resolve(stored Config) Config {
	c := stored
	if v := os.Getenv(EnvURL); v != "" {
		c.BaseURL = v
	}
	if v := os.Getenv(EnvKey); v != "" {
		c.Key = v
	}
	if v := os.Getenv(EnvModel); v != "" {
		c.Model = v
	}
	if c.BaseURL == "" {
		c.BaseURL = DefaultURL
	}
	c.BaseURL = strings.TrimRight(c.BaseURL, "/")
	return c
}

// Ready reports whether the config names a model to ask.
func (c Config) Ready() bool { return c.Model != "" }

// Usage is what a call cost, as the server counts it.
type Usage struct {
	Prompt     int `json:"prompt_tokens"`
	Completion int `json:"completion_tokens"`
}

func (u *Usage) add(o Usage) {
	u.Prompt += o.Prompt
	u.Completion += o.Completion
}

// Error is a request the server refused.
type Error struct {
	Status int
	Body   string
}

func (e *Error) Error() string { return fmt.Sprintf("model: %d: %s", e.Status, e.Body) }

// Client asks one model.
type Client struct {
	cfg  Config
	http *http.Client
	// noSchema is set once the server has refused schema-constrained output,
	// so it is not asked again.
	noSchema atomic.Bool
}

// New builds a client. Local models can be slow to answer a long prompt, so
// the limit is generous; the caller's context is the real bound.
func New(cfg Config) *Client {
	return &Client{cfg: cfg, http: &http.Client{Timeout: 5 * time.Minute}}
}

// Model is the name of the model the client asks.
func (c *Client) Model() string { return c.cfg.Model }

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type request struct {
	Model          string    `json:"model"`
	Messages       []message `json:"messages"`
	Temperature    float64   `json:"temperature"`
	ResponseFormat any       `json:"response_format,omitempty"`
}

type response struct {
	Choices []struct {
		Message message `json:"message"`
	} `json:"choices"`
	Usage Usage `json:"usage"`
}

// JSON asks the model, and decodes its answer into out, which must match
// schema, a JSON Schema object named name.
func (c *Client) JSON(ctx context.Context, system, user, name string, schema map[string]any, out any) (Usage, error) {
	var total Usage
	msgs := []message{{"system", system}, {"user", user}}

	var content string
	var err error
	if !c.noSchema.Load() {
		var u Usage
		content, u, err = c.chat(ctx, msgs, map[string]any{
			"type":        "json_schema",
			"json_schema": map[string]any{"name": name, "schema": schema, "strict": true},
		})
		total.add(u)
		var e *Error
		if err != nil && errors.As(err, &e) && (e.Status == http.StatusBadRequest || e.Status == http.StatusUnprocessableEntity) {
			c.noSchema.Store(true)
		} else if err != nil {
			return total, err
		}
	}
	if c.noSchema.Load() {
		raw, _ := json.MarshalIndent(schema, "", "  ")
		msgs[0].Content = system + "\n\nAnswer with a single JSON object, and nothing else, matching this JSON Schema:\n" + string(raw)
		var u Usage
		content, u, err = c.chat(ctx, msgs, map[string]any{"type": "json_object"})
		total.add(u)
		var e *Error
		if err != nil && errors.As(err, &e) && e.Status == http.StatusBadRequest {
			// Not even a JSON object mode: the prompt alone asks for it.
			content, u, err = c.chat(ctx, msgs, nil)
			total.add(u)
		}
		if err != nil {
			return total, err
		}
	}

	perr := decode(content, out)
	if perr == nil {
		return total, nil
	}
	// One repair: the answer, what was wrong with it, and a request for the
	// corrected object alone.
	msgs = append(msgs,
		message{"assistant", content},
		message{"user", "That is not valid JSON for the schema (" + perr.Error() + "). Reply with only the corrected JSON object."})
	content, u, err := c.chat(ctx, msgs, nil)
	total.add(u)
	if err != nil {
		return total, err
	}
	if err := decode(content, out); err != nil {
		return total, fmt.Errorf("model: the answer is not the JSON asked for: %w", err)
	}
	return total, nil
}

// chat makes one request and returns the answer's text.
func (c *Client) chat(ctx context.Context, msgs []message, format any) (string, Usage, error) {
	body, err := json.Marshal(request{Model: c.cfg.Model, Messages: msgs, ResponseFormat: format})
	if err != nil {
		return "", Usage{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", Usage{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.cfg.Key != "" {
		req.Header.Set("Authorization", "Bearer "+c.cfg.Key)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return "", Usage{}, fmt.Errorf("model: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return "", Usage{}, &Error{Status: resp.StatusCode, Body: strings.TrimSpace(string(msg))}
	}
	var r response
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&r); err != nil {
		return "", Usage{}, fmt.Errorf("model: decode: %w", err)
	}
	if len(r.Choices) == 0 {
		return "", r.Usage, fmt.Errorf("model: no answer")
	}
	return r.Choices[0].Message.Content, r.Usage, nil
}

// decode reads the JSON object in an answer, past any prose or code fence a
// model put around it.
func decode(content string, out any) error {
	s := strings.TrimSpace(content)
	if i, j := strings.IndexByte(s, '{'), strings.LastIndexByte(s, '}'); i >= 0 && j > i {
		s = s[i : j+1]
	}
	dec := json.NewDecoder(strings.NewReader(s))
	return dec.Decode(out)
}
