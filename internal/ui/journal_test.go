package ui

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"gioui.org/io/key"

	"github.com/chromafish/check/internal/journal"
)

// captureLog points the behaviour log at a buffer for the life of the test.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	was := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(was) })
	return &buf
}

// entries reads the captured log back.
func entries(t *testing.T, buf *bytes.Buffer) []journal.Entry {
	t.Helper()
	var logs []journal.Entry
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		l := journal.Parse(line)
		if !l.OK {
			t.Errorf("a line of the log is not logfmt: %q", line)
		}
		logs = append(logs, l)
	}
	return logs
}

func field(l journal.Entry, key string) string { return l.Field(key) }

// Opening a repository and reading a change writes each decision along the
// way, with paths and counts in fields and the message the same from one run
// to the next.
func TestTheLogRecordsAReview(t *testing.T) {
	buf := captureLog(t)
	h := newHarness(t)
	h.selectFileNamed("main.go")

	var msgs []string
	byMsg := map[string]journal.Entry{}
	for _, l := range entries(t, buf) {
		msgs = append(msgs, l.Message)
		if _, seen := byMsg[l.Message]; !seen {
			byMsg[l.Message] = l
		}
	}
	for _, want := range []string{"repo opened", "command ran", "revset evaluated", "revision selected", "diff parsed", "file selected"} {
		if _, ok := byMsg[want]; !ok {
			t.Errorf("no %q in the log:\n%s", want, strings.Join(msgs, "\n"))
		}
	}
	if l := byMsg["repo opened"]; field(l, "vcs") != "jj" || field(l, "root") != h.app.repo.Root() {
		t.Errorf("repo opened = %+v", l.Fields)
	}
	if l := byMsg["revset evaluated"]; field(l, "revset") != "all()" || field(l, "revisions") == "" {
		t.Errorf("revset evaluated = %+v", l.Fields)
	}
	if l := byMsg["revision selected"]; field(l, "change") != h.app.rev.ChangeID || field(l, "files") != "3" {
		t.Errorf("revision selected = %+v", l.Fields)
	}
	if l := byMsg["diff parsed"]; field(l, "files") != "3" || field(l, "truncated") != "0" || field(l, "added") == "0" {
		t.Errorf("diff parsed = %+v", l.Fields)
	}
	// The one the change a845346 would have shown as a one-line diff.
	var lit []journal.Entry
	for _, l := range entries(t, buf) {
		if l.Message == "file selected" {
			lit = append(lit, l)
		}
	}
	syntaxes := map[string]string{}
	for _, l := range lit {
		syntaxes[field(l, "path")] = field(l, "syntax")
	}
	if syntaxes["main.go"] != "Go" {
		t.Errorf("main.go was highlighted as %q, want Go", syntaxes["main.go"])
	}
	if s, ok := syntaxes["added.txt"]; ok && s != "plaintext" {
		t.Errorf("added.txt was highlighted as %q, want plaintext", s)
	}

	// Every command the repository was read through is there with its exit.
	if l := byMsg["command ran"]; field(l, "cmd") != "jj" || field(l, "exit") != "0" {
		t.Errorf("command ran = %+v", l.Fields)
	}

	// Nothing that varies from one run to the next sits in a message.
	for _, l := range entries(t, buf) {
		if strings.Contains(l.Message, h.app.repo.Root()) {
			t.Errorf("a message carries the repository's path: %q", l.Message)
		}
	}
}

// What reaches the status bar reaches the log, errors at their own level.
func TestStatusAndErrorsReachTheLog(t *testing.T) {
	buf := captureLog(t)
	h := newHarness(t)
	buf.Reset()

	h.press("/", 0)
	h.typeText("no_such_function()")
	h.press(key.NameReturn, 0)
	h.press("V", 0)

	var errs, notes []journal.Entry
	for _, l := range entries(t, buf) {
		switch l.Level {
		case "ERROR":
			errs = append(errs, l)
		case "INFO":
			notes = append(notes, l)
		}
	}
	if len(errs) == 0 || !strings.Contains(errs[0].Message, "no_such_function") {
		t.Errorf("the failing revset did not reach the log as an error: %+v", errs)
	}
	found := false
	for _, l := range notes {
		if strings.HasPrefix(l.Message, "read ") {
			found = true
		}
	}
	if !found {
		t.Errorf("marking a file read did not reach the log")
	}
}
