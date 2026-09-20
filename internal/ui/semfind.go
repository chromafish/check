package ui

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"gioui.org/layout"

	"github.com/chromafish/check/internal/diffparse"
	"github.com/chromafish/check/internal/jev"
)

// Semantic find asks the model which lines of the change answer a question,
// instead of which lines contain a string. The query is the same field and
// the result is the same list of rows, so everything downstream of it —
// n and N, the highlight, the scroll — is untouched.
//
// It runs in two stages because a change is far larger than one request may
// carry, and because the thing that stays small as a diff grows is the number
// of hunks, not the number of lines: sixty files came to eighty hunks in this
// repository's own history. The first stage ranks hunks by their header,
// which already names the function they fall in; the second ranks the lines
// of the hunks that won.
//
// A change small enough to send whole skips the first stage, which is the
// common case and the fast one.

// askMode is carried on the App rather than spelled as a prefix in the field
// itself. A prefix has to be typed into a bar that is not yet open, collides
// with the help sheet, and leaves the caret somewhere the next keystroke can
// land in front of it. The bar knows what it is for instead, and the field
// holds nothing but the question.

const (
	// semPresentMin is the probability below which the change is taken not to
	// contain what was asked about. A choice distribution always sums to one
	// and so always has a winner; this is the separate judgment that says
	// whether the winner means anything.
	semPresentMin = 0.35
	// semHitMin is the share of the distribution a line must hold to be worth
	// stopping at.
	semHitMin = 0.05
	// semMaxHits bounds the list, which someone walks with n one at a time.
	semMaxHits = 25
	// semTextMax is the most of one line or hunk label that is sent. A
	// minified file is one line as long as a request may be, and a line that
	// long is not read for its end anyway.
	semTextMax = 200
	// semMaxRequests bounds how many requests one stage may divide into, so
	// that a change of any size costs a bounded number of round trips.
	semMaxRequests = 8
	// semLimit is how long one question may take in all, across its stages.
	semLimit = 60 * time.Second
)

// semCand is one line offered to the model, and the row it came from.
type semCand struct {
	id   string
	row  int
	at   lineSpot // row, as a place that survives the rows being rebuilt
	text string   // the diff line, with its +/- marker
	desc string   // how the option is described in criteria
}

// semHunk is one hunk's worth of candidates, with the label the first stage
// ranks it by.
type semHunk struct {
	path  string
	label string // "path  @@ -1,2 +1,2 @@  func foo()"
	cands []semCand
	chars int // roughly what the candidates cost to send
}

// semState is what the model is asked about. The fields are named so that a
// question can refer to them.
type semState struct {
	Query string `json:"query"`
	Lines string `json:"lines"`
}

// collectSem gathers the change's hunks and their lines. Only rowLine rows
// are candidates: a heading or a gap is not somewhere a cursor should land.
func collectSem(d *DiffDoc) []semHunk {
	if d == nil {
		return nil
	}
	var hunks []semHunk
	n := 0
	for i := range d.Rows {
		r := d.rowPtr(i)
		switch r.Kind {
		case rowHunk:
			hunks = append(hunks, semHunk{path: d.PathAt(i), label: semClip(d.PathAt(i) + "  " + r.Text)})
		case rowLine:
			if len(hunks) == 0 {
				continue
			}
			h := &hunks[len(hunks)-1]
			n++
			c := semCand{
				id:   fmt.Sprintf("L%04d", n),
				row:  i,
				at:   d.lineSpot(i),
				text: semMark(r.Line) + semClip(r.Line.Text),
				desc: semDesc(d.PathAt(i), r.Line),
			}
			h.cands = append(h.cands, c)
			h.chars += len(c.id) + len(c.text) + len(c.desc) + 4
		}
	}
	// A hunk with no lines under it — a binary file, a pure rename — is not
	// something either stage can say anything about.
	kept := hunks[:0]
	for _, h := range hunks {
		if len(h.cands) > 0 {
			kept = append(kept, h)
		}
	}
	return kept
}

// lineSpot names one diff line by its file and its number on each side,
// which a note filed or a gap opened anywhere in the change leaves alone.
type lineSpot struct {
	file     int
	old, new int
}

func (d *DiffDoc) lineSpot(row int) lineSpot {
	r := d.rowPtr(row)
	return lineSpot{file: int(d.Rows[row].file), old: r.Line.OldNum, new: r.Line.NewNum}
}

