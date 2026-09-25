package ui

import (
	"context"
	"image"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"gioui.org/f32"
	"gioui.org/io/input"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/chromafish/check/internal/jev"
	"github.com/chromafish/check/internal/repo"
	"github.com/chromafish/check/internal/state"

	"github.com/chromafish/check/reef"
)

// harness drives a real App through Gio's input router, without a window or a
// GPU. Laying out only builds operations, so everything the interface does in
// response to a click or a keystroke can be exercised here.
type harness struct {
	t      *testing.T
	app    *App
	router input.Router
	ops    op.Ops
	size   image.Point
	now    time.Time
}

// newHarness builds a small jj repository and opens the application on it.
func newHarness(t *testing.T) *harness {
	t.Helper()
	if _, err := exec.LookPath("jj"); err != nil {
		t.Skip("jj is not installed")
	}
	return openHarness(t, newRepo(t))
}

// newGitHarness is the same thing on a git repository, which the interface has
// to drive through the same code with none of jj's answers available.
func newGitHarness(t *testing.T) *harness {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	return openHarness(t, newGitRepo(t))
}

func openHarness(t *testing.T, dir string) *harness {
	t.Helper()
	return openHarnessView(t, dir, false)
}

// openHarnessView opens the repository in the brief view when brief is set,
// and in the classic view otherwise.
func openHarnessView(t *testing.T, dir string, brief bool) *harness {
	t.Helper()
	ctx := context.Background()
	repo, err := repo.Open(ctx, dir)
	if err != nil {
		t.Fatalf("opening the repository: %v", err)
	}
	// Review state must not leak into the real one, or between tests.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	// Nor may a key in the environment send a test's diff to the service.
	t.Setenv(jev.EnvKey, "")
	// Most of what is tested is the classic view's; the brief's own tests
	// open the default instead.
	if !brief {
		if err := state.SaveSettings(state.Settings{Classic: true}); err != nil {
			t.Fatal(err)
		}
	}

	query := "all()"
	if repo.Info().Name == "git" {
		query = ""
	}
	h := &harness{
		t:    t,
		app:  NewOffscreen(repo, dir, state.New(), query),
		size: image.Pt(1400, 900),
		now:  time.Now(),
	}
	h.app.Start()
	h.settle()
	return h
}

// newRepo creates a repository with one committed change and one working-copy
// change that modifies a file, which is the shape most of the interface cares
// about.
func newRepo(t *testing.T) string {
	t.Helper()
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

	// wide.go is long enough, and changed in two places far enough apart, to
	// leave real runs of unchanged code between its hunks — which is what the
	// expandable gaps are for.
	wide := func(tenth, fiftieth string) string {
		var b strings.Builder
		b.WriteString("package wide\n")
		for i := 2; i <= 60; i++ {
			switch i {
			case 10:
				b.WriteString("// " + tenth + "\n")
			case 50:
				b.WriteString("// " + fiftieth + "\n")
			default:
				b.WriteString("var v" + strconv.Itoa(i) + " = " + strconv.Itoa(i) + "\n")
			}
		}
		return b.String()
	}

	run("git", "init")
	write("main.go", "package main\n\nfunc main() {\n\tprintln(\"one\")\n}\n")
	write("wide.go", wide("before ten", "before fifty"))
	run("describe", "-m", "first")
	run("new", "-m", "second")
	write("main.go", "package main\n\nfunc main() {\n\tprintln(\"two\")\n\tprintln(\"three\")\n}\n")
	write("wide.go", wide("after ten", "after fifty"))
	write("added.txt", "brand new\n")
	run("status") // snapshot the working copy
	return dir
}

// newGitRepo builds the git equivalent of newRepo: one commit, then edits left
// uncommitted in the working tree, which is the shape the interface sees when
// somebody opens Check on work in progress.
func newGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com",
			"GIT_AUTHOR_DATE=2026-01-01T00:00:00+00:00",
			"GIT_COMMITTER_DATE=2026-01-01T00:00:00+00:00")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	wide := func(tenth, fiftieth string) string {
		var b strings.Builder
		b.WriteString("package wide\n")
		for i := 2; i <= 60; i++ {
			switch i {
			case 10:
				b.WriteString("// " + tenth + "\n")
			case 50:
				b.WriteString("// " + fiftieth + "\n")
			default:
				b.WriteString("var v" + strconv.Itoa(i) + " = " + strconv.Itoa(i) + "\n")
			}
		}
		return b.String()
	}

	run("init", "-b", "main")
	write("main.go", "package main\n\nfunc main() {\n\tprintln(\"one\")\n}\n")
	write("wide.go", wide("before ten", "before fifty"))
	run("add", ".")
	run("commit", "-m", "first")

	write("main.go", "package main\n\nfunc main() {\n\tprintln(\"two\")\n\tprintln(\"three\")\n}\n")
	write("wide.go", wide("after ten", "after fifty"))
	write("added.txt", "brand new\n")
	run("add", "added.txt")
	return dir
}

