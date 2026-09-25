// Package clone turns a pasted repository link into a working copy on disk:
// cloned the first time, fetched every time after, and moved onto the branch
// the link named, if it named one.
//
// It is git throughout. A clone made here opens with the git backend, and one
// someone has since colocated with jj opens with jj; either way git is what
// fetches it.
//
// Every clone lives at a fixed place under one root, named after the host and
// the repository's path, so pasting the link to a repository that is already
// there finds it rather than cloning it again.
package clone

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/chromafish/check/internal/proc"
)

// Source is what a link says: where to clone from, where the clone goes, and
// the branch to be on.
type Source struct {
	// URL is what is handed to the clone.
	URL string
	// Host and Path name the repository, as in github.com and owner/repo.
	Host string
	Path string
	// Branch is the branch the link pointed into, or empty. A link to a
	// file on a branch runs the branch and the file's path together, since
	// a branch name may hold slashes too; Checkout tells them apart once it
	// can see the branches.
	Branch string
}

// Parse reads a pasted link. It takes the forms a browser's address bar or a
// "clone" button hands out:
//
//	https://github.com/owner/repo
//	https://github.com/owner/repo.git
//	https://github.com/owner/repo/tree/branch
//	https://gitlab.com/group/sub/repo/-/tree/branch
//	https://codeberg.org/owner/repo/src/branch/branch
//	https://bitbucket.org/owner/repo/src/branch
//	git@github.com:owner/repo.git
//	ssh://git@host/owner/repo.git
//	github.com/owner/repo
func Parse(raw string) (Source, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Source{}, errors.New("no link")
	}

	// scp-like ssh: git@host:owner/repo.git. It has no scheme and a colon
	// before the first slash.
	if !strings.Contains(raw, "://") {
		if at, colon := strings.Index(raw, "@"), strings.Index(raw, ":"); colon > 0 && (at < 0 || at < colon) &&
			(strings.Index(raw, "/") < 0 || colon < strings.Index(raw, "/")) {
			host := raw[:colon]
			if at >= 0 {
				host = raw[at+1 : colon]
			}
			path, err := cleanPath(raw[colon+1:])
			if err != nil {
				return Source{}, err
			}
			return Source{URL: raw, Host: strings.ToLower(host), Path: path}, nil
		}
		// Without a scheme, only something that starts with a host name
		// is a link: "src/check" is a folder that is not there, not a
		// server called src.
		host, _, _ := strings.Cut(raw, "/")
		if !strings.Contains(host, ".") {
			return Source{}, fmt.Errorf("not a repository link: %s", raw)
		}
		raw = "https://" + raw
	}

	u, err := url.Parse(raw)
	if err != nil {
		return Source{}, fmt.Errorf("not a repository link: %w", err)
	}
	if u.Host == "" {
		return Source{}, fmt.Errorf("not a repository link: %s", raw)
	}
	switch u.Scheme {
	case "https", "http", "ssh", "git":
	default:
		return Source{}, fmt.Errorf("cannot clone a %s link", u.Scheme)
	}

	p := strings.Trim(u.Path, "/")
	branch := ""
	if u.Scheme == "https" || u.Scheme == "http" {
		p, branch = splitBranch(p)
	}
	path, err := cleanPath(p)
	if err != nil {
		return Source{}, err
	}
	if strings.Count(path, "/") < 1 {
		return Source{}, fmt.Errorf("%s names no repository", raw)
	}

	clone := *u
	clone.Path = "/" + path
	if u.Scheme == "ssh" || u.Scheme == "git" {
		clone.Path += ".git"
	}
	clone.RawQuery, clone.Fragment = "", ""
	return Source{URL: clone.String(), Host: strings.ToLower(u.Hostname()), Path: path, Branch: branch}, nil
}