// findLine is lineSpot run backwards, or -1 when the line is gone.
func (d *DiffDoc) findLine(s lineSpot) int {
	if s.file < 0 || s.file >= len(d.FileRows) {
		return -1
	}
	start, end := d.rowRange(s.file)
	for i := start; i < end; i++ {
		if r := d.rowPtr(i); r.Kind == rowLine && r.Line.OldNum == s.old && r.Line.NewNum == s.new {
			return i
		}
	}
	return -1
}

// semClip cuts s to semTextMax bytes without splitting a character.
func semClip(s string) string {
	if len(s) <= semTextMax {
		return s
	}
	cut := semTextMax
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

// semMark is the character a diff prints a line with.
func semMark(l diffparse.Line) string {
	switch l.Kind {
	case diffparse.Added:
		return "+"
	case diffparse.Removed:
		return "-"
	default:
		return " "
	}
}

// semDesc describes one line as an option. The line's own text is already in
// the state document under its id, so this is a locator rather than a second
// copy of it: sending the text twice roughly doubles what a request costs,
// and whether it buys anything is worth measuring before paying for it.
func semDesc(path string, l diffparse.Line) string {
	num := l.NewNum
	side := "after"
	if l.Kind == diffparse.Removed {
		num, side = l.OldNum, "before"
	}
	return fmt.Sprintf("%s:%d (%s)", path, num, side)
}

// startAsk opens the find bar as a question, which is the only way into
// asking: there is no prefix to type, and the bar says which of the two it is.
func (a *App) startAsk(gtx layout.Context) {
	if a.findField == nil {
		return
	}
	if a.jev == nil {
		a.note("asking needs a key: %s, or typesafe_key in settings", jev.EnvKey)
		return
	}
	a.focus = PaneDiff
	a.findOpen = true
	if !a.askMode {
		// Coming from find, whatever was being matched is not a question.
		a.findField.SetText("")
		a.findHits, a.findAt = nil, -1
		a.dropAsk()
	}
	a.askMode = true
	a.findField.Placeholder = askPlaceholder
	a.findField.Focus(gtx)
}

// dropAsk forgets the question in flight and what the last one found. A
// request still running is left to finish, and its answer is thrown away.
func (a *App) dropAsk() {
	a.askGen++
	a.semRunning = false
	a.askDoc, a.askSpots = nil, nil
}

// askHits puts the places a question found back onto the rows as they stand.
// The rows are renumbered whenever a note is filed or a gap opened, and the
// places are not, so the hits follow the lines they were found on.
func (a *App) askHits() {
	if a.diff == nil || a.diff != a.askDoc {
		a.findHits, a.findAt = nil, -1
		a.askDoc, a.askSpots = nil, nil
		return
	}
	hits := make([]int, 0, len(a.askSpots))
	for _, s := range a.askSpots {
		if row := a.diff.findLine(s); row >= 0 {
			hits = append(hits, row)
		}
	}
	// A line can only go if its file was reloaded with different contents,
	// and then the hits still standing are the ones to keep.
	if len(hits) != len(a.askSpots) {
		a.askSpots = slices.DeleteFunc(a.askSpots, func(s lineSpot) bool { return a.diff.findLine(s) < 0 })
		a.findAt = -1
	}
	a.findHits = hits
	if a.findAt >= len(hits) {
		a.findAt = -1
	}
}

// semFind runs the search and posts its result to a later frame.
func (a *App) semFind(query string) {
	// The client is taken here, on the interface's goroutine: a key adopted
	// while the request runs replaces a.jev, and the request should not see
	// it change underneath it.
	c := a.jev
	if c == nil {
		a.note("asking needs a key: %s, or typesafe_key in settings", jev.EnvKey)
		return
	}
	doc := a.diff
	hunks := collectSem(doc)
	if len(hunks) == 0 {
		a.note("nothing to search")
		return
	}
	a.dropAsk()
	gen := a.askGen
	a.semRunning = true
	a.note("asking…")
	a.backgroundIn(context.Background(), semLimit, func(ctx context.Context) func() {
		cands, err := semSearch(ctx, c, query, hunks)
		return func() {
			// A question closed, replaced or asked again since is not the one
			// on screen, and its answer is not an answer to anything.
			if gen != a.askGen {
				return
			}
			a.semRunning = false
			// The change may have been swapped underneath the request. The
			// places are in the document that produced them, so applying
			// them to a different one would scroll to nonsense.
			if a.diff != doc || !a.askMode {
				return
			}
			if err != nil {
				a.semFail(err)
				return
			}
			if len(cands) == 0 {
				a.findHits, a.findAt = nil, -1
				a.note("no answer in this change")
				return
			}
			// Navigation order is the order of the change, so that n reads
			// downward the way the diff does; the cursor still starts on the
			// line the model ranked first.
			best := cands[0].id
			ordered := slices.Clone(cands)
			sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].row < ordered[j].row })
			a.askDoc = doc
			a.askSpots = make([]lineSpot, len(ordered))
			a.findAt = 0
			for i, c := range ordered {
				a.askSpots[i] = c.at
				if c.id == best {
					a.findAt = i
				}
			}
			a.askHits()
			if a.findAt < 0 || a.findAt >= len(a.findHits) {
				return
			}
			row := a.findHits[a.findAt]
			a.diff.Cursor = row
			a.focus = PaneDiff
			a.scrollTo(row)
			a.note("find %d/%d", a.findAt+1, len(a.findHits))
		}
	})
}

