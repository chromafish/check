package journal

import (
	"bytes"
	"strings"
	"testing"
)

func TestErrorsAloneUnlessAskedForMore(t *testing.T) {
	var buf bytes.Buffer
	l, err := New(&buf, "")
	if err != nil {
		t.Fatal(err)
	}
	l.Info("a decision", "n", 1)
	l.Error("a failure")
	if got := buf.String(); !strings.Contains(got, `msg="a failure"`) || strings.Contains(got, "a decision") {
		t.Errorf("at the default level the log holds:\n%s", got)
	}

	buf.Reset()
	l, err = New(&buf, "info")
	if err != nil {
		t.Fatal(err)
	}
	l.Info("a decision", "n", 1)
	if got := buf.String(); !strings.Contains(got, `level=INFO msg="a decision" n=1`) {
		t.Errorf("asked for info, the log holds:\n%s", got)
	}
}

func TestANameThatIsNotALevelIsReportedAndFallsBackToErrors(t *testing.T) {
	var buf bytes.Buffer
	l, err := New(&buf, "loud")
	if err == nil || !strings.Contains(err.Error(), "loud") {
		t.Fatalf("err = %v, want one naming the value", err)
	}
	l.Info("a decision")
	l.Error("a failure")
	if got := buf.String(); strings.Contains(got, "a decision") || !strings.Contains(got, "a failure") {
		t.Errorf("after a bad level the log holds:\n%s", got)
	}
}

// Each line is logfmt as Parse reads it, with the level and message lifted
// and the rest left as fields.
func TestLinesAreLogfmt(t *testing.T) {
	var buf bytes.Buffer
	l, _ := New(&buf, "info")
	l.Info("repo opened", "vcs", "jj", "root", "/tmp/a dir")
	line := strings.TrimSpace(buf.String())
	got := Parse(line)
	if !got.OK {
		t.Fatalf("the line %q did not parse as logfmt", line)
	}
	if got.Level != "INFO" || got.Message != "repo opened" || got.Time.IsZero() {
		t.Errorf("parsed as %+v", got)
	}
	if len(got.Fields) != 2 || got.Fields[0] != (Field{Key: "vcs", Value: "jj"}) || got.Fields[1] != (Field{Key: "root", Value: "/tmp/a dir"}) {
		t.Errorf("fields = %+v", got.Fields)
	}
}