// splitBranch separates the repository's path from the branch part of a web
// link, which each forge spells its own way.
func splitBranch(p string) (repo, branch string) {
	// GitLab puts everything that is not the repository after "/-/", and
	// a group may be nested any number of levels deep before it.
	if before, after, ok := strings.Cut(p, "/-/"); ok {
		if rest, ok := strings.CutPrefix(after, "tree/"); ok {
			return before, unescape(rest)
		}
		return before, ""
	}
	parts := strings.Split(p, "/")
	if len(parts) < 3 {
		return p, ""
	}
	repo = strings.Join(parts[:2], "/")
	switch rest := parts[2:]; {
	case (rest[0] == "tree" || rest[0] == "blob") && len(rest) > 1: // GitHub, Gitea
		return repo, unescape(strings.Join(rest[1:], "/"))
	case rest[0] == "src" && len(rest) > 2 && rest[1] == "branch": // Gitea, Forgejo, Codeberg
		return repo, unescape(strings.Join(rest[2:], "/"))
	case rest[0] == "src" && len(rest) > 1: // Bitbucket
		return repo, unescape(strings.Join(rest[1:], "/"))
	case forgePages[rest[0]]:
		// A link to an issue, a commit or a pull request is a link into
		// the repository all the same; what follows is not a branch.
		return repo, ""
	}
	// Anything else is a deeper repository path: a GitLab project in a
	// nested group, linked without its "/-/".
	return p, ""
}

// forgePages are the pages under a repository a link may point at, which are
// not part of the repository's own path.
var forgePages = map[string]bool{
	"pull": true, "pulls": true, "issues": true, "commit": true, "commits": true,
	"compare": true, "releases": true, "tags": true, "branches": true, "actions": true,
	"wiki": true, "wikis": true, "settings": true, "pulse": true, "graphs": true,
	"security": true, "projects": true, "discussions": true, "network": true,
	"activity": true, "tree": true, "blob": true, "src": true, "raw": true, "blame": true,
}

func unescape(s string) string {
	if u, err := url.PathUnescape(s); err == nil {
		return u
	}
	return s
}

// cleanPath is a repository path fit to be part of a directory name: no
// ".git", no empty or dot components that would climb out of the root.
func cleanPath(p string) (string, error) {
	p = strings.TrimSuffix(strings.Trim(p, "/"), ".git")
	if p == "" {
		return "", errors.New("the link names no repository")
	}
	for _, part := range strings.Split(p, "/") {
		if part == "" || part == "." || part == ".." {
			return "", fmt.Errorf("unusable repository path %q", p)
		}
	}
	return p, nil
}

// Dir is where a source's clone lives under root.
func Dir(root string, s Source) string {
	return filepath.Join(root, s.Host, filepath.FromSlash(s.Path))
}

// DefaultRoot is where clones go when nothing else has been chosen.
func DefaultRoot() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(os.TempDir(), "check-clones")
	}
	return filepath.Join(home, "Clones")
}

// ExpandRoot reads a root as a person types it: a leading ~ is the home
// directory, and a relative path is taken from the home directory too, since
// the working directory of a window is not something anyone chose.
func ExpandRoot(root string) string {
	root = strings.TrimSpace(root)
	if root == "" {
		return DefaultRoot()
	}
	home, _ := os.UserHomeDir()
	if root == "~" {
		return home
	}
	if rest, ok := strings.CutPrefix(root, "~/"); ok {
		return filepath.Join(home, rest)
	}
	if !filepath.IsAbs(root) && home != "" {
		return filepath.Join(home, root)
	}
	return filepath.Clean(root)
}

// Result is what Ensure did.
type Result struct {
	Dir    string
	Cloned bool   // false when the clone was already there and was fetched
	Branch string // the branch the working copy is now on, or empty
	// Warning is something that went wrong without stopping the repository
	// from opening: a fetch that failed offline, a branch that was not
	// found.
	Warning string
}

// stall is how long git may go without printing anything before it is taken
// as stuck. Progress is printed through a whole transfer, so this is silence,
// not slowness: a prompt nobody can see, a server that stopped answering.
var stall = 90 * time.Second

