package ui

import (
	"context"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"sync"

	"github.com/chromafish/check/internal/jev"
)

// The brief is what jev makes of a change before anyone has read it.
//
// jev does not write prose: it picks among options it is given, says how
// likely something is, and scores. So what the brief knows about a change is
// decided by which questions it asks, and it asks in two steps. The first
// reads the whole change and says what kind of change it is — a feature, a
// fix, tests alone — and what it touches: secrets, storage, the network.
// Only the questions that fit are then asked of its lines, worded for any
// language, with the change's languages and files beside them. A change of
// tests is asked about tests; a rename is asked nothing.

// changeKind is one answer to "what kind of change is this".
type changeKind struct {
	id, label, desc string
}

var changeKinds = []changeKind{
	{"feature", "FEATURE", "adds new behaviour or a new capability"},
	{"fix", "FIX", "corrects a bug in existing behaviour"},
	{"refactor", "REFACTOR", "restructures code without meaning to change what it does"},
	{"tests", "TESTS", "adds or changes tests or test tooling, and nothing else"},
	{"deps", "DEPENDENCIES", "adds, removes or updates third-party dependencies"},
	{"config", "CONFIG", "changes configuration, build, CI or deployment files"},
	{"docs", "DOCS", "changes documentation or comments only"},
	{"mechanical", "MECHANICAL", "a rename, a reformat or generated code, with no change in what the program does"},
}

// quietKinds are the kinds nothing further is asked about: there is no line
// in a rename or a doc change that a review question points at.
var quietKinds = map[string]bool{"mechanical": true, "docs": true}

// briefTopic is something a change may touch.
type briefTopic struct {
	id, label, what string
}

var briefTopics = []briefTopic{
	{"concurrency", "CONCURRENCY", "state shared between threads, tasks or processes, or locks"},
	{"secrets", "SECRETS", "secrets, credentials, tokens or permissions"},
	{"input", "INPUT", "input from outside the program: requests, files, arguments or the environment"},
	{"storage", "STORAGE", "stored data that outlives the process: databases, files, object stores, caches"},
	{"network", "NETWORK", "network calls, or other I/O that can hang or fail"},
	{"api", "PUBLIC API", "an interface that other code or users depend on"},
	{"errors", "ERRORS", "how failures are reported or handled"},
	{"leftovers", "LEFTOVERS", "debug output, commented-out code, TODOs or temporary values"},
}

// briefCard is one question the brief can ask. It is asked when the change
// is one of its kinds or touches one of its topics.
type briefCard struct {
	id     string
	title  string
	ask    string
	kinds  []string
	topics []string
}

// briefBank is every question the brief can ask. Each is worded so that
// "nowhere" is a real answer, and for any language.
var briefBank = []briefCard{
	// What the change is for.
	{id: "behaviour", title: "BEHAVIOUR", kinds: []string{"feature", "fix", "refactor"},
		ask: "Where does behaviour that existing callers or users rely on change?"},
	{id: "fixed", title: "THE FIX", kinds: []string{"fix"},
		ask: "Where is the fault this change fixes actually corrected, rather than worked around?"},
	{id: "unchanged", title: "SAME BEHAVIOUR?", kinds: []string{"refactor"},
		ask: "Where might this restructuring change what the code does, not only how it is arranged?"},
	{id: "untested", title: "UNTESTED", kinds: []string{"feature", "fix"},
		ask: "Which new or changed logic has no test exercising it in this change?"},

	// A change of tests.
	{id: "vacuous", title: "TESTS THAT PASS ANYWAY", kinds: []string{"tests"},
		ask: "Where can a test pass without checking anything: skipped quietly when its setup is missing, or with no assertion on the result?"},
	{id: "flaky", title: "FLAKY", kinds: []string{"tests"},
		ask: "Where does a test depend on the network, the clock, ordering or shared state in a way that could make it fail at random?"},
	{id: "residue", title: "LEFT BEHIND", kinds: []string{"tests"}, topics: []string{"storage"},
		ask: "Where does a test create something outside itself — files, buckets, rows, processes — and not clean it up when it fails?"},

	// Dependencies and configuration.
	{id: "deps", title: "DEPENDENCIES", kinds: []string{"deps"},
		ask: "Which dependency change could alter behaviour, widen what the program trusts, or pin something insecure?"},
	{id: "defaults", title: "DEFAULTS", kinds: []string{"config"},
		ask: "Which setting changes a default that existing deployments or developers rely on?"},

	// What the change touches.
	{id: "races", title: "RACES", topics: []string{"concurrency"},
		ask: "Where could shared state be used by two threads or tasks at once without protection, or could a lock be held too long or never released?"},
	{id: "secrets", title: "SECRETS", topics: []string{"secrets"},
		ask: "Where is a secret or credential written into the code, logged, or given a default value?"},
	{id: "input", title: "UNCHECKED INPUT", topics: []string{"input"},
		ask: "Where is input from outside the program used without being checked?"},
	{id: "dataloss", title: "DATA LOSS", topics: []string{"storage"},
		ask: "Where is stored data deleted, overwritten or migrated in a way that could lose it?"},
	{id: "hangs", title: "HANGS", topics: []string{"network"},
		ask: "Where does the code wait on the network or other I/O with no timeout, retry or cancellation?"},
	{id: "breaking", title: "BREAKING", topics: []string{"api"},
		ask: "Where does an interface others depend on change in a way that breaks them?"},
	{id: "swallowed", title: "SWALLOWED ERRORS", topics: []string{"errors"},
		ask: "Where can a failure go unnoticed: an error ignored or discarded, or turned into a crash where it should be handled?"},
	{id: "leftovers", title: "LEFTOVERS", topics: []string{"leftovers"},
		ask: "Where is debug output, commented-out code, a TODO or a temporary value left in?"},
}

