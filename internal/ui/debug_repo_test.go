package ui

import (
	"github.com/chromafish/check/internal/diffparse"
	"os"
	"strings"
	"testing"
)

func TestDebugRepo(t *testing.T) {
	diffBytes, _ := os.ReadFile("/tmp/diff0001.txt")
	if len(diffBytes) == 0 {
		t.Skip("no diff")
	}
	parsed := diffparse.Parse(string(diffBytes))
	for _, f := range parsed {
		if strings.Contains(f.Path(), "0001") {
			t.Logf("file %s hunks %d", f.Path(), len(f.Hunks))
			for hi, h := range f.Hunks {
				t.Logf("hunk %d old %d,%d new %d,%d", hi, h.OldStart, h.OldCount, h.NewStart, h.NewCount)
				for _, l := range h.Lines {
					if l.Kind == diffparse.Removed || l.Kind == diffparse.Added {
						kind := "A"
						if l.Kind == diffparse.Removed {
							kind = "R"
						}
						t.Logf(" %s line %d/%d segs %d text %.120q", kind, l.OldNum, l.NewNum, len(l.Segments), l.Text[:min(120, len(l.Text))])
						for _, s := range l.Segments {
							t.Logf("   seg %d-%d changed=%v %.40q", s.Start, s.End, s.Changed, l.Text[s.Start:min(s.End, len(l.Text))])
						}
					}
				}
			}
		}
	}
}
