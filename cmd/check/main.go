package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"gioui.org/app"

	"github.com/chromafish/check/internal/clone"
	"github.com/chromafish/check/internal/journal"
	"github.com/chromafish/check/internal/repo"
	"github.com/chromafish/check/internal/state"
	"github.com/chromafish/check/internal/ui"
	"github.com/chromafish/check/internal/vcs"
)

// Set at link time: -ldflags "-X main.version=v1.2.3".
var version = "dev"

func main() {
	revset := flag.String("r", "", "revisions to list: a jj revset, or arguments for git log")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: check [-r revset] [path | repository link]\n\n"+
			"With no path, the start screen: recent repositories, a folder, or a link.\n"+
			"Use \"check .\" for the current folder.\n\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	if *showVersion {
		fmt.Println("check", version)
		return
	}

	dir := "."
	if flag.NArg() > 0 {
		dir = flag.Arg(0)
	}
	journal.Start()

	var (
		open  vcs.Repo
		store *state.Store
	)
	// With no path the start screen comes up, to pick a recent repository,
	// choose a folder or paste a link; "check ." is the current folder. An
	// argument that is not a folder on disk but reads as a repository link
	// is cloned, or found among the clones already made.
	link := ""
	if flag.NArg() > 0 {
		if r, err := repo.Open(context.Background(), dir); err == nil {
			open, store = r, state.New()
		} else if _, statErr := os.Stat(dir); statErr != nil {
			if _, perr := clone.Parse(dir); perr == nil {
				link, dir = dir, "."
			} else {
				fmt.Fprintln(os.Stderr, "check:", err)
			}
		} else {
			fmt.Fprintln(os.Stderr, "check:", err)
		}
	}

	a := ui.New(open, dir, store, *revset)
	if link != "" {
		a.Clone(link)
	}
	go func() {
		if err := a.Run(); err != nil {
			fmt.Fprintln(os.Stderr, "check:", err)
			os.Exit(1)
		}
		os.Exit(0)
	}()
	app.Main()
}
