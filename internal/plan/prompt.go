package plan

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/chromafish/check/internal/diffparse"
	"github.com/chromafish/check/internal/signals"
)

const system = `You are a site reliability engineer reviewing a code change before it ships.
Your job is to say how the change will be observed in production.

You are given the change's message, a list of the telemetry signals it adds, removes, alters or sits beside (each with an id like S3),
a list of changed code with no telemetry near it (each with an id like D2), and the diff itself.

Rules:
- In "watch", name only signals from the list, by their id. Say whether each should rise, fall, stay flat, appear or disappear once the change ships, and why, in one sentence.
- In "gaps", name only unobserved code from the list, by its id. Say what would go unseen, and suggest one signal to add: its kind (span, metric, log, event, error), its name, and a one-line snippet in the file's language.
- In "regressions", give conditions that would mean the change broke something. Refer to a signal by its id, or, when it is one you suggested in "gaps", by that suggested name with "proposed": true.
- Leave a list empty rather than invent anything. Do not mention signals or code that are not listed.
- "rollout" is the risk of shipping (low, medium, high) and one sentence of advice: behind a flag, as a canary, or as is.
- Be brief. The summary is two sentences at most: what this change does in production.`

var schema = map[string]any{
	"type":                 "object",
	"additionalProperties": false,
	"required":             []string{"summary", "watch", "gaps", "regressions", "rollout"},
	"properties": map[string]any{
		"summary": map[string]any{"type": "string"},
		"watch": array(map[string]any{
			"signal": str(),
			"expect": enum("rise", "fall", "flat", "appear", "disappear"),
			"why":    str(),
		}),
		"gaps": array(map[string]any{
			"where":   str(),
			"unseen":  str(),
			"kind":    enum("span", "metric", "log", "event", "error"),
			"name":    str(),
			"snippet": str(),
		}),
		"regressions": array(map[string]any{
			"signal":    str(),
			"proposed":  map[string]any{"type": "boolean"},
			"condition": str(),
		}),
		"rollout": object(map[string]any{
			"risk":   enum("low", "medium", "high"),
			"advice": str(),
		}),
	},
}

func str() map[string]any { return map[string]any{"type": "string"} }

func enum(vs ...string) map[string]any { return map[string]any{"type": "string", "enum": vs} }

func object(props map[string]any) map[string]any {
	req := make([]string, 0, len(props))
	for k := range props {
		req = append(req, k)
	}
	return map[string]any{"type": "object", "additionalProperties": false, "required": req, "properties": props}
}

func array(item map[string]any) map[string]any {
	return map[string]any{"type": "array", "items": object(item)}
}

// answer is the model's reply, before it is checked.
type answer struct {
	Summary string `json:"summary"`
	Watch   []struct {
		Signal string `json:"signal"`
		Expect string `json:"expect"`
		Why    string `json:"why"`
	} `json:"watch"`
	Gaps []struct {
		Where   string `json:"where"`
		Unseen  string `json:"unseen"`
		Kind    string `json:"kind"`
		Name    string `json:"name"`
		Snippet string `json:"snippet"`
	} `json:"gaps"`
	Regressions []struct {
		Signal    string `json:"signal"`
		Proposed  bool   `json:"proposed"`
		Condition string `json:"condition"`
	} `json:"regressions"`
	Rollout Rollout `json:"rollout"`
}

// built is a prompt and what its ids stand for.
type built struct {
	text    string
	signals map[string]signals.Found
	dark    map[string]signals.Dark
	omitted int
}