const (
	// briefKindMin and briefTopicMin are how sure the first step must be
	// that a change is of a kind, or touches a topic, before the questions
	// that go with it are asked.
	briefKindMin  = 0.3
	briefTopicMin = 0.5
	// briefMaxCards bounds how many questions are asked of one change: past
	// a handful, the brief is a checklist nobody reads.
	briefMaxCards = 5
	// briefHits is how many lines one question may point at.
	briefHits = 4
)

// briefHit is one line a question points at.
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
	kind      changeKind
	kindP     float64
	kinds     map[string]float64 // every kind's share
	topics    []briefTopic       // what the change touches, surest first
	languages []string
	asked     []briefCard
	cards     [][]briefHit // one list per asked question, best first
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

// startBrief asks about the diff now on screen, or takes the answers from
// the cache when the same change was asked about before. It runs whenever a
// diff finishes loading.
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
		b.res = &briefResult{}
		return
	}
	base := briefBase(a.files)
	c := a.jev
	b.running = true
	a.backgroundIn(context.Background(), semLimit, func(ctx context.Context) func() {
		res, err := runBrief(ctx, c, hunks, base)
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

// briefBase is what every question about a change is asked beside: its
// languages and its files.
func briefBase(files []FileRow) semState {
	var list strings.Builder
	for i, f := range files {
		if i == 200 {
			fmt.Fprintf(&list, "… and %d more\n", len(files)-i)
			break
		}
		fmt.Fprintf(&list, "%s %s +%d -%d\n", f.Status, f.Display(), f.Added, f.Removed)
	}
	return semState{Languages: strings.Join(languagesOf(files), ", "), Files: list.String()}
}

// languageNames names a file's language by its extension, for the questions'
// benefit: "Rust" tells the model more than ".rs".
var languageNames = map[string]string{
	".go": "Go", ".rs": "Rust", ".py": "Python", ".js": "JavaScript", ".jsx": "JavaScript",
	".ts": "TypeScript", ".tsx": "TypeScript", ".java": "Java", ".kt": "Kotlin", ".swift": "Swift",
	".c": "C", ".h": "C", ".cc": "C++", ".cpp": "C++", ".hpp": "C++", ".cs": "C#", ".rb": "Ruby",
	".php": "PHP", ".scala": "Scala", ".ex": "Elixir", ".exs": "Elixir", ".erl": "Erlang",
	".hs": "Haskell", ".ml": "OCaml", ".clj": "Clojure", ".lua": "Lua", ".zig": "Zig",
	".sh": "shell", ".bash": "shell", ".sql": "SQL", ".tf": "Terraform", ".nix": "Nix",
	".dart": "Dart", ".vue": "Vue", ".svelte": "Svelte",
}

// languagesOf is the change's languages, most changed lines first.
func languagesOf(files []FileRow) []string {
	lines := map[string]int{}
	for _, f := range files {
		if name, ok := languageNames[strings.ToLower(path.Ext(f.Path))]; ok {
			lines[name] += f.Added + f.Removed + 1
		}
	}
	names := make([]string, 0, len(lines))
	for n := range lines {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool {
		if lines[names[i]] != lines[names[j]] {
			return lines[names[i]] > lines[names[j]]
		}
		return names[i] < names[j]
	})
	return names
}

// dropBrief forgets the brief on screen, as a new change is selected.
func (a *App) dropBrief() {
	a.briefGen++
	a.brief = nil
	a.heat = nil
	a.marks = nil
	a.briefSel = 0
}

// briefError is the one line the brief shows when it failed.
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

// finding is one line the brief points at, under the question it answers
// best. A line more than one question landed on is listed once.
type finding struct {
	card int // index into the result's asked questions
	hit  briefHit
}

// findings groups the brief's lines by the question each answers best, in
// the order the questions were asked, and says which questions found nothing.
func (r *briefResult) findings() (groups [][]finding, empty []briefCard) {
	if r == nil {
		return nil, nil
	}
	type key struct {
		path     string
		old, new int
	}
	best := map[key]finding{}
	for i, hits := range r.cards {
		for _, h := range hits {
			k := key{h.path, h.at.old, h.at.new}
			if f, ok := best[k]; !ok || h.p > f.hit.p {
				best[k] = finding{card: i, hit: h}
			}
		}
	}
	groups = make([][]finding, len(r.asked))
	for _, f := range best {
		groups[f.card] = append(groups[f.card], f)
	}
	for i := range groups {
		sort.Slice(groups[i], func(a, b int) bool { return groups[i][a].hit.p > groups[i][b].hit.p })
		if len(groups[i]) == 0 {
			empty = append(empty, r.asked[i])
		}
	}
	return groups, empty
}

// updateHeat sums, per file, how strongly the brief points into it, and
// marks the lines it points at for the diff's gutter.
func (a *App) updateHeat() {
	a.heat, a.marks = nil, nil
	if a.brief == nil || a.brief.res == nil {
		return
	}
	a.heat = map[string]float64{}
	a.marks = map[markKey]bool{}
	groups, _ := a.brief.res.findings()
	for _, g := range groups {
		for _, f := range g {
			a.heat[f.hit.path] += f.hit.p
			a.marks[markKey{f.hit.path, f.hit.at.old, f.hit.at.new}] = true
		}
	}
}

// markKey names a line the brief points at, as the diff's gutter looks it up.
type markKey struct {
	path     string
	old, new int
}

// marked reports whether the brief points at a row of the diff.
func (a *App) marked(doc *DiffDoc, row int) bool {
	if len(a.marks) == 0 || doc == nil || row < 0 || row >= len(doc.Rows) {
		return false
	}
	r := doc.rowPtr(row)
	if r.Kind != rowLine {
		return false
	}
	return a.marks[markKey{doc.PathAt(row), r.Line.OldNum, r.Line.NewNum}]
}

// runBrief reads what kind of change this is, then asks it the questions
// that fit. It runs off the interface's goroutine and touches nothing on
// the App.
func runBrief(ctx context.Context, c *jev.Client, hunks []semHunk, base semState) (*briefResult, error) {
	res, err := classify(ctx, c, hunks, base)
	if err != nil {
		return nil, err
	}
	if quietKinds[res.kind.id] && res.kindP >= 0.5 {
		return res, nil
	}
	res.asked = chooseCards(res)
	if len(res.asked) == 0 {
		return res, nil
	}
	res.cards, err = askCards(ctx, c, hunks, base, res.asked)
	if err != nil {
		return nil, err
	}
	return res, nil
}

// classify is the first step: what kind of change, and what it touches, in
// one request over as much of the change as fits.
func classify(ctx context.Context, c *jev.Client, hunks []semHunk, base semState) (*briefResult, error) {
	var doc strings.Builder
	if all, ok := semWhole(hunks); ok {
		for _, c := range all {
			doc.WriteString(c.text)
			doc.WriteByte('\n')
		}
	} else {
		// Too large to read whole: its hunks, each by its header and first
		// lines, as far as the budget goes.
		for _, h := range hunks {
			var part strings.Builder
			part.WriteString(h.label)
			part.WriteByte('\n')
			for _, c := range h.cands[:min(len(h.cands), 6)] {
				part.WriteString(c.text)
				part.WriteByte('\n')
			}
			if doc.Len()+part.Len() > jev.Budget {
				break
			}
			doc.WriteString(part.String())
		}
	}
	criteria := make(map[string]string, len(changeKinds))
	for _, k := range changeKinds {
		criteria[k.id] = k.desc
	}
	questions := map[string]jev.Question{
		"kind": jev.Choice("`lines` is a code change, in the languages and files given. "+
			"What kind of change is it, taken as a whole?", criteria),
	}
	for _, t := range briefTopics {
		questions["t_"+t.id] = jev.Noul("Does this change add or alter code that deals with " + t.what + "?")
	}
	state := base
	state.Lines = doc.String()
	resp, err := c.Ask(ctx, state, questions)
	if err != nil {
		return nil, err
	}

	res := &briefResult{languages: strings.Split(base.Languages, ", ")}
	if base.Languages == "" {
		res.languages = nil
	}
	kind := resp.Answers["kind"]
	for _, k := range changeKinds {
		if p := kind.Probabilities[k.id]; p > res.kindP {
			res.kind, res.kindP = k, p
		}
	}
	type scored struct {
		t briefTopic
		p float64
	}
	var touched []scored
	for _, t := range briefTopics {
		if p := resp.Answers["t_"+t.id].Noul; p >= briefTopicMin {
			touched = append(touched, scored{t, p})
		}
	}
	sort.SliceStable(touched, func(i, j int) bool { return touched[i].p > touched[j].p })
	for _, s := range touched {
		res.topics = append(res.topics, s.t)
	}
	res.kinds = kind.Probabilities
	return res, nil
}

// chooseCards picks the questions that fit what the first step found, the
// best fitting first.
func chooseCards(res *briefResult) []briefCard {
	touched := map[string]bool{}
	for _, t := range res.topics {
		touched[t.id] = true
	}
	type scored struct {
		c     briefCard
		score float64
	}
	var picked []scored
	for _, card := range briefBank {
		score := 0.0
		for _, k := range card.kinds {
			if p := res.kinds[k]; p >= briefKindMin && p > score {
				score = p
			}
		}
		for _, t := range card.topics {
			if touched[t] {
				// A topic is a narrower fact than a kind, and a question
				// asked for one is the more pointed of the two.
				score = max(score, 0.9)
			}
		}
		if score > 0 {
			picked = append(picked, scored{card, score})
		}
	}
	sort.SliceStable(picked, func(i, j int) bool { return picked[i].score > picked[j].score })
	var cards []briefCard
	for _, p := range picked[:min(len(picked), briefMaxCards)] {
		cards = append(cards, p.c)
	}
	return cards
}

// askCards asks the chosen questions of the change's lines. A change that
// fits in one request is asked in one; a larger one is first ranked by hunk,
// all questions together, and then each question reads the lines of the
// hunks it chose, the questions in parallel.
func askCards(ctx context.Context, c *jev.Client, hunks []semHunk, base semState, cards []briefCard) ([][]briefHit, error) {
	out := make([][]briefHit, len(cards))
	if all, ok := semWhole(hunks); ok {
		got, _, err := semMulti(ctx, c, base, briefLineQs(cards), briefOpts(all), briefHits, nil)
		if err != nil {
			return nil, err
		}
		byID := briefIndex(all)
		for i, card := range cards {
			out[i] = briefFill(got[card.id], byID)
		}
		return out, nil
	}

	opts := make([]semOpt, len(hunks))
	index := make(map[string]int, len(hunks))
	for i, h := range hunks {
		id := fmt.Sprintf("H%03d", i+1)
		index[id] = i
		opts[i] = semOpt{id: id, desc: h.label, line: fmt.Sprintf("%s|%s (%d lines)", id, h.label, len(h.cands))}
	}
	qs := make([]semQ, len(cards))
	for i, card := range cards {
		qs[i] = semQ{
			id: card.id, present: card.id + "_present",
			choice: "Each option in `criteria` is a region of a code change, named by the file it is in, " +
				"the lines it covers and the function it falls inside. Which region best answers this " +
				"review question: " + card.ask,
			whether: "Does any region listed in `lines` answer this review question: " + card.ask +
				" Answer no if none does.",
		}
	}
	ranked, _, err := semMulti(ctx, c, base, qs, opts, 0, nil)
	if err != nil {
		return nil, err
	}

	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		first error
	)
	for i, card := range cards {
		lines := briefLinesOf(ranked[card.id], index, hunks)
		if len(lines) == 0 {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, _, err := semMulti(ctx, c, base, briefLineQs([]briefCard{card}), briefOpts(lines), briefHits, nil)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if first == nil {
					first = err
				}
				return
			}
			out[i] = briefFill(got[card.id], briefIndex(lines))
		}()
	}
	wg.Wait()
	if first != nil {
		return nil, first
	}
	return out, nil
}

// briefLineQs are the questions asked of lines.
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

// briefFill turns a question's ranked ids into the lines they name.
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

// briefLinesOf gathers the lines of a question's best hunks, as many as one
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
