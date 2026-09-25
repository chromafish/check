package plan

import (
	"fmt"
	"strings"

	"github.com/chromafish/check/internal/signals"
)

// Markdown is the plan written out for a pull request.
func Markdown(p *Plan, inv *signals.Inventory) string {
	var w strings.Builder
	w.WriteString("## Observability plan\n\n")
	if p.Summary != "" {
		w.WriteString(p.Summary + "\n\n")
	}
	if p.Rollout.Risk != "" || p.Rollout.Advice != "" {
		fmt.Fprintf(&w, "**Rollout risk: %s.** %s\n\n", orDash(p.Rollout.Risk), p.Rollout.Advice)
	}
	if inv != nil {
		fmt.Fprintf(&w, "Telemetry in this change: %d added, %d removed, %d altered; %d unobserved changes.\n\n",
			inv.Count(signals.Added), inv.Count(signals.Removed), inv.Count(signals.Altered), len(inv.Dark))
	}
	if len(p.Watch) > 0 {
		w.WriteString("### Watch\n\n")
		for _, x := range p.Watch {
			fmt.Fprintf(&w, "- **%s** `%s` (%s, `%s:%d`) — expect it to %s. %s\n",
				x.Signal.Kind, nameOf(x.Signal.Signal), x.Signal.Status, x.Signal.Path, x.Signal.Line, x.Expect, x.Why)
		}
		w.WriteString("\n")
	}
	if len(p.Gaps) > 0 {
		w.WriteString("### Gaps\n\n")
		for _, g := range p.Gaps {
			fmt.Fprintf(&w, "- `%s:%d` — %s Suggest a %s `%s`", g.Path, g.Line, g.Unseen, g.Kind, g.Name)
			if g.Snippet != "" {
				fmt.Fprintf(&w, ":\n  ```\n  %s\n  ```", g.Snippet)
			}
			w.WriteString("\n")
		}
		w.WriteString("\n")
	}
	if len(p.Regressions) > 0 {
		w.WriteString("### Regressions\n\n")
		for _, r := range p.Regressions {
			tag := ""
			if r.Proposed {
				tag = " (proposed)"
			}
			fmt.Fprintf(&w, "- `%s`%s: %s\n", r.Signal, tag, r.Condition)
		}
		w.WriteString("\n")
	}
	fmt.Fprintf(&w, "<sub>Written by %s", p.Model)
	if p.Dropped > 0 {
		fmt.Fprintf(&w, "; %d unverifiable claims dropped", p.Dropped)
	}
	w.WriteString(".</sub>\n")
	return w.String()
}

func nameOf(s signals.Signal) string {
	if s.Name != "" {
		return s.Name
	}
	return s.Detector
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}
