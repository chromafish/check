// Package journal opens the behaviour log: one line for each decision the
// program makes, for a bug report to be filed with.
//
// The log is slog's default logger. A package writes to it with slog.Info
// and slog.Error and has nothing passed down to it; off, the default is a
// handler that discards, and a call costs the one check that finds that out.
//
// REVIEW_LOG=1 writes to stderr, and REVIEW_LOG=/some/file to that file,
// for an application launched from the Finder, where there is no terminal
// to write to. Errors alone are written unless REVIEW_LOG_LEVEL=info
// asks for the decisions too. The frame profiler behind REVIEW_TRACE is a
// separate switch: it answers a different question, and its output differs
// on every run.
//
// Two runs of unchanged code are meant to write the same lines, so that what
// differs between two revisions is what the change did. A message therefore
// carries no duration, no absolute path and nothing ordered by scheduling; a
// value that varies and is still worth having goes in a field, where it can
// be ignored. The time each line was written is such a field.
package journal

import (
	"fmt"
	"io"
	"log/slog"
	"os"
)

const (
	envDest  = "REVIEW_LOG"
	envLevel = "REVIEW_LOG_LEVEL"
)

// Start installs the log the environment asks for, or one that discards.
// It runs before anything that writes to the log.
func Start() {
	dest := os.Getenv(envDest)
	if dest == "" {
		slog.SetDefault(slog.New(slog.DiscardHandler))
		return
	}
	var w io.Writer = os.Stderr
	if dest != "1" && dest != "true" {
		f, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			fmt.Fprintf(os.Stderr, "check: cannot write the log to %s: %v\n", dest, err)
		} else {
			w = f
		}
	}
	logger, err := New(w, os.Getenv(envLevel))
	if err != nil {
		fmt.Fprintln(os.Stderr, "check:", err)
	}
	slog.SetDefault(logger)
}

// New returns a logger writing logfmt to w at the named level, or at Error
// when the name is empty. A name that is not a level is reported, and the
// logger still comes back, at Error.
func New(w io.Writer, level string) (*slog.Logger, error) {
	min := slog.LevelError
	var err error
	if level != "" {
		if err = min.UnmarshalText([]byte(level)); err != nil {
			min = slog.LevelError
			err = fmt.Errorf("%s=%q is not a level; writing errors alone", envLevel, level)
		}
	}
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: min})), err
}
