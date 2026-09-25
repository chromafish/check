package clone

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParse(t *testing.T) {
	cases := []struct {
		in                      string
		url, host, path, branch string
	}{
		{"https://github.com/owner/repo", "https://github.com/owner/repo", "github.com", "owner/repo", ""},
		{"https://github.com/owner/repo.git", "https://github.com/owner/repo", "github.com", "owner/repo", ""},
		{"  https://github.com/owner/repo/  ", "https://github.com/owner/repo", "github.com", "owner/repo", ""},
		{"https://github.com/owner/repo/tree/feat/x", "https://github.com/owner/repo", "github.com", "owner/repo", "feat/x"},
		{"https://github.com/owner/repo/tree/feat%2Fy?tab=readme#top", "https://github.com/owner/repo", "github.com", "owner/repo", "feat/y"},
		{"https://github.com/owner/repo/pull/12", "https://github.com/owner/repo", "github.com", "owner/repo", ""},
		{"https://gitlab.com/group/sub/repo/-/tree/main", "https://gitlab.com/group/sub/repo", "gitlab.com", "group/sub/repo", "main"},
		{"https://gitlab.com/group/sub/repo", "https://gitlab.com/group/sub/repo", "gitlab.com", "group/sub/repo", ""},
		{"https://codeberg.org/owner/repo/src/branch/dev", "https://codeberg.org/owner/repo", "codeberg.org", "owner/repo", "dev"},
		{"https://bitbucket.org/owner/repo/src/dev", "https://bitbucket.org/owner/repo", "bitbucket.org", "owner/repo", "dev"},
		{"git@github.com:owner/repo.git", "git@github.com:owner/repo.git", "github.com", "owner/repo", ""},
		{"ssh://git@example.com:2222/owner/repo.git", "ssh://git@example.com:2222/owner/repo.git", "example.com", "owner/repo", ""},
		{"github.com/owner/repo", "https://github.com/owner/repo", "github.com", "owner/repo", ""},
		{"https://GitHub.com/owner/repo", "https://GitHub.com/owner/repo", "github.com", "owner/repo", ""},
	}
	for _, c := range cases {
		s, err := Parse(c.in)
		if err != nil {
			t.Errorf("Parse(%q): %v", c.in, err)
			continue
		}
		if s.URL != c.url || s.Host != c.host || s.Path != c.path || s.Branch != c.branch {
			t.Errorf("Parse(%q) = %+v, want %s %s %s %q", c.in, s, c.url, c.host, c.path, c.branch)
		}
	}
}

func TestParseRefuses(t *testing.T) {
	for _, in := range []string{"", "https://github.com", "https://github.com/owner", "file:///etc", "https://x.com/../../etc/tree/main", "ftp://x.com/a/b", "src/check/thing", "owner/repo"} {
		if s, err := Parse(in); err == nil {
			t.Errorf("Parse(%q) = %+v, want an error", in, s)
		}
	}
}

func TestExpandRoot(t *testing.T) {
	home, _ := os.UserHomeDir()
	if got := ExpandRoot("~/src"); got != filepath.Join(home, "src") {
		t.Errorf("~/src = %s", got)
	}
	if got := ExpandRoot("src"); got != filepath.Join(home, "src") {
		t.Errorf("src = %s", got)
	}
	if got := ExpandRoot(""); got != DefaultRoot() {
		t.Errorf("empty = %s", got)
	}
	if got := ExpandRoot("/tmp/x/"); got != "/tmp/x" {
		t.Errorf("/tmp/x/ = %s", got)
	}
}

// origin builds a repository to clone from, with a main branch and a branch
// whose name holds a slash.
func origin(t *testing.T) (string, func(...string)) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	dir := t.TempDir()
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
	run("init", "-b", "main")
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\n"), 0o644)
	run("add", ".")
	run("commit", "-m", "first")
	run("checkout", "-b", "feat/x")
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("two\n"), 0o644)
	run("commit", "-am", "second")
	run("checkout", "main")
	return dir, run
}

