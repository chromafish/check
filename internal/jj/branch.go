package jj

import (
	"context"
	"fmt"
	"strings"

	"github.com/chromafish/check/internal/vcs"
)

// branchLimit bounds how many of a branch's commits are listed. A bookmark
// that has wandered thousands of commits from the trunk is not a branch
// anyone reviews commit by commit.
const branchLimit = 200

// Branches lists the local bookmarks, those on the most recent commits first.
func (r *Repo) Branches(ctx context.Context) ([]string, error) {
	revs, err := r.Log(ctx, "bookmarks()", 0)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, rev := range revs {
		names = append(names, rev.Bookmarks...)
	}
	return names, nil
}

// Branch reads one bookmark: its tip, where it leaves trunk(), and the
// commits in between.
func (r *Repo) Branch(ctx context.Context, name string) (vcs.Branch, error) {
	sym := quote(name)
	tip, err := r.Log(ctx, sym, 1)
	if err != nil {
		return vcs.Branch{}, err
	}
	if len(tip) == 0 {
		return vcs.Branch{}, fmt.Errorf("no bookmark %s", name)
	}
	b := vcs.Branch{Name: name, Tip: tip[0]}
	base, err := r.Log(ctx, fmt.Sprintf("heads(::trunk() & ::%s)", sym), 1)
	if err != nil {
		return vcs.Branch{}, err
	}
	b.Base = tip[0].CommitIDFull
	if len(base) > 0 {
		b.Base = base[0].CommitIDFull
	}
	b.Commits, err = r.Log(ctx, fmt.Sprintf("trunk()..%s", sym), branchLimit)
	if err != nil {
		return vcs.Branch{}, err
	}
	if len(b.Commits) == 0 {
		b.History, err = r.Log(ctx, fmt.Sprintf("::%s ~ root()", sym), historyLimit)
		if err != nil {
			return vcs.Branch{}, err
		}
	}
	return b, nil
}

// historyLimit is how much of a trunk's history is listed.
const historyLimit = 100

// CurrentBranch is the bookmark nearest below the working copy, which is
// the branch a jj user is building on.
func (r *Repo) CurrentBranch(ctx context.Context) (string, error) {
	revs, err := r.Log(ctx, "heads(::@ & bookmarks())", 1)
	if err != nil || len(revs) == 0 || len(revs[0].Bookmarks) == 0 {
		return "", err
	}
	return revs[0].Bookmarks[0], nil
}

// quote makes a bookmark name a revset string literal, so a name holding a
// slash or anything else revsets treat as an operator is still one name.
func quote(name string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(name) + `"`
}
