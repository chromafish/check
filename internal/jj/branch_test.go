package jj

import (
	"context"
	"os"
	"os/exec"
	"testing"
)

// A bookmark is reviewed from where it leaves trunk(), and its commits are
// only the ones trunk() does not already have.
func TestBranchIsTheRangeOffTheTrunk(t *testing.T) {
	dir := newRepo(t)
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("jj", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "JJ_USER=Test", "JJ_EMAIL=test@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("jj %v: %v\n%s", args, err, out)
		}
	}
	run("bookmark", "create", "main", "-r", "subject(first)")
	run("bookmark", "create", "feat/x", "-r", "subject(second)")
	run("config", "set", "--repo", `revset-aliases."trunk()"`, "main")

	ctx := context.Background()
	r, err := Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	names, err := r.Branches(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 2 || names[0] != "feat/x" || names[1] != "main" {
		t.Fatalf("branches = %v, want feat/x then main", names)
	}

	b, err := r.Branch(ctx, "feat/x")
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Commits) != 1 || b.Commits[0].Description != "second" {
		t.Errorf("commits = %+v, want only second", b.Commits)
	}
	main, _ := r.Log(ctx, "main", 1)
	if b.Base != main[0].CommitIDFull {
		t.Errorf("base = %s, want main's commit %s", b.Base, main[0].CommitIDFull)
	}
	files, err := r.Files(ctx, b.Spec())
	if err != nil || len(files) != 1 || files[0].Path != "main.go" {
		t.Errorf("range files = %v, %v; want main.go", files, err)
	}

	// The trunk itself has nothing of its own.
	m, err := r.Branch(ctx, "main")
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Commits) != 0 {
		t.Errorf("main has %d commits of its own, want none", len(m.Commits))
	}
	if len(m.History) != 1 || m.History[0].Description != "first" {
		t.Errorf("main's history = %+v, want first", m.History)
	}
	// The working copy sits on second, which feat/x points at.
	if cur, err := r.CurrentBranch(ctx); err != nil || cur != "feat/x" {
		t.Errorf("current branch = %q, %v; want feat/x", cur, err)
	}
}
