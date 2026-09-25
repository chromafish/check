package jj

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// newRepo builds a jj repository with two described changes and a working
// copy on top of the second.
func newRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("jj"); err != nil {
		t.Skip("jj is not installed")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("jj", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"JJ_USER=Test", "JJ_EMAIL=test@example.com",
			"JJ_TIMESTAMP=2026-01-01T00:00:00+00:00")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("jj %v: %v\n%s", args, err, out)
		}
	}
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run("git", "init")
	write(".gitignore", "artefact\n")
	write("main.go", "one\n")
	run("describe", "-m", "first")
	run("new", "-m", "second")
	write("main.go", "two\n")
	run("new")
	return dir
}