func head(t *testing.T, dir string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

// The first paste clones, onto the branch the link named even when the link
// ran on into a file's path; the second finds the clone and fetches it.
func TestEnsureClonesOnceThenFetches(t *testing.T) {
	src, run := origin(t)
	root := t.TempDir()
	s := Source{URL: src, Host: "example.com", Path: "owner/repo", Branch: "feat/x/a.txt"}

	res, err := Ensure(context.Background(), root, s, nil)
	if err != nil {
		t.Fatalf("first Ensure: %v", err)
	}
	if !res.Cloned || res.Branch != "feat/x" || res.Warning != "" {
		t.Fatalf("first Ensure = %+v, want a clone on feat/x", res)
	}
	if want := filepath.Join(root, "example.com", "owner", "repo"); res.Dir != want {
		t.Errorf("dir = %s, want %s", res.Dir, want)
	}
	if got := head(t, res.Dir); got != "feat/x" {
		t.Errorf("HEAD = %s, want feat/x", got)
	}
	if _, err := os.Stat(res.Dir + ".partial"); err == nil {
		t.Error("the partial clone was left behind")
	}

	// origin moves on; pasting again fetches and fast-forwards, no clone.
	run("checkout", "feat/x")
	os.WriteFile(filepath.Join(src, "a.txt"), []byte("three\n"), 0o644)
	run("commit", "-am", "third")
	res, err = Ensure(context.Background(), root, Source{URL: src, Host: "example.com", Path: "owner/repo", Branch: "feat/x"}, nil)
	if err != nil {
		t.Fatalf("second Ensure: %v", err)
	}
	if res.Cloned {
		t.Error("the second paste cloned again")
	}
	body, _ := os.ReadFile(filepath.Join(res.Dir, "a.txt"))
	if string(body) != "three\n" {
		t.Errorf("a.txt = %q, want origin's latest", body)
	}

	// Back to main without a branch in the link leaves the working copy be.
	res, err = Ensure(context.Background(), root, Source{URL: src, Host: "example.com", Path: "owner/repo"}, nil)
	if err != nil || res.Branch != "" {
		t.Fatalf("third Ensure = %+v, %v", res, err)
	}
	if got := head(t, res.Dir); got != "feat/x" {
		t.Errorf("HEAD = %s, want it left on feat/x", got)
	}
}

func TestEnsureWarnsOfAMissingBranch(t *testing.T) {
	src, _ := origin(t)
	res, err := Ensure(context.Background(), t.TempDir(), Source{URL: src, Host: "h", Path: "o/r", Branch: "nope"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Branch != "" || !strings.Contains(res.Warning, "nope") {
		t.Errorf("Ensure = %+v, want a warning about nope", res)
	}
}

func TestEnsureLeavesNothingWhenTheCloneFails(t *testing.T) {
	root := t.TempDir()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	s := Source{URL: filepath.Join(root, "missing"), Host: "h", Path: "o/r"}
	if _, err := Ensure(context.Background(), root, s, nil); err == nil {
		t.Fatal("cloning nothing succeeded")
	}
	if _, err := os.Stat(Dir(root, s)); err == nil {
		t.Error("a failed clone left a directory behind")
	}
	if _, err := os.Stat(Dir(root, s) + ".partial"); err == nil {
		t.Error("a failed clone left its partial behind")
	}
}

// A clone that would ask for a login is refused at once, not left waiting on a
// prompt nobody can see. The server here is a stand-in that demands one.
func TestEnsureNeverWaitsForAPrompt(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("WWW-Authenticate", `Basic realm="git"`)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	// No credential helper, so nothing but a prompt could answer.
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(t.TempDir(), "gitconfig"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")

	s := Source{URL: srv.URL + "/owner/repo", Host: "127.0.0.1", Path: "owner/repo"}
	var log strings.Builder
	done := make(chan error, 1)
	go func() {
		_, err := Ensure(context.Background(), t.TempDir(), s, &log)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a clone behind a login succeeded")
		}
		if !strings.Contains(err.Error(), "needs credentials") {
			t.Errorf("error = %v, want it to say credentials are needed", err)
		}
		if !strings.Contains(log.String(), "trying ssh") {
			t.Errorf("progress = %q, want the ssh retry said", log.String())
		}
	case <-time.After(60 * time.Second):
		t.Fatal("the clone is waiting on a prompt")
	}
}

// Silence is taken as stuck: a git that prints nothing for the stall period
// is stopped and says why.
func TestEnsureStopsASilentGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	old := stall
	stall = 500 * time.Millisecond
	defer func() { stall = old }()
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-block }))
	defer func() { close(block); srv.Close() }()

	s := Source{URL: srv.URL + "/owner/repo", Host: "127.0.0.1", Path: "owner/repo"}
	root := t.TempDir()
	_, err := Ensure(context.Background(), root, s, nil)
	if err == nil || !strings.Contains(err.Error(), "printed nothing") {
		t.Fatalf("err = %v, want the stall reported", err)
	}
	if _, err := os.Stat(Dir(root, s) + ".partial"); err == nil {
		t.Error("the stopped clone left its partial behind")
	}
}

func TestSummaryDropsProgress(t *testing.T) {
	err := errors.New("Cloning into 'x'...\nremote: Counting objects: 10% (1/10)\rremote: Counting objects: 100% (10/10)\nfatal: could not read Username for 'https://github.com': terminal prompts disabled")
	if got := summary(err); got != "fatal: could not read Username for 'https://github.com': terminal prompts disabled" {
		t.Errorf("summary = %q", got)
	}
}
