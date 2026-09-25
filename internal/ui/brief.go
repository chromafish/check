package ui

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/chromafish/check/internal/jev"
)

// The brief is what jev makes of a change before anyone has read it: a fixed
// set of review questions, asked the moment the diff is in, each answered
// with the lines it points at. It is the first thing the default view shows,
// so that a review starts from where to look rather than from the top.
//
// Every card is asked in the same request, over the same lines: the service
// answers the questions of one request in parallel, so six cards cost what
// one does.

// briefCard is one standing question.
type briefCard struct {
	id    string
	title string
	ask   string
}

// briefCards are the questions a review opens with. They are the ones a
// careful reviewer asks of any change, whatever it is for, and each is
// phrased so that "nowhere" is a real answer.
var briefCards = []briefCard{
	{"behaviour", "BEHAVIOUR", "Where does this change alter what existing callers or users observe?"},
	{"errors", "ERRORS", "Where is error handling added, removed or weakened?"},
	{"trust", "TRUST", "Where is untrusted input, a secret, a permission or a file path handled?"},
	{"state", "STATE", "Where are shared state, goroutines, locks or caches touched?"},
	{"tests", "TESTS", "Which changed logic has no matching change to a test?"},
	{"leftovers", "LEFTOVERS", "Where is debug output, commented-out code, a TODO or a hard-coded value left in?"},
}

const (
	// briefHits is how many lines a card lists.
	briefHits = 5
	// briefMechanical is the probability past which the change is called
	// mechanical and its cards are dimmed.
	briefMechanical = 0.6
)

// briefHit is one line a card points at.
type briefHit struct {
	path string
	at   lineSpot
	num  int    // the line's number on the side it is on
	text string // the diff line, marker first
	p    float64
}

// briefResult is everything the brief found. It holds places rather than
// rows, so it stays good for the same change across a reload and can be
// kept in the cache.
type briefResult struct {
	mechanical float64
	cards      [][]briefHit // one list per briefCards entry, best first
}

// brief is the brief of the change on screen, finished or not.
type brief struct {
	key     string
	doc     *DiffDoc
	running bool
	err     string
	res     *briefResult
}

// briefKey names what a brief was asked about. A rewritten commit, or a
// working copy whose files have moved on, is a different change and is asked
// about again.
func (a *App) briefKey() string {
	key := a.spec.Describe() + "@" + a.rev.CommitIDFull
	if a.rev.WorkingCopy {
		for _, f := range a.files {
			key += fmt.Sprintf("|%s+%d-%d", f.Path, f.Added, f.Removed)
		}
	}
	return key
}

// startBrief asks the standing questions about the diff now on screen, or
// takes the answers from the cache when the same change was asked about
// before. It runs whenever a diff finishes loading.
func (a *App) startBrief() {
	doc := a.diff
	if doc == nil || a.jev == nil {
		a.brief = nil
		return
	}
	key := a.briefKey()
	if a.brief != nil && a.brief.key == key && a.brief.doc == doc && (a.brief.running || a.brief.res != nil) {
		return
	}
	a.briefGen++
	gen := a.briefGen
	b := &brief{key: key, doc: doc}
	a.brief = b
	if res, ok := a.briefCache[key]; ok {
		b.res = res
		a.updateHeat()
		return
	}
	hunks := collectSem(doc)
	if len(hunks) == 0 {
		b.res = &briefResult{cards: make([][]briefHit, len(briefCards))}
		return
	}
	c := a.jev
	b.running = true
	a.backgroundIn(context.Background(), semLimit, func(ctx context.Context) func() {
		res, err := runBrief(ctx, c, hunks)
		return func() {
			if gen != a.briefGen {
				return
			}
			b.running = false
			if err != nil {
				b.err = briefError(err)
				return
			}
			if a.briefCache == nil {
				a.briefCache = map[string]*briefResult{}
			}
			a.briefCache[key] = res
			b.res = res
			a.updateHeat()
		}
	})
}

// dropBrief forgets the brief on screen, as a new change is selected. A
// request still running finishes into the cache's absence and is ignored.
func (a *App) dropBrief() {
	a.briefGen++
	a.brief = nil
	a.heat = nil
	a.briefSel = 0
}

// briefError is the one line a card area shows when the brief failed.
func briefError(err error) string {
	var e *jev.Error
	switch {
	case errors.As(err, &e) && (e.Status == 401 || e.Status == 403):
		return "key rejected — check it in settings"
	case errors.As(err, &e) && e.Temporary():
		return "jev is busy — R to try again"
	case errors.Is(err, context.DeadlineExceeded):
		return "jev took too long — R to try again"
	}
	return err.Error()
}

// updateHeat sums, per file, how strongly the cards point into it. The
// manifest draws it, so the files worth opening first are visible before
// any of them is.
func (a *App) updateHeat() {
	a.heat = nil
	if a.brief == nil || a.brief.res == nil {
		return
	}
	a.heat = map[string]float64{}
	for _, hits := range a.brief.res.cards {
		for _, h := range hits {
			a.heat[h.path] += h.p
		}
	}
}

