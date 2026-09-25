package signals

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chromafish/check/internal/diffparse"
	"github.com/chromafish/check/internal/vcs"
)

func found(t *testing.T, p, src string) []string {
	t.Helper()
	var out []string
	for _, s := range Scan(Builtin(), p, strings.Split(src, "\n")) {
		out = append(out, fmt.Sprintf("%d %s %s %q", s.Line, s.Kind, s.Detector, s.Name))
	}
	return out
}

func want(t *testing.T, got []string, lines ...string) {
	t.Helper()
	if strings.Join(got, "\n") != strings.Join(lines, "\n") {
		t.Errorf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(lines, "\n"))
	}
}

func TestGo(t *testing.T) {
	want(t, found(t, "store.go", `package store

var repacks = prometheus.NewCounter(prometheus.CounterOpts{
	Namespace: "store",
	Name:      "repack_total",
})

func (s *Store) Consolidate(ctx context.Context) error {
	ctx, span := tracer.Start(ctx, "store.consolidate")
	defer span.End()
	slog.InfoContext(ctx, "consolidating", "view", s.view)
	if err != nil {
		span.RecordError(err)
		logger.Error("consolidate failed", zap.Error(err))
	}
	if client.BoolVariation("fast-repack", user, false) {
	}
	fmt.Println("not telemetry")
}
`),
		`3 metric prometheus-go "repack_total"`,
		`9 span otel-go-span "store.consolidate"`,
		`11 log go-log "consolidating"`,
		`13 error span-error ""`,
		`14 log go-log "consolidate failed"`,
		`16 flag launchdarkly "fast-repack"`)
}

func TestRust(t *testing.T) {
	want(t, found(t, "src/store.rs", `#[instrument(skip(self))]
pub async fn consolidate(&self) -> Result<()> {
    let _g = info_span!("repack", tries = 3).entered();
    counter!("repack_total").increment(1);
    warn!(tries, "retrying consolidate");
    tracing::error!(target: "store", "gave up");
    Ok(())
}
`),
		`1 span tracing-instrument "consolidate"`,
		`3 span tracing-span "repack"`,
		`4 metric metrics-rs "repack_total"`,
		`5 log rust-log "retrying consolidate"`,
		`6 log rust-log "gave up"`)
}

func TestPythonAndJS(t *testing.T) {
	want(t, found(t, "app/jobs.py", `import logging
log = logging.getLogger(__name__)
REQS = Counter("jobs_total", "jobs run")

def run():
    with tracer.start_as_current_span("jobs.run"):
        log.warning("slow job %s", name)
`),
		`3 metric prometheus-py "jobs_total"`,
		`6 span otel-py-span "jobs.run"`,
		`7 log python-logging "slow job %s"`)
	want(t, found(t, "src/api.ts", "const h = new client.Histogram({\n  name: 'http_seconds',\n});\nlogger.info({ id }, `served`);\n"),
		`1 metric prom-client "http_seconds"`,
		`4 log js-log "served"`)
}

// A repository declares its own detectors in check.toml, beside the
// built-ins.
func TestLoadReadsTheRepositorysDetectors(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, File), []byte(`[[signal]]
name = "audit"
kind = "event"
extensions = [".rs"]
pattern = 'audit::record\(\s*"([^"]+)"'
`), 0o644)
	ds, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := Scan(ds, "a.rs", []string{`audit::record("repo.deleted", id);`})
	if len(got) != 1 || got[0].Detector != "audit" || got[0].Name != "repo.deleted" || got[0].Kind != Event {
		t.Errorf("got %+v", got)
	}

	os.WriteFile(filepath.Join(dir, File), []byte("[[signal]]\nname = \"bad\"\npattern = '('\n"), 0o644)
	if _, err := Load(dir); err == nil || !strings.Contains(err.Error(), "bad") {
		t.Errorf("a bad pattern loaded: %v", err)
	}
}

const before = `package store

func Open() {
	slog.Info("opening")
}

func Size() int {
	return 1
}

func Old() {
	slog.Warn("old path taken")
}
`

const after = `package store

func Open() {
	slog.Info("opening", "fresh", true)
}

func Size() int {
	return 2
}

func Consolidate(ctx context.Context) {
	ctx, span := tracer.Start(ctx, "consolidate")
	defer span.End()
}
` + "\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n" + `
func Far() {
	doSomething()
}
`

// A change's inventory says which signals it adds, removes and rewrites,
// which untouched ones sit beside it, and where it changes code nothing
// observes.
func TestBuild(t *testing.T) {
	diff := unified(t, "store.go", before, after)
	files := diffparse.Parse(diff)
	read := func(_ context.Context, p string, side vcs.Side) ([]byte, error) {
		if side == vcs.Before {
			return []byte(before), nil
		}
		return []byte(after), nil
	}
	inv, err := Build(context.Background(), Builtin(), []*diffparse.File{&files[0]}, read)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range inv.Signals {
		got = append(got, fmt.Sprintf("%s %d %q", f.Status, f.Line, f.Name))
	}
	want(t, got,
		`added 12 "consolidate"`,
		`removed 12 "old path taken"`,
		`altered 4 "opening"`)
	if len(inv.Dark) != 1 || inv.Dark[0].Line < 30 {
		t.Errorf("dark = %+v, want Far alone", inv.Dark)
	}
}

// unified is the diff git would print for one file.
func unified(t *testing.T, p, a, b string) string {
	t.Helper()
	dir := t.TempDir()
	fa, fb := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	os.WriteFile(fa, []byte(a), 0o644)
	os.WriteFile(fb, []byte(b), 0o644)
	out, err := runGit(dir, "diff", "--no-index", "--no-color", fa, fb)
	if err != nil && out == "" {
		t.Skip("git is not available")
	}
	out = strings.ReplaceAll(out, "a"+fa, "a/"+p)
	out = strings.ReplaceAll(out, "b"+fb, "b/"+p)
	return out
}

func runGit(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	return string(out), err
}
