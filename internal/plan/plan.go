// Package plan writes how to observe a change in production: what to watch
// once it ships, where it runs unseen, and what a regression would look like.
//
// A model writes the plan, from the change, its message and the inventory of
// its telemetry (package signals). The model is told the signals by id and
// the unobserved code by id, and answers with those ids; what it answers is
// then checked against them. A signal the inventory does not have, or a place
// the change does not touch, is dropped rather than shown, so a small model
// can be used without its inventions reaching the reader.
package plan

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/chromafish/check/internal/diffparse"
	"github.com/chromafish/check/internal/llm"
	"github.com/chromafish/check/internal/signals"
	"github.com/chromafish/check/internal/triage"
)

// Plan is how to observe one change.
type Plan struct {
	Commit      string       `json:"commit"`
	Model       string       `json:"model"`
	Created     time.Time    `json:"created"`
	Summary     string       `json:"summary"`
	Watch       []Watch      `json:"watch"`
	Gaps        []Gap        `json:"gaps"`
	Regressions []Regression `json:"regressions"`
	Rollout     Rollout      `json:"rollout"`
	// Dropped counts what the model said that did not check out.
	Dropped int `json:"dropped"`
	// Omitted counts the hunks triage left out of what the model read.
	Omitted int       `json:"omitted"`
	Usage   llm.Usage `json:"usage"`
}

// Watch is an existing signal that should move, or hold, once the change
// ships.
type Watch struct {
	Signal signals.Found `json:"signal"`
	Expect string        `json:"expect"` // rise, fall, flat, appear, disappear
	Why    string        `json:"why"`
}

// Gap is somewhere the change runs unobserved, and what would observe it.
type Gap struct {
	Path    string `json:"path"`
	Line    int    `json:"line"`
	Old     bool   `json:"old"`
	Unseen  string `json:"unseen"`
	Kind    string `json:"kind"`
	Name    string `json:"name"`
	Snippet string `json:"snippet"`
}

// Regression is a condition on a signal that would mean the change broke
// something. A proposed one is on a signal a gap suggests adding.
type Regression struct {
	Signal    string         `json:"signal"`
	Found     *signals.Found `json:"found,omitempty"`
	Proposed  bool           `json:"proposed"`
	Condition string         `json:"condition"`
}

// Rollout is how carefully the change should be shipped.
type Rollout struct {
	Risk   string `json:"risk"` // low, medium, high
	Advice string `json:"advice"`
}

// Backend reads telemetry once a change is deployed: the values of a signal
// over a window. Integrations implement it; the plan's regressions are what
// they are asked about.
type Backend interface {
	Name() string
	Query(ctx context.Context, s signals.Signal, since, until time.Time) ([]Point, error)
}

// Point is one value of a signal at a time.
type Point struct {
	At    time.Time
	Value float64
}

// Input is everything a plan is written from.
type Input struct {
	Commit   string
	Message  string
	Files    []*diffparse.File
	Inv      *signals.Inventory
	Verdicts map[string]triage.Verdict // by HunkID; nil without triage
	// Budget is how many characters of the change the prompt may carry.
	Budget int
}

// DefaultBudget keeps a prompt to roughly 8k tokens of diff, which a small
// local model can still hold with room to answer.
const DefaultBudget = 24_000

// HunkID names the i'th hunk of a file.
func HunkID(path string, i int) string { return path + "#" + strconv.Itoa(i) }

// Hunks is every hunk of a change, as triage reads them.
func Hunks(files []*diffparse.File) []triage.Hunk {
	var out []triage.Hunk
	for _, f := range files {
		for i, h := range f.Hunks {
			var b strings.Builder
			for _, l := range h.Lines {
				switch l.Kind {
				case diffparse.Added:
					b.WriteString("+" + l.Text + "\n")
				case diffparse.Removed:
					b.WriteString("-" + l.Text + "\n")
				}
			}
			label := fmt.Sprintf("%s:%d %s", f.Path(), h.NewStart, strings.TrimSpace(h.Section))
			out = append(out, triage.Hunk{ID: HunkID(f.Path(), i), Label: label, Text: b.String()})
		}
	}
	return out
}

// Write asks the model for a plan and checks what it says.
func Write(ctx context.Context, c *llm.Client, in Input) (*Plan, error) {
	if in.Budget == 0 {
		in.Budget = DefaultBudget
	}
	p := prompt(in)
	var raw answer
	u, err := c.JSON(ctx, system, p.text, "observability_plan", schema, &raw)
	if err != nil {
		return nil, err
	}
	pl := check(raw, p)
	pl.Commit = in.Commit
	pl.Model = c.Model()
	pl.Created = time.Now()
	pl.Usage = u
	pl.Omitted = p.omitted
	return pl, nil
}