// runBrief asks every card about the change. A change that fits in one
// request is asked about line by line in one; a larger one is first ranked
// by hunk, all cards together, and then each card reads the lines of the
// hunks it chose, the cards in parallel.
//
// It runs off the interface's goroutine and touches nothing on the App.
func runBrief(ctx context.Context, c *jev.Client, hunks []semHunk) (*briefResult, error) {
	extra := map[string]jev.Question{
		"mechanical": jev.Noul("Is this change mechanical — a rename, a reformat, a dependency bump or " +
			"generated code — with no change in what the program does?"),
	}
	res := &briefResult{cards: make([][]briefHit, len(briefCards))}

	if all, ok := semWhole(hunks); ok {
		got, extras, err := semMulti(ctx, c, "", briefLineQs(briefCards), briefOpts(all), briefHits, extra)
		if err != nil {
			return nil, err
		}
		res.mechanical = extras["mechanical"].Noul
		byID := briefIndex(all)
		for i, card := range briefCards {
			res.cards[i] = briefFill(got[card.id], byID)
		}
		return res, nil
	}

	// Rank hunks for every card at once.
	opts := make([]semOpt, len(hunks))
	index := make(map[string]int, len(hunks))
	for i, h := range hunks {
		id := fmt.Sprintf("H%03d", i+1)
		index[id] = i
		opts[i] = semOpt{id: id, desc: h.label, line: fmt.Sprintf("%s|%s (%d lines)", id, h.label, len(h.cands))}
	}
	qs := make([]semQ, len(briefCards))
	for i, card := range briefCards {
		qs[i] = semQ{
			id: card.id, present: card.id + "_present",
			choice: "Each option in `criteria` is a region of a code change, named by the file it is in, " +
				"the lines it covers and the function it falls inside. Which region best answers this " +
				"review question: " + card.ask,
			whether: "Does any region listed in `lines` answer this review question: " + card.ask +
				" Answer no if none does.",
		}
	}
	ranked, extras, err := semMulti(ctx, c, "", qs, opts, 0, extra)
	if err != nil {
		return nil, err
	}
	res.mechanical = extras["mechanical"].Noul

	// Then each card reads the lines of its own hunks.
	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		first error
	)
	for i, card := range briefCards {
		lines := briefLinesOf(ranked[card.id], index, hunks)
		if len(lines) == 0 {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, _, err := semMulti(ctx, c, "", briefLineQs([]briefCard{card}), briefOpts(lines), briefHits, nil)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if first == nil {
					first = err
				}
				return
			}
			res.cards[i] = briefFill(got[card.id], briefIndex(lines))
		}()
	}
	wg.Wait()
	if first != nil {
		return nil, first
	}
	return res, nil
}

// briefLineQs are the cards asked of lines.
func briefLineQs(cards []briefCard) []semQ {
	qs := make([]semQ, len(cards))
	for i, card := range cards {
		qs[i] = semQ{
			id: card.id, present: card.id + "_present",
			choice: "`lines` is a code change, one line per row, each row prefixed with its id and then " +
				"the +, - or space that says whether the line was added, removed or left alone. " +
				"Which line best answers this review question: " + card.ask,
			whether: "Does anything in `lines` answer this review question: " + card.ask +
				" Answer no if nothing does.",
		}
	}
	return qs
}

// briefOpts offers lines as the options of a choice.
func briefOpts(cands []semCand) []semOpt {
	opts := make([]semOpt, len(cands))
	for i, c := range cands {
		opts[i] = semOpt{id: c.id, desc: c.desc, line: c.id + "|" + c.text}
	}
	return opts
}

func briefIndex(cands []semCand) map[string]semCand {
	m := make(map[string]semCand, len(cands))
	for _, c := range cands {
		m[c.id] = c
	}
	return m
}

// briefFill turns a card's ranked ids into the lines they name.
func briefFill(scores []semScore, byID map[string]semCand) []briefHit {
	var hits []briefHit
	for _, s := range scores {
		c, ok := byID[s.id]
		if !ok {
			continue
		}
		num := c.at.new
		if c.text != "" && c.text[0] == '-' {
			num = c.at.old
		}
		hits = append(hits, briefHit{path: c.path, at: c.at, num: num, text: c.text, p: s.p})
	}
	return hits
}

// briefLinesOf gathers the lines of a card's best hunks, as many as one
// request carries. A hunk too large to send whole is sent as its first part.
func briefLinesOf(scores []semScore, index map[string]int, hunks []semHunk) []semCand {
	var lines []semCand
	used := 0
	for _, s := range scores {
		i, ok := index[s.id]
		if !ok {
			continue
		}
		for _, c := range hunks[i].cands {
			cost := len(c.id) + len(c.text) + len(c.desc) + 4
			if len(lines) >= jev.MaxChoices || used+cost > jev.Budget {
				return lines
			}
			lines = append(lines, c)
			used += cost
		}
	}
	return lines
}

// briefRow resolves a hit to the row it is on in doc, or -1.
func (d *DiffDoc) briefRow(h briefHit) int {
	for i, fd := range d.Files {
		if fd.Path == h.path {
			s := h.at
			s.file = i
			return d.findLine(s)
		}
	}
	return -1
}