// frame lays out one frame and delivers whatever it produced to the router.
func (h *harness) frame() {
	h.t.Helper()
	h.ops.Reset()
	gtx := layout.Context{
		Ops:         &h.ops,
		Metric:      unit.Metric{PxPerDp: 1, PxPerSp: 1},
		Constraints: layout.Exact(h.size),
		Now:         h.now,
		Source:      h.router.Source(),
	}
	h.app.Layout(gtx)
	h.router.Frame(&h.ops)
}

// settle draws until all background work has landed.
func (h *harness) settle() {
	h.t.Helper()
	// Generous, because it is only ever a failure deadline: under the race
	// detector a change of a hundred thousand lines takes most of a minute.
	deadline := time.Now().Add(90 * time.Second)
	for {
		h.frame()
		if h.app.Idle() {
			// One more frame so results applied during the last one are drawn.
			h.frame()
			return
		}
		if time.Now().After(deadline) {
			h.t.Fatal("background work did not finish")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// advance moves the frame clock, for behaviour that depends on time passing.
func (h *harness) advance(d time.Duration) {
	h.now = h.now.Add(d)
	h.frame()
}

// click presses and releases at a point, then settles.
func (h *harness) click(x, y int) {
	h.t.Helper()
	pos := f32.Pt(float32(x), float32(y))
	h.router.Queue(
		pointer.Event{Kind: pointer.Move, Source: pointer.Mouse, Position: pos},
		pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: pos},
		pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: pos},
	)
	h.settle()
}

// clickOnce delivers a click and lays out exactly one frame, which is the frame
// the control's own handler runs in. Anything it defers has not happened yet.
func (h *harness) clickOnce(x, y int) {
	h.t.Helper()
	pos := f32.Pt(float32(x), float32(y))
	h.router.Queue(
		pointer.Event{Kind: pointer.Move, Source: pointer.Mouse, Position: pos},
		pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: pos},
		pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: pos},
	)
	h.frame()
}

// hover moves the pointer without pressing.
func (h *harness) hover(x, y int) {
	h.t.Helper()
	h.router.Queue(pointer.Event{
		Kind: pointer.Move, Source: pointer.Mouse,
		Position: f32.Pt(float32(x), float32(y)),
	})
	h.frame()
	h.frame()
}

// press sends one keystroke and settles.
func (h *harness) press(name key.Name, mods key.Modifiers) {
	h.t.Helper()
	h.router.Queue(
		key.Event{Name: name, Modifiers: mods, State: key.Press},
		key.Event{Name: name, Modifiers: mods, State: key.Release},
	)
	h.settle()
}

// typeText feeds text to whatever field has focus.
func (h *harness) typeText(s string) {
	h.t.Helper()
	h.router.Queue(key.EditEvent{Text: s})
	h.frame()
	h.frame()
}

// clipboard returns the text the application asked to have copied.
func (h *harness) clipboard() string {
	h.t.Helper()
	mime, content, ok := h.router.WriteClipboard()
	if !ok {
		return ""
	}
	if mime != "application/text" {
		h.t.Fatalf("clipboard mime = %q", mime)
	}
	return string(content)
}

// Geometry helpers, so tests describe where they are clicking in the same
// terms the layout does rather than in bare numbers.

func (h *harness) headerHeight() int { return h.gtx().Dp(reef.TopbarH) }

// rowHeight is the pitch of a list row, which the system fixes as a token.
func (h *harness) rowHeight() int { return h.app.ui.Row(h.gtx()) }

// paneX returns an x inside the given pane.
func (h *harness) paneX(p Pane) int {
	gtx := layout.Context{
		Ops:         &h.ops,
		Metric:      unit.Metric{PxPerDp: 1, PxPerSp: 1},
		Constraints: layout.Exact(h.size),
	}
	w := h.app.splits.Widths(gtx, h.size.X)
	revs, files := w[0], w[1]
	switch p {
	case PaneRevs:
		return revs / 2
	case PaneFiles:
		return revs + 1 + files/2
	default:
		return revs + files + 2 + (h.size.X-revs-files-2)/2
	}
}

