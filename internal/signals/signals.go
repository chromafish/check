// Package signals finds the telemetry a program emits — its spans, metrics,
// log lines, events, error reports and feature flag reads — and what a change
// does to it.
//
// What counts as telemetry is data, not code: a detector is a regular
// expression and the kind of signal it finds, applied to files by extension.
// Check ships detectors for the common libraries, and a repository declares
// its own in check.toml, so a home-grown audit log or a metrics wrapper is
// found the same way as OpenTelemetry.
package signals

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/BurntSushi/toml"
)

// Kind is what sort of telemetry a signal is.
type Kind string

const (
	Span   Kind = "span"
	Metric Kind = "metric"
	Log    Kind = "log"
	Event  Kind = "event"
	Error  Kind = "error"
	Flag   Kind = "flag"
)

// Detector finds one kind of signal. Pattern's first capture group, when it
// matches anything, is the signal's name: the span or metric name, or a log
// line's message. A pattern may run over several lines — a metric's options
// are often spread across a struct literal — but a match is only taken from
// the line it starts on.
type Detector struct {
	Name       string   `toml:"name"`
	Kind       Kind     `toml:"kind"`
	Extensions []string `toml:"extensions"`
	Pattern    string   `toml:"pattern"`

	re *regexp.Regexp
}

// Signal is one place a program emits telemetry.
type Signal struct {
	Kind     Kind
	Name     string
	Detector string
	Path     string
	Line     int // 1-based
	Text     string
}

// Key identifies a signal across the two sides of a change: the same detector
// and name in the same file.
func (s Signal) Key() string { return s.Path + "\x00" + s.Detector + "\x00" + s.Name }

// window is how many lines past its first a pattern may read.
const window = 6

// File is the configuration file a repository declares its detectors in.
const File = "check.toml"

type config struct {
	Signal []Detector `toml:"signal"`
}

// Load is the built-in detectors followed by those the repository at root
// declares. A repository without a check.toml has the built-ins alone.
func Load(root string) ([]Detector, error) {
	ds := Builtin()
	raw, err := os.ReadFile(filepath.Join(root, File))
	if os.IsNotExist(err) {
		return ds, nil
	}
	if err != nil {
		return nil, err
	}
	var c config
	if _, err := toml.Decode(string(raw), &c); err != nil {
		return nil, fmt.Errorf("%s: %w", File, err)
	}
	for i := range c.Signal {
		d := &c.Signal[i]
		if err := d.compile(); err != nil {
			return nil, fmt.Errorf("%s: signal %q: %w", File, d.Name, err)
		}
		if d.Kind == "" {
			d.Kind = Event
		}
	}
	return append(ds, c.Signal...), nil
}

func (d *Detector) compile() error {
	if d.Name == "" {
		return fmt.Errorf("a signal needs a name")
	}
	re, err := regexp.Compile(d.Pattern)
	if err != nil {
		return err
	}
	d.re = re
	return nil
}

// applies reports whether a detector reads a file. A detector naming no
// extensions reads every file.
func (d *Detector) applies(p string) bool {
	if len(d.Extensions) == 0 {
		return true
	}
	ext := strings.ToLower(path.Ext(p))
	for _, e := range d.Extensions {
		if e == ext {
			return true
		}
	}
	return false
}

// Covers reports whether any detector reads a file: a file none reads, a
// document or a configuration file, has no telemetry to find.
func Covers(ds []Detector, p string) bool {
	for i := range ds {
		if len(ds[i].Extensions) > 0 && ds[i].applies(p) {
			return true
		}
	}
	return false
}

var nextName = regexp.MustCompile(`\b(?:fn|func|def|function|async\s+fn)\s+(?:\([^)]*\)\s*)?([A-Za-z_]\w*)`)

// Scan finds the signals in one file's lines.
func Scan(ds []Detector, p string, lines []string) []Signal {
	var out []Signal
	var use []*Detector
	for i := range ds {
		if ds[i].applies(p) {
			use = append(use, &ds[i])
		}
	}
	if len(use) == 0 {
		return nil
	}
	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		text := strings.Join(lines[i:min(len(lines), i+window)], "\n")
		for _, d := range use {
			for _, m := range d.re.FindAllStringSubmatchIndex(text, -1) {
				if m[0] >= len(line) {
					break
				}
				name := ""
				if len(m) >= 4 && m[2] >= 0 {
					name = text[m[2]:m[3]]
				}
				// An attribute that instruments a function is named
				// after the function it stands over.
				if name == "" && d.Kind == Span {
					if n := nextName.FindStringSubmatch(text[m[1]:]); n != nil {
						name = n[1]
					}
				}
				out = append(out, Signal{
					Kind: d.Kind, Name: name, Detector: d.Name,
					Path: p, Line: i + 1, Text: strings.TrimSpace(line),
				})
			}
		}
	}
	return out
}