// semFail reports what went wrong and leaves find as it was. A key that does
// not work is worth saying plainly, because nothing else will tell anyone;
// a service that is busy is not worth more than a line.
func (a *App) semFail(err error) {
	var e *jev.Error
	if errors.As(err, &e) {
		switch {
		case e.Status == 401 || e.Status == 403:
			a.note("semantic find: key rejected")
		case e.Temporary():
			a.note("semantic find unavailable, try again")
		default:
			a.note("semantic find: %s", e)
		}
		return
	}
	if errors.Is(err, context.DeadlineExceeded) {
		a.note("semantic find timed out, try again")
		return
	}
	a.note("semantic find failed: %v", err)
}

// semSearch narrows the change until the lines left can be offered as one
// choice, then asks which of them answers the question.
//
// How many stages that takes depends on the change, and each is skipped when
// what is left already fits. Two limits decide: a request may carry only so
// much, and a choice may offer only so many options — the second is the one
// a diff reaches first, since a line is short and there are very many of them.
//
// It runs off the interface's goroutine, so it touches nothing on the App.
func semSearch(ctx context.Context, c *jev.Client, query string, hunks []semHunk) ([]semCand, error) {
	// A change with more hunks than a choice can offer is narrowed to a file
	// first. Files are fewer than hunks by construction.
	if len(hunks) > jev.MaxChoices {
		picked, err := semFiles(ctx, c, query, hunks)
		if err != nil || len(picked) == 0 {
			return nil, err
		}
		hunks = picked
	}
	if all, ok := semWhole(hunks); ok {
		return semLines(ctx, c, query, all)
	}
	picked, err := semHunks(ctx, c, query, hunks)
	if err != nil || len(picked) == 0 {
		return nil, err
	}
	// One hunk too large to offer whole — a generated file, a block pasted
	// in — is divided into windows and narrowed again.
	if len(picked[0].cands) > jev.MaxChoices || picked[0].chars > jev.Budget {
		lines, err := semWindow(ctx, c, query, picked[0])
		if err != nil || len(lines) == 0 {
			return nil, err
		}
		return semLines(ctx, c, query, lines)
	}
	var lines []semCand
	used := 0
	for _, h := range picked {
		if len(lines) > 0 && (used+h.chars > jev.Budget || len(lines)+len(h.cands) > jev.MaxChoices) {
			break
		}
		lines = append(lines, h.cands...)
		used += h.chars
	}
	return semLines(ctx, c, query, lines)
}

// semWhole is every candidate in the hunks, and whether they can be asked
// about in one go.
func semWhole(hunks []semHunk) ([]semCand, bool) {
	n, chars := 0, 0
	for _, h := range hunks {
		n += len(h.cands)
		chars += h.chars
	}
	if n > jev.MaxChoices || chars > jev.Budget {
		return nil, false
	}
	all := make([]semCand, 0, n)
	for _, h := range hunks {
		all = append(all, h.cands...)
	}
	return all, true
}

// semOpt is one option of a choice: the id it is filed under, how criteria
// describes it, and the row the state document lists it on.
type semOpt struct{ id, desc, line string }

