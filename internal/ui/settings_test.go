package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gioui.org/font"
	"gioui.org/io/key"

	"github.com/chromafish/check/internal/state"

	"github.com/chromafish/check/reef"
)

// The sheet is modal. While it is up the keyboard belongs to it, so a keystroke
// meant for the typeface list does not also move a selection in the review
// underneath, out of sight behind it.
func TestSettingsSheetTakesTheKeyboard(t *testing.T) {
	h := newHarness(t)
	h.press(key.NameTab, 0)
	if h.app.focus != PaneFiles {
		t.Fatalf("focus is %v, want the manifest", h.app.focus)
	}
	was := h.app.fileSel

	h.press(",", key.ModShortcut)
	if !h.app.settingsOpen {
		t.Fatal("cmd-, did not open the settings sheet")
	}

	h.press("J", 0)
	if h.app.fileSel != was {
		t.Errorf("j moved the manifest to row %d under the sheet, want it left at %d", h.app.fileSel, was)
	}

	h.press(key.NameEscape, 0)
	if h.app.settingsOpen {
		t.Error("escape did not close the sheet")
	}
}

// The body size is what the whole type scale is anchored on, so setting it
// larger has to make the rows the interface is laid out on taller.
func TestSettingTheSizeScalesTheType(t *testing.T) {
	h := newHarness(t)
	before := h.app.ui.Row(h.app.ui.Sized(h.gtx()))

	h.press(",", key.ModShortcut)
	for range 12 {
		h.press("=", 0)
	}
	if got := h.app.ui.Size; got != reef.MaxFontSize {
		t.Fatalf("the body size is %v pt, want it to stop at %v", got, reef.MaxFontSize)
	}
	if after := h.app.ui.Row(h.app.ui.Sized(h.gtx())); after <= before {
		t.Errorf("a row is %dpx at %v pt and was %dpx at the default; it should have grown", after, h.app.ui.Size, before)
	}

	for range 40 {
		h.press("-", 0)
	}
	if got := h.app.ui.Size; got != reef.MinFontSize {
		t.Errorf("the body size is %v pt, want it to stop at %v", got, reef.MinFontSize)
	}
}

// Preferences belong to the person rather than to the repository, so they are
// written when they are set and read back by the next application to start.
func TestPreferencesSurviveTheApplication(t *testing.T) {
	h := newHarness(t)
	h.press(",", key.ModShortcut)
	h.press("=", 0)
	h.press("=", 0)

	want := h.app.ui.Size
	if want == reef.SizeUI {
		t.Fatal("two presses of + did not change the body size")
	}
	if stored := state.LoadSettings().FontSize; stored != int(want) {
		t.Errorf("the settings file records %d pt, want %d", stored, int(want))
	}

	again := NewOffscreen(h.app.repo, h.app.dir, h.app.store, "all()")
	if again.ui.Size != want {
		t.Errorf("a fresh application opened at %v pt, want the %v pt that was set", again.ui.Size, want)
	}
}

// Choosing a family sets the interface in it, with the face that ships in the
// binary left standing behind it: Gio reads a typeface as a list of families,
// so anything the chosen one has no glyph for is still drawn.
func TestChoosingATypefaceSetsTheInterfaceInIt(t *testing.T) {
	h := newHarness(t)
	h.press(",", key.ModShortcut)
	// The sheet opens on the colours; tab moves the cursor to the typefaces.
	h.press(key.NameTab, 0)
	if len(h.app.families) < 2 {
		t.Skip("no monospaced typefaces installed to choose from")
	}

	h.press("J", 0)
	want := h.app.families[1]
	if got := h.app.ui.Family; got != want {
		t.Fatalf("the interface is set in %q, want %q", got, want)
	}
	face := string(h.app.ui.Font(reef.WeightBody, font.Regular).Typeface)
	if !strings.HasPrefix(face, want) {
		t.Errorf("the working typeface is %q, want it to lead with %q", face, want)
	}
	if !strings.HasSuffix(face, string(reef.Mono)) {
		t.Errorf("the working typeface is %q, want %q left behind it as the fallback", face, reef.Mono)
	}

	h.press("K", 0)
	if h.app.ui.Family != "" {
		t.Errorf("stepping back up the list left the interface in %q, want the built-in face", h.app.ui.Family)
	}
}

// The list always offers the face in the binary, whatever the scan of the
// system's fonts turns up, and never offers a name a typeface cannot carry.
func TestTypefaceListLeadsWithTheBuiltInFace(t *testing.T) {
	h := newHarness(t)
	h.press(",", key.ModShortcut)
	h.press(key.NameTab, 0)

	if len(h.app.families) == 0 || h.app.families[0] != "" {
		t.Fatalf("the list is %q, want the built-in face at the head of it", h.app.families)
	}
	if got := familyLabel(h.app.families[0]); got != string(reef.Mono) {
		t.Errorf("the built-in face is named %q, want %q", got, reef.Mono)
	}
	for _, f := range h.app.families[1:] {
		if !reef.UsableFamily(f) {
			t.Errorf("the list offers %q, which is not a family a typeface can name", f)
		}
	}
}

