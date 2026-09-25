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
	return b, nil
}

// quote makes a bookmark name a revset string literal, so a name holding a
// slash or anything else revsets treat as an operator is still one name.
func quote(name string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(name) + `"`
}
