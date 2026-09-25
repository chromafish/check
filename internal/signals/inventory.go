package signals

import (
	"context"
	"path"
	"sort"
	"strings"

	"github.com/chromafish/check/internal/diffparse"
	"github.com/chromafish/check/internal/vcs"
)

// Status is what a change did to a signal.
type Status string

const (
	Added   Status = "added"   // new in the change
	Removed Status = "removed" // gone after it
	Altered Status = "altered" // on a line the change rewrote, and still there
	Nearby  Status = "nearby"  // untouched, beside changed code
)

// Found is a signal and what the change did to it.
type Found struct {
	Signal
	Status Status
}

// Dark is a run of changed code with no signal near it: somewhere the change
// will run in production without being seen.
type Dark struct {
	Path    string
	Line    int    // the first changed line, on the side the change leaves
	Old     bool   // the line is on the old side: the run only removes code
	Text    string // its first changed line, which is what it is to a reader
	Section string // what the diff says the hunk is inside, if anything
	Changed int    // how many lines it adds or removes
}

// Inventory is what a change does to a program's telemetry.
type Inventory struct {
	Signals []Found
	Dark    []Dark
	// Files is how many changed files were read; Skipped, how many were not
	// because they are binary, too large, or unreadable.
	Files, Skipped int
}

// Count is how many signals have a status.
func (inv *Inventory) Count(s Status) int {
	n := 0
	for _, f := range inv.Signals {
		if f.Status == s {
			n++
		}
	}
	return n
}

// Reader reads one side of a changed file.
type Reader func(ctx context.Context, path string, side vcs.Side) ([]byte, error)

const (
	// near is how many lines from changed code an untouched signal may be
	// and still be said to observe it.
	near = 15
	// maxBytes is the largest file read.
	maxBytes = 2 << 20
)

// Build reads both sides of every changed file and takes stock of the
// change's telemetry.
func Build(ctx context.Context, ds []Detector, files []*diffparse.File, read Reader) (*Inventory, error) {
	inv := &Inventory{}
	for _, f := range files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		p := f.Path()
		if f.IsBinary || f.Truncated || !Covers(ds, p) {
			inv.Skipped++
			continue
		}
		var oldLines, newLines []string
		ok := true
		if !f.IsNew {
			oldLines, ok = readLines(ctx, read, f.OldPath, p, vcs.Before)
		}
		if ok && !f.IsDeleted {
			newLines, ok = readLines(ctx, read, p, p, vcs.After)
		}
		if !ok {
			inv.Skipped++
			continue
		}
		inv.Files++
		inv.file(ds, f, p, oldLines, newLines)
	}
	sort.SliceStable(inv.Signals, func(i, j int) bool {
		a, b := inv.Signals[i], inv.Signals[j]
		if rank(a.Status) != rank(b.Status) {
			return rank(a.Status) < rank(b.Status)
		}
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		return a.Line < b.Line
	})
	return inv, nil
}

func rank(s Status) int {
	switch s {
	case Added:
		return 0
	case Removed:
		return 1
	case Altered:
		return 2
	}
	return 3
}

func readLines(ctx context.Context, read Reader, p, fallback string, side vcs.Side) ([]string, bool) {
	if p == "" {
		p = fallback
	}
	src, err := read(ctx, p, side)
	if err != nil || len(src) > maxBytes {
		return nil, false
	}
	return strings.Split(strings.ReplaceAll(string(src), "\r\n", "\n"), "\n"), true
}