// A base16 scheme, in the form they are published in. Devs bring the colours
// they already use everywhere else, so the file a person drops in is one they
// downloaded rather than one they wrote.
const gruvboxScheme = `scheme: "Gruvbox dark, medium"
base00: "282828" # ----
base01: "3c3836"
base02: "504945"
base03: "665c54"
base04: "bdae93"
base05: "d5c4a1"
base06: "ebdbb2"
base07: "fbf1c7"
base08: "fb4934"
base09: "fe8019"
base0A: "fabd2f"
base0B: "b8bb26"
base0C: "8ec07c"
base0D: "83a598"
base0E: "d3869b"
base0F: "d65d0e"
`

// A scheme dropped into the themes directory turns up in the sheet, is applied
// as the cursor lands on it, and is written down so the next window opens in
// it.
func TestChoosingAThemeDrawsTheInterfaceInIt(t *testing.T) {
	h := newHarness(t)
	dir, err := state.ThemesDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "gruvbox.yaml"), []byte(gruvboxScheme), 0o644); err != nil {
		t.Fatal(err)
	}

	before := h.app.ui.P.Bg
	h.press(",", key.ModShortcut)
	builtin := len(reef.Builtin())
	if len(h.app.schemes) != builtin+1 {
		t.Fatalf("the sheet offers %d schemes, want the %d built in and gruvbox", len(h.app.schemes), builtin+1)
	}
	if h.app.schemes[0].ID != reef.Default().ID {
		t.Errorf("the list leads with %q, want the built-in scheme", h.app.schemes[0].ID)
	}

	// Gruvbox is the row under the built-in schemes, and a scheme is in force
	// as the cursor lands on it.
	for range builtin {
		h.press("J", 0)
	}
	if h.app.ui.Scheme().ID != "gruvbox" {
		t.Fatalf("the scheme in force is %q, want gruvbox", h.app.ui.Scheme().ID)
	}
	if h.app.ui.P.Bg == before {
		t.Error("choosing a scheme did not change the page colour")
	}
	if !h.app.ui.Dark {
		t.Error("a dark scheme did not put the interface in dark mode")
	}
	if stored := state.LoadSettings().Theme; stored != "gruvbox" {
		t.Errorf("the settings file records %q, want gruvbox", stored)
	}

	// The scheme has only a dark mode, so inverting says so rather than
	// throwing the colours away.
	was := h.app.ui.P
	h.press("T", 0)
	if h.app.ui.P != was {
		t.Error("inverting a scheme with one mode changed the palette")
	}
}

// The key for asking is entered on the settings sheet, because a key nobody
// can type is a key nobody has.
func TestTheSettingsSheetTakesTheKey(t *testing.T) {
	h := newHarness(t)
	h.settle()
	h.press(",", 0)
	if !h.app.settingsOpen {
		t.Fatal("the settings sheet did not open")
	}
	// Tab walks the sheet's sections: themes, typefaces, then the key.
	h.press(key.NameTab, 0)
	h.press(key.NameTab, 0)
	if h.app.settingsList != listKey {
		t.Fatalf("section = %d, want the key section", h.app.settingsList)
	}
	if !h.app.keyField.Focused() {
		t.Fatal("the key field did not take the caret")
	}

	h.app.keyField.SetText("sk-test-key")
	h.press(key.NameReturn, 0)

	if got := h.app.settings.TypeSafeKey; got != "sk-test-key" {
		t.Errorf("stored key = %q, want the one that was typed", got)
	}
	if h.app.jev == nil {
		t.Error("asking is still off although a key was entered")
	}
	if h.app.settingsOpen == false {
		t.Error("return in the key field closed the whole sheet, not just the field")
	}
	// It survives the application, which is the point of putting it here.
	if state.LoadSettings().TypeSafeKey != "sk-test-key" {
		t.Error("the key was not written to the settings file")
	}
}

// Emptying the field is how a key is taken back out.
func TestClearingTheKeyTurnsAskingOff(t *testing.T) {
	h := newHarness(t)
	h.settle()
	h.press(",", 0)
	h.press(key.NameTab, 0)
	h.press(key.NameTab, 0)
	h.app.keyField.SetText("sk-test-key")
	h.press(key.NameReturn, 0)
	if h.app.jev == nil {
		t.Fatal("setup: the key did not take")
	}

	h.press(key.NameTab, 0)
	h.press(key.NameTab, 0)
	h.app.keyField.SetText("")
	h.press(key.NameReturn, 0)
	if h.app.jev != nil {
		t.Error("asking is still on with an empty key")
	}
	if state.LoadSettings().TypeSafeKey != "" {
		t.Error("the cleared key is still in the settings file")
	}
}

// While the caret is in the key field the letters bound to commands are just
// letters, or a key with a t or a j in it could not be typed.
func TestKeyFieldSwallowsTheCommandLetters(t *testing.T) {
	h := newHarness(t)
	h.settle()
	dark := h.app.settings.Dark
	h.press(",", 0)
	h.press(key.NameTab, 0)
	h.press(key.NameTab, 0)
	h.press("T", 0)
	if h.app.settings.Dark != dark {
		t.Error("t inverted the palette while the caret was in the key field")
	}
}
