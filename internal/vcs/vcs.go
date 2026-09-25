// Package vcs defines the interface version control tools implement.
package vcs

import (
	"context"
	"io"
	"time"
)

// Side selects which version of a file a diff is asked for.
type Side int

const (
	// After is the file as the diff leaves it.
	After Side = iota
	// Before is the file as the diff found it.
	Before
)

// Repo is one open repository. Every method reads.
type Repo interface {
	// Root is the absolute path of the repository's top directory.
	Root() string

	// Info describes the tool backing this repository.
	Info() Info

	// Snapshot brings a tool's recorded working copy up to date with the files
	// on disk. It runs before a reload, so that edits made outside the
	// application are picked up. A tool with nothing to record does nothing.
	Snapshot(ctx context.Context) error

	// Log lists revisions matching a query, newest first, in topological
	// order. The query is written in the tool's own language, and an empty one
	// means the tool's default selection.
	Log(ctx context.Context, query string, limit int) ([]Revision, error)

	// Description returns the full commit message of a revision.
	Description(ctx context.Context, rev string) (string, error)

	// Files lists what a diff touches, with per-file line counts.
	Files(ctx context.Context, spec DiffSpec) ([]FileChange, error)

	// DiffStream returns the whole diff in git's unified format, with the given
	// number of context lines, as it is produced. A change is read from the top
	// while the rest of it is still being written, so the body of a large one
	// does not have to be complete before any of it can be shown.
	//
	// An unset spec is an empty diff, not an error. The reader must be closed,
	// and Close returns the diff's error.
	DiffStream(ctx context.Context, spec DiffSpec, context int) (io.ReadCloser, error)

	// FileDiff returns one file's diff, in the same format as DiffStream. It is
	// how a file left out of the change's diff is fetched on demand.
	FileDiff(ctx context.Context, spec DiffSpec, path string, context int) (string, error)

	// FileContent returns one side of a file as of a diff. Both sides are
	// needed to lex a hunk in the context of the whole file it came from, and
	// to fill in unchanged lines the diff left out. It takes the spec rather
	// than a revision because naming the other side of a diff is the tool's
	// business, not the caller's.
	FileContent(ctx context.Context, spec DiffSpec, path string, side Side) ([]byte, error)

	// Workspace makes dir a second working copy of the repository, at rev.
	// The directory is created the first time and moved to the revision
	// after that, so what a build left in it is still there for the next.
	// The repository's own working copy is not touched. This is the one
	// method that writes, and it writes only to dir and to the tool's
	// records of it.
	Workspace(ctx context.Context, dir, rev string) error

	// Branches lists the repository's local branches — bookmarks in jj —
	// most recently moved first.
	Branches(ctx context.Context) ([]string, error)

	// Branch describes one branch as a unit of review: where it forks from
	// the trunk and the commits it has of its own.
	Branch(ctx context.Context, name string) (Branch, error)
}

// Branch is a named line of work, reviewed whole as the range from where it
// leaves the trunk to its tip.
type Branch struct {
	Name string
	// Tip is the revision the name points at.
	Tip Revision
	// Base is the commit the branch forks from, the "from" side of its
	// range. It is the tip itself for a branch with no commits of its own.
	Base string
	// Commits are the branch's own commits, newest first.
	Commits []Revision
}

// Spec is the diff of the whole branch.
func (b Branch) Spec() DiffSpec {
	return DiffSpec{Kind: DiffRange, From: b.Base, To: b.Tip.CommitIDFull}
}

// Info describes the tool backing a repository.
type Info struct {
	// Name is the command, and Version what it reports for itself.
	Name    string
	Version string

	// QueryLabel names the query field and QueryHint is what it shows while
	// empty, so the field says which query language it is taking.
	QueryLabel string
	QueryHint  string

	// DefaultQuery is what to ask for when nothing has been typed. A tool's
	// own default is written for a terminal, where the log is scrollback and
	// a short answer is the courteous one; the revisions pane is a standing
	// column that costs the same whether or not it is filled. A backend whose
	// default answers the narrower question names a wider one here, and an
	// empty string takes the tool's.
	DefaultQuery string

	// Handoff tells an agent how to get to a change. It takes the revision's
	// identifier twice: once to look at, once to move to.
	Handoff string
}

// Revision is one entry in the log.
//
// ChangeID and CommitID differ only where a tool keeps an identity for a
// change across rewrites; otherwise they are the same string. A note is filed
// under the change, so it survives a rewrite wherever the tool allows one.
type Revision struct {
	ChangeID     string
	ChangeIDFull string
	CommitID     string
	CommitIDFull string
	Description  string // first line only
	Author       string
	Email        string
	Timestamp    time.Time

	// WorkingCopy marks the revision holding uncommitted work.
	WorkingCopy bool

	// Marks a tool may not have, left unset rather than invented.
	Immutable bool
	Conflict  bool
	Divergent bool
	Hidden    bool

	Empty bool
	Root  bool

	// Named pointers at this revision: branches, or whatever the tool calls
	// them.
	Bookmarks       []string
	RemoteBookmarks []string
	Tags            []string

	// Parents holds the short identifiers of this revision's parents, matching
	// the ChangeID of other revisions in the same list.
	Parents []string

	// Graph is the ancestry drawing for this row, filled in by BuildGraph.
	Graph GraphRow
}

// Subject is the description's first line, or a placeholder where there is no
// description.
func (r Revision) Subject() string {
	if r.Description == "" {
		return "(no description set)"
	}
	return r.Description
}

// DiffKind selects how a diff is framed.
type DiffKind int

const (
	// DiffChange shows what a single revision does: its parent against itself.
	DiffChange DiffKind = iota
	// DiffRange shows the cumulative difference between two revisions.
	DiffRange
)

// DiffSpec names a diff to compute.
type DiffSpec struct {
	Kind DiffKind
	Rev  string // DiffChange
	From string // DiffRange
	To   string // DiffRange
}

// Empty reports whether the spec is unset, as it is before anything has been
// selected.
func (s DiffSpec) Empty() bool {
	switch s.Kind {
	case DiffRange:
		return s.From == "" || s.To == ""
	default:
		return s.Rev == ""
	}
}

// Describe renders the spec the way a command line would express it.
func (s DiffSpec) Describe() string {
	switch s.Kind {
	case DiffRange:
		return s.From + " → " + s.To
	default:
		return s.Rev
	}
}

// Status is how a file changed between the two sides of a diff.
type Status byte

const (
	Modified Status = 'M'
	Added    Status = 'A'
	Deleted  Status = 'D'
	Renamed  Status = 'R'
	Copied   Status = 'C'
)

func (s Status) String() string { return string(rune(s)) }

// FileChange is one entry in a diff's file list.
type FileChange struct {
	Status  Status
	Path    string // path on the "to" side; for deletions, the path that went away
	OldPath string // set for renames and copies
	Added   int
	Removed int
	Binary  bool
}

// Display is the path as it should be shown, spelling out renames.
func (f FileChange) Display() string {
	if f.OldPath != "" && f.OldPath != f.Path {
		return f.OldPath + " → " + f.Path
	}
	return f.Path
}