// Ensure makes the source's clone exist under root and up to date with
// origin, and puts its working copy on the branch the link named. It is all
// git: a clone is the one thing both backends read the same way.
//
// What git prints is copied to progress as it runs. git is never allowed to
// ask anything: a clone runs in a window, where a question on the terminal
// it was started from is a hang nobody can see the cause of.
func Ensure(ctx context.Context, root string, s Source, progress io.Writer) (Result, error) {
	dir := Dir(root, s)
	res := Result{Dir: dir}
	bin, err := exec.LookPath("git")
	if err != nil {
		return res, errors.New("cloning needs git on the PATH")
	}
	if progress == nil {
		progress = io.Discard
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var stalled atomic.Bool
	watch := time.AfterFunc(stall, func() {
		stalled.Store(true)
		cancel()
	})
	defer watch.Stop()
	g := &git{ctx: ctx, bin: bin, env: quietEnv(ctx, bin), out: &resetting{w: progress, t: watch}}
	explain := func(err error) error {
		if stalled.Load() {
			return fmt.Errorf("git printed nothing for %s and was stopped", stall)
		}
		return err
	}

	if exists(dir) {
		fmt.Fprintf(progress, "$ git fetch origin\n")
		if _, err := g.run(dir, "fetch", "--progress", "--prune", "origin"); err != nil {
			if ctx.Err() != nil && !stalled.Load() {
				return res, ctx.Err()
			}
			res.Warning = "fetch failed, showing what was there: " + summary(explain(err))
		}
	} else {
		if err := g.clone(s, dir); err != nil {
			return res, explain(err)
		}
		res.Cloned = true
	}

	if s.Branch == "" {
		return res, nil
	}
	remote, err := g.remoteBranches(dir)
	if err != nil {
		return res, explain(err)
	}
	branch := pickBranch(s.Branch, remote)
	if branch == "" {
		res.Warning = fmt.Sprintf("no branch %q on origin", s.Branch)
		return res, nil
	}
	fmt.Fprintf(progress, "$ git checkout %s\n", branch)
	if err := g.checkout(dir, branch); err != nil {
		return res, explain(err)
	}
	res.Branch = branch
	return res, nil
}

// git runs git without letting it prompt, printing to out as it goes.
type git struct {
	ctx context.Context
	bin string
	env []string
	out io.Writer
}

func (g *git) run(dir string, args ...string) ([]byte, error) {
	return proc.RunTee(g.ctx, dir, g.bin, g.env, g.out, args...)
}

// quietEnv is the environment that keeps git from asking for anything: no
// username or password prompt on the terminal, no credential manager window,
// and ssh in batch mode, which still uses an agent's keys but never waits on
// a passphrase or a host key question. An ssh command someone has configured
// is left alone.
func quietEnv(ctx context.Context, bin string) []string {
	env := []string{"GIT_TERMINAL_PROMPT=0", "GCM_INTERACTIVE=never"}
	if os.Getenv("GIT_SSH_COMMAND") == "" && os.Getenv("GIT_SSH") == "" {
		if out, err := proc.Run(ctx, "", bin, "config", "--get", "core.sshCommand"); err != nil || strings.TrimSpace(string(out)) == "" {
			env = append(env, "GIT_SSH_COMMAND=ssh -o BatchMode=yes -o ConnectTimeout=20")
		}
	}
	return env
}

// resetting passes writes through and restarts the stall timer on each.
type resetting struct {
	w io.Writer
	t *time.Timer
}

func (r *resetting) Write(p []byte) (int, error) {
	r.t.Reset(stall)
	return r.w.Write(p)
}

// clone clones next to dir and moves the result into place, so that a clone
// interrupted part way — a closed window, a dropped connection — leaves
// nothing at dir for the next paste to mistake for a finished one.
//
// A web link to a private repository is the usual case of an https clone
// with no credentials behind it, and the same person usually has an ssh key
// for the host; so when https is refused for want of credentials, ssh is
// tried before giving up.
func (g *git) clone(s Source, dir string) error {
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return err
	}
	partial := dir + ".partial"
	try := func(url string) error {
		if err := os.RemoveAll(partial); err != nil {
			return err
		}
		fmt.Fprintf(g.out, "$ git clone %s\n", url)
		_, err := g.run(filepath.Dir(dir), "clone", "--progress", "--", url, partial)
		if err != nil {
			os.RemoveAll(partial)
		}
		return err
	}
	err := try(s.URL)
	if err != nil && g.ctx.Err() == nil && needsCredentials(err) {
		if alt := sshURL(s); alt != "" {
			fmt.Fprintf(g.out, "https needs credentials, trying ssh\n")
			if sshErr := try(alt); sshErr == nil {
				err = nil
			} else {
				return fmt.Errorf("%s/%s needs credentials: https has none, and ssh said %s",
					s.Host, s.Path, summary(sshErr))
			}
		}
	}
	if err != nil {
		return fmt.Errorf("cloning %s: %s", s.URL, summary(err))
	}
	return os.Rename(partial, dir)
}

