package ui

import (
	"image"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/chromafish/check/internal/clone"
	"github.com/chromafish/check/internal/jev"
	"github.com/chromafish/check/internal/vcs"
)

// A repository opened from a branch link is reviewed on that branch first,
// as a whole, in the brief view.
func TestOpeningOnABranchReviewsIt(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	h := openHarnessView(t, newBriefRepo(t), true)

	dir := newGitRepo(t)
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("checkout", "-b", "feat/x")
	os.WriteFile(filepath.Join(dir, "more.txt"), []byte("more\n"), 0o644)
	run("add", ".")
	run("commit", "-m", "more")

	h.app.openRepoOn(dir, "feat/x", "")
	h.settle()
	if h.app.reviewKey != "b:feat/x" {
		t.Fatalf("under review: %q, want feat/x", h.app.reviewKey)
	}
	if h.app.spec.Kind != vcs.DiffRange {
		t.Errorf("spec = %+v, want the branch as a range", h.app.spec)
	}
}

// A link that is not one says so on the open screen, and nothing starts.
func TestABadLinkSaysSo(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	a := NewOffscreen(nil, ".", nil, "")
	a.cloneLink("https://github.com/only-owner")
	if a.openErr == "" {
		t.Error("a link naming no repository raised no error")
	}
	if a.cloning != nil {
		t.Error("a clone started from a bad link")
	}
}

// git draws progress by returning to the start of the line, so the console
// keeps one line per counter rather than one per update.
func TestConsoleFoldsCarriageReturns(t *testing.T) {
	var c console
	c.Write([]byte("$ git clone x\nReceiving objects:  10% (1/10)\rReceiving objects: 100% (10/10)"))
	c.Write([]byte(", done.\nResolving deltas: 50%\r"))
	c.Write([]byte("Resolving deltas: 100%"))
	got := c.tail(consoleRows)
	want := []string{"$ git clone x", "Receiving objects: 100% (10/10), done.", "Resolving deltas: 100%"}
	if len(got) != len(want) {
		t.Fatalf("tail = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line %d = %q, want %q", i, got[i], want[i])
		}
	}
	if tail := c.tail(1); len(tail) != 1 || tail[0] != "Resolving deltas: 100%" {
		t.Errorf("tail(1) = %q", tail)
	}
}

// A clone that finishes opens the repository it made, and one stopped with
// Esc says so and opens nothing.
func TestAFinishedCloneOpens(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	src := newGitRepo(t)
	cmd := exec.Command("git", "commit", "-qm", "second")
	cmd.Dir = src
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v\n%s", err, out)
	}

	h := &harness{t: t, size: image.Pt(1400, 900), now: time.Now()}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv(jev.EnvKey, "")
	h.app = NewOffscreen(nil, ".", nil, "")
	h.app.settings.CloneDir = t.TempDir()
	h.settle()

	h.app.startClone(clone.Source{URL: src, Host: "example.com", Path: "owner/repo"})
	h.settle()
	if h.app.openErr != "" {
		t.Fatalf("open error %q after a clone that finished", h.app.openErr)
	}
	if h.app.repo == nil {
		t.Fatal("the clone finished and nothing opened")
	}
	want, _ := filepath.EvalSymlinks(clone.Dir(h.app.settings.CloneDir, clone.Source{Host: "example.com", Path: "owner/repo"}))
	if got, _ := filepath.EvalSymlinks(h.app.repo.Root()); got != want {
		t.Errorf("opened %s, want %s", got, want)
	}
	if h.app.cloning != nil || h.app.console != nil {
		t.Error("the clone's state outlived the repository opening")
	}
}