// listY returns the y of the nth row of a pane's list.
func (h *harness) listY(n int) int {
	panelHead := h.gtx().Dp(reef.ControlH)
	return h.headerHeight() + panelHead + 1 + n*h.rowHeight() + h.rowHeight()/2
}

// gtx returns a context with the same metrics the frames use, for asking the
// layout where things are.
func (h *harness) gtx() layout.Context {
	return layout.Context{
		Ops:         &h.ops,
		Metric:      unit.Metric{PxPerDp: 1, PxPerSp: 1},
		Constraints: layout.Exact(h.size),
	}
}

// diffRowY returns the y of the nth row of the diff body, using the origin the
// last frame recorded rather than recomputing the layout.
func (h *harness) diffRowY(n int) int {
	h.t.Helper()
	top, ok := h.app.rowTops[n]
	if !ok {
		h.t.Fatalf("diff row %d was not on screen in the last frame", n)
	}
	return top + h.app.rowHeights[n]/2
}

// diffGutterX returns an x inside the diff pane's gutter, where the comment
// affordance lives.
func (h *harness) diffGutterX() int { return h.app.diffColX + 6 }

// firstLineRow returns the index of the first row of the diff that is an
// actual line of code rather than a hunk header.
func (h *harness) firstLineRow() int {
	h.t.Helper()
	doc := h.app.diff
	if doc == nil {
		h.t.Fatal("no diff is loaded")
	}
	for i := doc.Cursor; i < len(doc.Rows); i++ {
		r := doc.Row(i)
		if _, drawn := h.app.rowTops[i]; !drawn {
			continue
		}
		if r.Kind == rowLine && r.Line.NewNum+r.Line.OldNum > 0 {
			return i
		}
	}
	h.t.Fatal("no code line of the diff is on screen")
	return 0
}

// drag presses at one point, moves to another, and releases.
func (h *harness) drag(x0, y0, x1, y1 int) {
	h.t.Helper()
	from := f32.Pt(float32(x0), float32(y0))
	to := f32.Pt(float32(x1), float32(y1))
	h.router.Queue(pointer.Event{
		Kind: pointer.Press, Source: pointer.Mouse,
		Buttons: pointer.ButtonPrimary, Position: from,
	})
	h.frame()
	// The router synthesises Drag from a Move with a button held; queueing a
	// Drag directly is not a supported input event.
	h.router.Queue(pointer.Event{
		Kind: pointer.Move, Source: pointer.Mouse,
		Buttons: pointer.ButtonPrimary, Position: to,
	})
	h.frame()
	h.router.Queue(pointer.Event{
		Kind: pointer.Release, Source: pointer.Mouse, Position: to,
	})
	h.settle()
}

// selectFileWithRows opens the first file in the manifest whose diff has at
// least n rows, so a test that needs several lines does not land on a one line
// file and fail for the wrong reason.
func (h *harness) selectFileWithRows(n int) {
	h.t.Helper()
	for i := range h.app.files {
		if doc := h.app.diff; doc != nil && i < len(doc.Files) && len(doc.Files[i].base) < n {
			continue
		}
		h.click(h.paneX(PaneFiles), h.listY(i))
		return
	}
	h.t.Fatalf("no file in the manifest has a diff of %d rows", n)
}

// selectFileNamed opens the first file in the manifest whose path ends with
// suffix.
func (h *harness) selectFileNamed(suffix string) {
	h.t.Helper()
	for i, f := range h.app.files {
		if strings.HasSuffix(f.Path, suffix) {
			h.click(h.paneX(PaneFiles), h.listY(i))
			return
		}
	}
	h.t.Fatalf("no file ending in %q in the manifest", suffix)
}

// manifestBoxX returns an x inside the read/unread box of a manifest row,
// from the position the last frame recorded.
func (h *harness) manifestBoxX() int {
	h.t.Helper()
	if h.app.viewedBoxW == 0 {
		h.t.Fatal("no frame has laid out a manifest row yet")
	}
	return h.app.filesColX + h.app.viewedBoxX + h.app.viewedBoxW/2
}

// amendWorkingCopy edits a file in the repository, which rewrites the
// working-copy change and gives it a new commit ID.
func (h *harness) amendWorkingCopy(name, body string) {
	h.t.Helper()
	if err := os.WriteFile(filepath.Join(h.app.repo.Root(), name), []byte(body), 0o644); err != nil {
		h.t.Fatal(err)
	}
}