// prompt lays the change out for the model: the message, the signals and
// the unobserved code by id, then as much of the diff as the budget allows,
// what triage kept first.
func prompt(in Input) built {
	b := built{signals: map[string]signals.Found{}, dark: map[string]signals.Dark{}}
	var w strings.Builder
	fmt.Fprintf(&w, "## Change message\n%s\n\n", strings.TrimSpace(orNone(in.Message)))

	w.WriteString("## Signals\n")
	if in.Inv == nil || len(in.Inv.Signals) == 0 {
		w.WriteString("(none)\n")
	} else {
		for i, s := range in.Inv.Signals {
			id := "S" + strconv.Itoa(i+1)
			b.signals[id] = s
			name := s.Name
			if name == "" {
				name = "(unnamed)"
			}
			fmt.Fprintf(&w, "%s [%s %s] %q at %s:%d — %s\n", id, s.Status, s.Kind, name, s.Path, s.Line, clip(s.Text, 160))
		}
	}
	w.WriteString("\n## Changed code with no telemetry near it\n")
	if in.Inv == nil || len(in.Inv.Dark) == 0 {
		w.WriteString("(none)\n")
	} else {
		for i, d := range in.Inv.Dark {
			id := "D" + strconv.Itoa(i+1)
			b.dark[id] = d
			side := ""
			if d.Old {
				side = " (removed code)"
			}
			fmt.Fprintf(&w, "%s %s:%d%s, %d lines changed, starting: %s\n", id, d.Path, d.Line, side, d.Changed, clip(d.Text, 120))
		}
	}

	w.WriteString("\n## Diff\n")
	budget := in.Budget
	var left []string
	for _, f := range in.Files {
		for i, h := range f.Hunks {
			id := HunkID(f.Path(), i)
			if v, ok := in.Verdicts[id]; ok && v.Skip() {
				b.omitted++
				left = append(left, fmt.Sprintf("%s:%d (%s)", f.Path(), h.NewStart, v.Op))
				continue
			}
			text := hunkText(f, h)
			if len(text) > budget {
				b.omitted++
				left = append(left, fmt.Sprintf("%s:%d (past the budget)", f.Path(), h.NewStart))
				continue
			}
			budget -= len(text)
			w.WriteString(text)
		}
	}
	if len(left) > 0 {
		fmt.Fprintf(&w, "\nNot shown: %s\n", strings.Join(left, ", "))
	}
	b.text = w.String()
	return b
}

// hunkText is a hunk with each line numbered on the side it is on, so the
// model can see where it is.
func hunkText(f *diffparse.File, h diffparse.Hunk) string {
	var w strings.Builder
	fmt.Fprintf(&w, "--- %s  %s\n", f.Path(), strings.TrimSpace(h.Section))
	for _, l := range h.Lines {
		switch l.Kind {
		case diffparse.Added:
			fmt.Fprintf(&w, "+%5d| %s\n", l.NewNum, l.Text)
		case diffparse.Removed:
			fmt.Fprintf(&w, "-%5d| %s\n", l.OldNum, l.Text)
		default:
			fmt.Fprintf(&w, " %5d| %s\n", l.NewNum, l.Text)
		}
	}
	return w.String()
}

func orNone(s string) string {
	if strings.TrimSpace(s) == "" {
		return "(none)"
	}
	return s
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

var expects = map[string]bool{"rise": true, "fall": true, "flat": true, "appear": true, "disappear": true}
var kinds = map[string]bool{"span": true, "metric": true, "log": true, "event": true, "error": true}
var risks = map[string]bool{"low": true, "medium": true, "high": true}

// check keeps what the model said that refers to the change, and counts the
// rest.
func check(a answer, b built) *Plan {
	p := &Plan{Summary: strings.TrimSpace(a.Summary)}
	for _, w := range a.Watch {
		s, ok := b.signals[strings.TrimSpace(w.Signal)]
		if !ok || !expects[w.Expect] {
			p.Dropped++
			continue
		}
		p.Watch = append(p.Watch, Watch{Signal: s, Expect: w.Expect, Why: strings.TrimSpace(w.Why)})
	}
	proposed := map[string]bool{}
	for _, g := range a.Gaps {
		d, ok := b.dark[strings.TrimSpace(g.Where)]
		if !ok || !kinds[g.Kind] || strings.TrimSpace(g.Name) == "" {
			p.Dropped++
			continue
		}
		p.Gaps = append(p.Gaps, Gap{Path: d.Path, Line: d.Line, Old: d.Old, Unseen: strings.TrimSpace(g.Unseen),
			Kind: g.Kind, Name: strings.TrimSpace(g.Name), Snippet: strings.TrimSpace(g.Snippet)})
		proposed[strings.TrimSpace(g.Name)] = true
	}
	for _, r := range a.Regressions {
		name := strings.TrimSpace(r.Signal)
		if s, ok := b.signals[name]; ok && !r.Proposed {
			found := s
			label := s.Name
			if label == "" {
				label = s.Detector
			}
			p.Regressions = append(p.Regressions, Regression{Signal: label, Found: &found, Condition: strings.TrimSpace(r.Condition)})
			continue
		}
		if r.Proposed && proposed[name] {
			p.Regressions = append(p.Regressions, Regression{Signal: name, Proposed: true, Condition: strings.TrimSpace(r.Condition)})
			continue
		}
		p.Dropped++
	}
	p.Rollout = a.Rollout
	if !risks[p.Rollout.Risk] {
		p.Rollout.Risk = ""
	}
	return p
}