// file takes stock of one file.
func (inv *Inventory) file(ds []Detector, f *diffparse.File, p string, oldLines, newLines []string) {
	olds := Scan(ds, p, oldLines)
	news := Scan(ds, p, newLines)
	oldKeys, newKeys := map[string]bool{}, map[string]bool{}
	for _, s := range olds {
		oldKeys[s.Key()] = true
	}
	for _, s := range news {
		newKeys[s.Key()] = true
	}

	addedAt, removedAt := map[int]bool{}, map[int]bool{}
	for _, h := range f.Hunks {
		for _, l := range h.Lines {
			switch l.Kind {
			case diffparse.Added:
				addedAt[l.NewNum] = true
			case diffparse.Removed:
				removedAt[l.OldNum] = true
			}
		}
	}

	seen := map[string]bool{}
	add := func(s Signal, st Status) {
		k := s.Key() + string(st)
		if seen[k] {
			return
		}
		seen[k] = true
		inv.Signals = append(inv.Signals, Found{s, st})
	}
	for _, s := range news {
		switch {
		case addedAt[s.Line] && !oldKeys[s.Key()]:
			add(s, Added)
		case addedAt[s.Line]:
			add(s, Altered)
		case nearAny(s.Line, addedAt) || nearAny(s.Line, newSideOf(f)):
			add(s, Nearby)
		}
	}
	for _, s := range olds {
		if removedAt[s.Line] && !newKeys[s.Key()] {
			add(s, Removed)
		}
	}

	// A hunk is dark when nothing observes it: no signal it adds or
	// rewrites, and none near it. Tests are not what runs in production.
	if isTest(p) {
		return
	}
	for _, h := range f.Hunks {
		if trivial(h) {
			continue
		}
		adds := linesOf(h, diffparse.Added)
		sigs, old := news, false
		if len(adds) == 0 {
			adds, sigs, old = linesOf(h, diffparse.Removed), olds, true
		}
		for _, run := range clusters(adds, old) {
			observed := false
			for _, s := range sigs {
				if nearAny(s.Line, run.at) {
					observed = true
					break
				}
			}
			if !observed {
				inv.Dark = append(inv.Dark, Dark{Path: p, Line: run.first, Old: old, Text: run.text,
					Section: strings.TrimSpace(h.Section), Changed: len(run.at)})
			}
		}
	}
}

func linesOf(h diffparse.Hunk, k diffparse.Kind) []diffparse.Line {
	var out []diffparse.Line
	for _, l := range h.Lines {
		if l.Kind == k {
			out = append(out, l)
		}
	}
	return out
}

// cluster is a run of changed lines close enough together to be one piece
// of code.
type cluster struct {
	first int
	text  string
	at    map[int]bool
}

// gap is how many lines apart two changed lines may be and still be one
// piece of code.
const gap = 3

// clusters divides changed lines into the pieces of code they make up,
// leaving out blank lines, which belong to none.
func clusters(lines []diffparse.Line, old bool) []cluster {
	var out []cluster
	last := -gap - 1
	for _, l := range lines {
		if strings.TrimSpace(l.Text) == "" {
			continue
		}
		n := l.NewNum
		if old {
			n = l.OldNum
		}
		if len(out) == 0 || n-last > gap {
			out = append(out, cluster{first: n, text: strings.TrimSpace(l.Text), at: map[int]bool{}})
		}
		out[len(out)-1].at[n] = true
		last = n
	}
	return out
}

// newSideOf is the new-side lines of a file's hunks that were only removed
// around: where a deletion sits after the change.
func newSideOf(f *diffparse.File) map[int]bool {
	out := map[int]bool{}
	for _, h := range f.Hunks {
		for _, l := range h.Lines {
			if l.Kind == diffparse.Removed {
				out[h.NewStart] = true
				break
			}
		}
	}
	return out
}

func nearAny(line int, at map[int]bool) bool {
	for n := line - near; n <= line+near; n++ {
		if at[n] {
			return true
		}
	}
	return false
}

// trivial reports whether a hunk changes nothing that runs: blank lines,
// comments, imports.
func trivial(h diffparse.Hunk) bool {
	for _, l := range h.Lines {
		if l.Kind == diffparse.Context {
			continue
		}
		t := strings.TrimSpace(l.Text)
		switch {
		case t == "", strings.HasPrefix(t, "//"), strings.HasPrefix(t, "#"), strings.HasPrefix(t, "/*"),
			strings.HasPrefix(t, "*"), strings.HasPrefix(t, "--"),
			strings.HasPrefix(t, "import "), strings.HasPrefix(t, "use "), strings.HasPrefix(t, "from "),
			t == "(", t == ")":
			continue
		}
		return false
	}
	return true
}

func isTest(p string) bool {
	l := strings.ToLower(p)
	for _, s := range []string{"_test.", ".test.", ".spec.", "/tests/", "/test/", "/testdata/", "/__tests__/", "/fixtures/"} {
		if strings.Contains("/"+l, s) {
			return true
		}
	}
	return strings.HasPrefix(path.Base(l), "test_")
}