// semChoose offers opts as one choice, divided into as many requests as the
// limits on options and size make it take, and returns the ids that answer
// query, best first. Each request carries its own presence question, and a
// part of the change that does not contain an answer contributes nothing,
// however its own distribution fell.
func semChoose(ctx context.Context, c *jev.Client, query, qid, instr, present string, opts []semOpt, most int) ([]string, error) {
	var batches [][]semOpt
	from, chars := 0, 0
	for i, o := range opts {
		cost := len(o.id)*2 + len(o.desc) + len(o.line) + 8
		if i > from && (i-from >= jev.MaxChoices || chars+cost > jev.Budget) {
			batches = append(batches, opts[from:i])
			from, chars = i, 0
		}
		chars += cost
	}
	if from < len(opts) {
		batches = append(batches, opts[from:])
	}
	if len(batches) > semMaxRequests {
		batches = batches[:semMaxRequests]
	}

	probs := map[string]float64{}
	for _, b := range batches {
		criteria := make(map[string]string, len(b))
		var doc strings.Builder
		for _, o := range b {
			criteria[o.id] = o.desc
			doc.WriteString(o.line)
			doc.WriteByte('\n')
		}
		resp, err := c.Ask(ctx, semState{Query: query, Lines: doc.String()}, map[string]jev.Question{
			qid:       jev.Choice(instr, criteria),
			"present": jev.Noul(present),
		})
		if err != nil {
			return nil, err
		}
		if resp.Answers["present"].Noul < semPresentMin {
			continue
		}
		for id, p := range resp.Answers[qid].Probabilities {
			// The model answers only with options it was offered, but that is
			// its promise and not something to index by unchecked.
			if _, ok := criteria[id]; ok {
				probs[id] = p
			}
		}
	}
	return semRank(probs, semHitMin, most), nil
}

// semFiles narrows a change of very many hunks to the files worth reading,
// keeping the hunks of the files that won.
func semFiles(ctx context.Context, c *jev.Client, query string, hunks []semHunk) ([]semHunk, error) {
	var order []string
	count := map[string]int{}
	for _, h := range hunks {
		if _, seen := count[h.path]; !seen {
			order = append(order, h.path)
		}
		count[h.path] += len(h.cands)
	}
	opts := make([]semOpt, len(order))
	index := make(map[string]string, len(order))
	for i, path := range order {
		id := fmt.Sprintf("F%03d", i+1)
		index[id] = path
		opts[i] = semOpt{id: id, desc: semClip(path), line: fmt.Sprintf("%s|%s (%d changed lines)", id, semClip(path), count[path])}
	}
	ranked, err := semChoose(ctx, c, query, "file",
		"Each option in `criteria` is the path of a file this change touches. "+
			"Which file is the one that answers `query`?",
		"Do the files listed in `lines` include one that answers `query`? "+
			"Answer no if `query` is about something this change does not touch.",
		opts, 0)
	if err != nil {
		return nil, err
	}
	keep := map[string]bool{}
	for _, id := range ranked {
		keep[index[id]] = true
	}
	var out []semHunk
	for _, h := range hunks {
		if keep[h.path] {
			out = append(out, h)
		}
	}
	return out, nil
}