// needsCredentials reports whether git failed for want of a login. A forge
// answers a private repository it will not admit to with "not found", so
// that counts too.
func needsCredentials(err error) bool {
	msg := strings.ToLower(err.Error())
	for _, s := range []string{
		"could not read username", "could not read password", "terminal prompts disabled",
		"authentication failed", "repository not found", "403", "401",
	} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}

// sshURL is the ssh spelling of an https source, or empty for one that is
// not https.
func sshURL(s Source) string {
	if !strings.HasPrefix(s.URL, "https://") && !strings.HasPrefix(s.URL, "http://") {
		return ""
	}
	return "git@" + s.Host + ":" + s.Path + ".git"
}

// summary is the part of what git printed that says what went wrong, without
// the progress lines around it.
func summary(err error) string {
	var keep []string
	for _, line := range strings.FieldsFunc(err.Error(), func(r rune) bool { return r == '\n' || r == '\r' }) {
		line = strings.TrimSpace(line)
		low := strings.ToLower(line)
		if strings.HasPrefix(low, "fatal:") || strings.HasPrefix(low, "error:") ||
			strings.HasPrefix(low, "remote: repository not found") || strings.Contains(low, "permission denied") {
			keep = append(keep, line)
		}
	}
	if len(keep) == 0 {
		return firstLine(err.Error())
	}
	return strings.Join(keep, " ")
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// remoteBranches lists the branches origin has, by name.
func (g *git) remoteBranches(dir string) ([]string, error) {
	out, err := g.run(dir, "for-each-ref", "--format=%(refname)", "refs/remotes/origin")
	if err != nil {
		return nil, err
	}
	var names []string
	for _, line := range strings.Fields(string(out)) {
		name := strings.TrimPrefix(line, "refs/remotes/origin/")
		if name != "HEAD" && name != line {
			names = append(names, name)
		}
	}
	return names, nil
}

// pickBranch finds the branch a link's branch part starts with. "a/b/c" may be
// the branch a/b and the file c, so the longest prefix that is a branch wins.
func pickBranch(want string, remote []string) string {
	have := make(map[string]bool, len(remote))
	for _, r := range remote {
		have[r] = true
	}
	parts := strings.Split(want, "/")
	for n := len(parts); n > 0; n-- {
		if b := strings.Join(parts[:n], "/"); have[b] {
			return b
		}
	}
	return ""
}

// checkout puts the working copy on a branch: made from origin's if there is
// no local one yet, and otherwise brought up to origin's when that needs no
// merge. Uncommitted work in the way stops it, and says so, rather than being
// carried or thrown away.
func (g *git) checkout(dir, branch string) error {
	if _, err := g.run(dir, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch); err == nil {
		if _, err := g.run(dir, "checkout", branch, "--"); err != nil {
			return fmt.Errorf("checking out %s: %s", branch, summary(err))
		}
		// A local branch that has gone its own way is left where it is.
		g.run(dir, "merge", "--ff-only", "origin/"+branch)
		return nil
	}
	if _, err := g.run(dir, "checkout", "-b", branch, "--track", "origin/"+branch); err != nil {
		return fmt.Errorf("checking out %s: %s", branch, summary(err))
	}
	return nil
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return line
}
