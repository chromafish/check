package ui

import (
	"os"
	"os/exec"
	"testing"

	"github.com/chromafish/check/internal/state"
	"github.com/chromafish/check/internal/vcs"
)

// commitAll commits whatever the working tree holds, leaving it clean.
func commitAll(t *testing.T, dir, msg string) {
	t.Helper()
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-qm", msg}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

// A repository where everything is on main, and nothing is uncommitted, opens
// on main's history, reviewing its newest commit; there is no working copy
// row pretending to be something to review.
func TestNavigatorOnATrunkOnlyRepository(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	dir := newGitRepo(t)
	commitAll(t, dir, "second")
	h := openHarnessView(t, dir, true)

	rows := h.app.reviewRows()
	if len(rows) != 3 || rows[0].kind != reviewBranch || rows[0].name != "main" {
		t.Fatalf("rows = %+v, want main then its two commits", rows)
	}
	if rows[1].kind != reviewCommit || rows[1].rev.Description != "second" || rows[2].rev.Description != "first" {
		t.Errorf("main's commits = %q, %q; want second then first", rows[1].rev.Description, rows[2].rev.Description)
	}
	if h.app.spec.Kind != vcs.DiffChange || h.app.rev.Description != "second" {
		t.Errorf("under review: %+v %q, want the newest commit", h.app.spec, h.app.rev.Description)
	}
	h.app.focus = PaneRevs
	h.press("J", 0)
	h.press("J", 0)
	h.settle()
	if h.app.rev.Description != "first" {
		t.Errorf("under review: %q, want first after moving down", h.app.rev.Description)
	}
}

// newBriefRepo is the harness repository with a bookmark on the first change,
// which the working copy sits above.
func newBriefRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("jj"); err != nil {
		t.Skip("jj is not installed")
	}
	dir := newRepo(t)
	cmd := exec.Command("jj", "bookmark", "create", "feat", "-r", "subject(first)")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "JJ_USER=Test", "JJ_EMAIL=test@example.com")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("jj bookmark: %v\n%s", err, out)
	}
	return dir
}

// Uncommitted work comes first and is what opens; the branch beneath it is
// listed, current and open, and reviewed whole when chosen.
func TestNavigatorWithUncommittedWorkAndABranch(t *testing.T) {
	h := openHarnessView(t, newBriefRepo(t), true)

	rows := h.app.reviewRows()
	if len(rows) < 2 || rows[0].kind != reviewWorking || rows[1].name != "feat" {
		t.Fatalf("rows = %+v, want uncommitted work then feat", rows)
	}
	if h.app.current != "feat" || !h.app.opened["feat"] {
		t.Errorf("current = %q, open = %v; want feat, open", h.app.current, h.app.opened)
	}
	if h.app.reviewKey != "@" {
		t.Fatalf("under review: %q, want the uncommitted work", h.app.reviewKey)
	}

	h.app.focus = PaneRevs
	h.press("J", 0)
	h.settle()
	if h.app.reviewKey != branchKey("feat") || h.app.spec.Kind != vcs.DiffRange {
		t.Errorf("under review: %q %+v, want feat as a range", h.app.reviewKey, h.app.spec)
	}
	if got := h.app.diffTitle(); got != "DIFF · FEAT" {
		t.Errorf("diff title = %q", got)
	}
}

// B moves between the views and the choice is kept.
func TestBSwitchesToTheClassicViewAndRemembersIt(t *testing.T) {
	h := openHarnessView(t, newBriefRepo(t), true)
	h.press("B", 0)
	if !h.app.classic() {
		t.Fatal("B did not switch to the classic view")
	}
	if !state.LoadSettings().Classic {
		t.Error("the classic view was not recorded")
	}
	h.press("B", 0)
	if h.app.classic() {
		t.Error("B did not switch back")
	}
}
