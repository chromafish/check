// Package triage asks jev which parts of a change run in production, so that
// the model writing a plan reads those and not the rest.
//
// jev answers typed questions with probabilities, cheaply and quickly, and
// this is a question of that shape: for each hunk, does it change what the
// program does when it runs, and what sort of operation is it part of. What
// jev says is test code, tooling or documentation is left out of the plan's
// prompt, which is the whole of what triage is for: a smaller prompt for a
// cheaper model.
package triage

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/chromafish/check/internal/jev"
)

// Op is the sort of operation a hunk is part of.
type Op string

const (
	Request  Op = "request"
	Job      Op = "job"
	Storage  Op = "storage"
	External Op = "external"
	Startup  Op = "startup"
	Tests    Op = "tests"
	Tooling  Op = "tooling"
	Docs     Op = "docs"
)

var ops = map[string]string{
	string(Request):  "serving a request or user action: HTTP or RPC handlers, CLI commands, UI event handling",
	string(Job):      "background or scheduled work: workers, queues, cron jobs, periodic tasks",
	string(Storage):  "reading or writing stored data: databases, files, object stores, caches, migrations",
	string(External): "calling another service or network resource",
	string(Startup):  "startup, configuration or wiring of the program",
	string(Tests):    "tests, test fixtures or test helpers",
	string(Tooling):  "build scripts, CI, linters, developer tooling",
	string(Docs):     "documentation or comments only",
}

// Hunk is one part of a change, as jev reads it.
type Hunk struct {
	ID    string // how the caller names it
	Label string // its file and place
	Text  string // its changed lines, with their +/- markers
}

// Verdict is what jev made of a hunk.
type Verdict struct {
	// Runtime is the probability that the hunk changes what the program
	// does when it runs in production.
	Runtime float64
	Op      Op
}

// Skip reports whether a hunk can be left out of a plan: it does not run in
// production, or jev is fairly sure it is tests, tooling or documentation.
func (v Verdict) Skip() bool {
	if v.Runtime < 0.2 {
		return true
	}
	return v.Op == Tests || v.Op == Tooling || v.Op == Docs
}

const (
	// perRequest is how many hunks one request asks about: two questions
	// each.
	perRequest = 24
	// hunkChars is how much of one hunk is sent.
	hunkChars = 800
	// parallel is how many requests are in flight at once.
	parallel = 4
)

// Run asks jev about every hunk, in batches within the service's limits, and
// returns a verdict per hunk ID. A hunk a failed batch covered has no
// verdict, and is read by the plan as it would be without triage.
func Run(ctx context.Context, c *jev.Client, hunks []Hunk) (map[string]Verdict, error) {
	var batches [][]Hunk
	var cur []Hunk
	used := 0
	for _, h := range hunks {
		cost := min(len(h.Text), hunkChars) + len(h.Label) + 16
		if len(cur) > 0 && (len(cur) >= perRequest || used+cost > jev.Budget) {
			batches = append(batches, cur)
			cur, used = nil, 0
		}
		cur = append(cur, h)
		used += cost
	}
	if len(cur) > 0 {
		batches = append(batches, cur)
	}

	out := map[string]Verdict{}
	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		first error
		sem   = make(chan struct{}, parallel)
	)
	for _, b := range batches {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			got, err := ask(ctx, c, b)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if first == nil {
					first = err
				}
				return
			}
			for id, v := range got {
				out[id] = v
			}
		}()
	}
	wg.Wait()
	if len(out) == 0 && first != nil {
		return nil, first
	}
	return out, nil
}

func ask(ctx context.Context, c *jev.Client, batch []Hunk) (map[string]Verdict, error) {
	var doc strings.Builder
	qs := map[string]jev.Question{}
	for i, h := range batch {
		id := fmt.Sprintf("H%d", i+1)
		text := h.Text
		if len(text) > hunkChars {
			text = text[:hunkChars] + "\n…"
		}
		fmt.Fprintf(&doc, "=== %s  %s\n%s\n", id, h.Label, text)
		qs["run_"+id] = jev.Noul("`hunks` holds parts of one code change, each headed by === and its id. " +
			"Does hunk " + id + " change what the program does when it runs in production, " +
			"rather than only its tests, tooling, comments or formatting?")
		qs["op_"+id] = jev.Choice("`hunks` holds parts of one code change, each headed by === and its id. "+
			"What sort of operation is the code hunk "+id+" changes part of?", ops)
	}
	resp, err := c.Ask(ctx, map[string]string{"hunks": doc.String()}, qs)
	if err != nil {
		return nil, err
	}
	out := map[string]Verdict{}
	for i, h := range batch {
		id := fmt.Sprintf("H%d", i+1)
		run, okRun := resp.Answers["run_"+id]
		op, okOp := resp.Answers["op_"+id]
		if !okRun || !okOp {
			continue
		}
		v := Verdict{Runtime: run.Noul, Op: Op(op.Choice)}
		if _, known := ops[op.Choice]; !known {
			v.Op = ""
		}
		out[h.ID] = v
	}
	return out, nil
}