// semWindow divides one oversized hunk and asks which part of it holds the
// answer. As many windows as fit are asked about together, in one request
// over the lines they cover, so a hunk costs a round trip per budget's worth
// of it and not one per window.
func semWindow(ctx context.Context, c *jev.Client, query string, h semHunk) ([]semCand, error) {
	// Windows overlap, so that an answer lying across a boundary is whole in
	// at least one of them.
	const overlap = 10
	// A window is what the line stage will be offered, so it is sized by the
	// whole cost of a candidate; the rows of this stage's own document carry
	// only the id and the text.
	cost := func(c semCand) int { return len(c.id) + len(c.text) + len(c.desc) + 4 }
	row := func(c semCand) int { return len(c.id) + len(c.text) + 2 }

	type window struct{ from, to int }
	var wins []window
	for from := 0; from < len(h.cands); {
		to, chars := from, 0
		for to < len(h.cands) && to-from < jev.MaxChoices && (to == from || chars+cost(h.cands[to]) <= jev.Budget) {
			chars += cost(h.cands[to])
			to++
		}
		wins = append(wins, window{from, to})
		if to == len(h.cands) {
			break
		}
		from = max(from+1, to-overlap)
	}

	best, at := 0.0, -1
	for i, asked := 0, 0; i < len(wins) && asked < semMaxRequests; asked++ {
		// Take windows while the lines they cover together still fit.
		j, chars := i, 0
		for next := wins[i].from; j < len(wins); j++ {
			add := 0
			for _, c := range h.cands[next:wins[j].to] {
				add += row(c)
			}
			if j > i && chars+add > jev.Budget {
				break
			}
			chars += add
			next = wins[j].to
		}
		var doc strings.Builder
		for _, c := range h.cands[wins[i].from:wins[j-1].to] {
			fmt.Fprintf(&doc, "%s|%s\n", c.id, c.text)
		}
		questions := make(map[string]jev.Question, j-i)
		for k := i; k < j; k++ {
			questions[fmt.Sprintf("w%02d", k)] = jev.Noul(fmt.Sprintf(
				"`lines` is one region of a code change, one line per row, each row prefixed with "+
					"its id. Does what `query` asks about occur in the rows from %s to %s?",
				h.cands[wins[k].from].id, h.cands[wins[k].to-1].id))
		}
		resp, err := c.Ask(ctx, semState{Query: query, Lines: doc.String()}, questions)
		if err != nil {
			return nil, err
		}
		for k := i; k < j; k++ {
			if p := resp.Answers[fmt.Sprintf("w%02d", k)].Noul; p > best {
				best, at = p, k
			}
		}
		i = j
	}
	if at < 0 || best < semPresentMin {
		return nil, nil
	}
	return h.cands[wins[at].from:wins[at].to], nil
}

// semHunks is the first stage: which hunks are worth reading line by line,
// best first. It returns nothing when the change does not appear to contain
// an answer at all, which saves the second request.
func semHunks(ctx context.Context, c *jev.Client, query string, hunks []semHunk) ([]semHunk, error) {
	opts := make([]semOpt, len(hunks))
	index := make(map[string]int, len(hunks))
	for i, h := range hunks {
		id := fmt.Sprintf("H%03d", i+1)
		index[id] = i
		opts[i] = semOpt{id: id, desc: h.label, line: fmt.Sprintf("%s|%s (%d lines)", id, h.label, len(h.cands))}
	}
	ranked, err := semChoose(ctx, c, query, "hunk",
		"Each option in `criteria` is a region of a code change, named by the file it is in, "+
			"the lines it covers and the function it falls inside. Which region is the one that "+
			"answers `query`?",
		"Do the regions listed in `lines` include one that answers `query`? "+
			"Answer no if `query` is about something this change does not touch.",
		opts, 0)
	if err != nil {
		return nil, err
	}
	out := make([]semHunk, 0, len(ranked))
	for _, id := range ranked {
		out = append(out, hunks[index[id]])
	}
	return out, nil
}

// semLines is the second stage: which of these lines is the answer. The same
// presence question is asked again, because the hunks that won the first
// stage won it against each other and not against nothing.
func semLines(ctx context.Context, c *jev.Client, query string, cands []semCand) ([]semCand, error) {
	if len(cands) == 0 {
		return nil, nil
	}
	opts := make([]semOpt, len(cands))
	index := make(map[string]int, len(cands))
	for i, c := range cands {
		index[c.id] = i
		opts[i] = semOpt{id: c.id, desc: c.desc, line: c.id + "|" + c.text}
	}
	ranked, err := semChoose(ctx, c, query, "line",
		"`lines` is a code change, one line per row, each row prefixed with its id and then "+
			"the +, - or space that says whether the line was added, removed or left alone. "+
			"Which line is the one that answers `query`?",
		"Do the lines in `lines` answer `query`? Answer no if the lines are about "+
			"something else, however close.",
		opts, semMaxHits)
	if err != nil {
		return nil, err
	}
	out := make([]semCand, 0, len(ranked))
	for _, id := range ranked {
		out = append(out, cands[index[id]])
	}
	return out, nil
}

// semRank orders ids by probability, best first, dropping those below min and
// stopping at max. A max of zero is no limit.
func semRank(probs map[string]float64, min float64, max int) []string {
	ids := make([]string, 0, len(probs))
	for id, p := range probs {
		if p >= min {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool {
		if probs[ids[i]] != probs[ids[j]] {
			return probs[ids[i]] > probs[ids[j]]
		}
		return ids[i] < ids[j]
	})
	if max > 0 && len(ids) > max {
		ids = ids[:max]
	}
	return ids
}
