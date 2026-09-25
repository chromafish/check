package git

import (
	"context"
	"fmt"
	"strings"

	"github.com/chromafish/check/internal/vcs"
)

// branchLimit bounds how many of a branch's commits are listed.
const branchLimit = 200

// Branches lists the local branches, most recently committed to first.
func (r *Repo) Branches(ctx context.Context) ([]string, error) {
	out, err := r.read(ctx, "for-each-ref", "--sort=-committerdate", "--format=%(refname:short)", "refs/heads")
	if err != nil {
		return nil, err
	}
	return strings.Fields(string(out)), nil
}

// Branch reads one branch: its tip, where it leaves the trunk, and the
// commits in between.
func (r *Repo) Branch(ctx context.Context, name string) (vcs.Branch, error) {
	ref := "refs/heads/" + name
	out, err := r.read(ctx, "log", "-1", "--decorate=full", "--format="+logFormat, ref)
	if err != nil {
		return vcs.Branch{}, err
	}
	tip, err := parseLog(string(out))
	if err != nil {
		return vcs.Branch{}, err
	}
	if len(tip) == 0 {
		return vcs.Branch{}, fmt.Errorf("no branch %s", name)
	}
	b := vcs.Branch{Name: name, Tip: tip[0], Base: tip[0].CommitIDFull}
	trunk := r.trunk(ctx, name)
	if trunk == "" {
		return b, nil
	}
	base, err := r.read(ctx, "merge-base", trunk, ref)
	if err != nil {
		// Unrelated histories share no base; the branch is reviewed as its
		// tip alone.
		return b, nil
	}
	b.Base = strings.TrimSpace(string(base))
	out, err = r.read(ctx, "log", "--topo-order", "--decorate=full", "--format="+logFormat,
		"-n", fmt.Sprint(branchLimit), trunk+".."+ref)
	if err != nil {
		return vcs.Branch{}, err
	}
	b.Commits, err = parseLog(string(out))
	return b, err
}

// trunk is the branch others fork from: what origin's HEAD names, or else a
// local main or master. It is never the branch being asked about, which is
// the trunk's own business and has no fork point from itself.
func (r *Repo) trunk(ctx context.Context, name string) string {
	var candidates []string
	if out, err := r.read(ctx, "symbolic-ref", "--quiet", "refs/remotes/origin/HEAD"); err == nil {
		candidates = append(candidates, strings.TrimSpace(string(out)))
	}
	candidates = append(candidates, "refs/heads/main", "refs/heads/master")
	for _, c := range candidates {
		if c == "refs/heads/"+name {
			continue
		}
		if _, err := r.read(ctx, "rev-parse", "--verify", "--quiet", c); err == nil {
			return c
		}
	}
	return ""
}
