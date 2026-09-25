package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// A branch is reviewed from its merge-base with main, and its commits are only
// the ones main does not already have.
func TestBranchIsTheRangeOffTheTrunk(t *testing.T) {
	dir := newRepo(t)
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com",
			"GIT_COMMITTER_DATE=2026-01-02T00:00:00+00:00")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("checkout", "-b", "feat/x")
	if err := os.WriteFile(filepath.Join(dir, "extra.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-m", "extra")

	ctx := context.Background()
	r := open(t, dir)
	names, err := r.Branches(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 2 || names[0] != "feat/x" {
		t.Fatalf("branches = %v, want feat/x first", names)
	}
	b, err := r.Branch(ctx, "feat/x")
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Commits) != 1 || b.Commits[0].Description != "extra" {
		t.Errorf("commits = %+v, want only extra", b.Commits)
	}
	files, err := r.Files(ctx, b.Spec())
	if err != nil || len(files) != 1 || files[0].Path != "extra.go" {
		t.Errorf("range files = %v, %v; want extra.go", files, err)
	}
	m, err := r.Branch(ctx, "main")
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Commits) != 0 {
		t.Errorf("main has %d commits of its own, want none", len(m.Commits))
	}
}
