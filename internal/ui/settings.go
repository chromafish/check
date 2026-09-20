package ui

import (
	"context"
	"fmt"
	"image"
	"os"
	"slices"
	"strings"

	"gioui.org/font"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/chromafish/check/internal/jev"
	"github.com/chromafish/check/internal/state"

	"github.com/chromafish/check/reef"
)

// Settings are the colours and the type: which scheme the interface is drawn
// in, which monospaced family it is set in, and how big the body is. All three
// are preferences of the person rather than of the repository, so they are
// kept once and applied to every window the application opens.
//
// Schemes are base16 files in state.ThemesDir, which is the form nearly every
// editor and terminal colour scheme is already published in — a person drops
// in the one they already use everywhere else.
//
// The sheet has no confirmation and no cancel. A choice is applied as the
// cursor lands on it, because the whole interface behind the sheet is the
// preview and reading it is the only way to judge a typeface.

// applySettings puts the stored preferences on the theme. It runs before the
// first frame, so the interface is never drawn in the wrong face and then
// corrected.
func (a *App) applySettings() {
	a.ui.SetFamily(a.settings.FontFamily)
	a.ui.SetSize(unit.Sp(a.settings.FontSize))
	a.schemes = reef.Builtin()
	a.ui.SetDark(a.settings.Dark)

	// The list starts as the face in the binary plus whatever was chosen last
	// time, so the sheet is usable in the moment it opens, before the scan of
	// the system's fonts has landed.
	a.families = []string{""}
	if a.settings.FontFamily != "" {
		a.families = append(a.families, a.settings.FontFamily)
		a.familySel = 1
	}

	// A scheme chosen last time has to be in force on the first frame, so it
	// is read now. Nobody who has never chosen one pays for the directory read.
	if a.settings.Theme != "" && schemeIndex(reef.Builtin(), a.settings.Theme) < 0 {
		a.askSchemes()
	}
}

// askSchemes reads the scheme directory once, off the main goroutine.
func (a *App) askSchemes() {
	if a.schemesAsked {
		return
	}
	a.schemesAsked = true
	a.background(func(context.Context) func() {
		found, err := loadSchemes()
		return func() { a.adoptSchemes(found, err) }
	})
}

// toggleSettings opens or closes the sheet, and asks what typefaces are
// installed the first time it is wanted. Nothing else on the screen needs that
// answer, so it is not paid for at startup.
func (a *App) toggleSettings() {
	a.settingsOpen = !a.settingsOpen
	if !a.settingsOpen {
		// The sheet can be closed with the caret still in the key field, and
		// a key typed but never recorded is the same as no key at all.
		a.adoptKey()
		a.settingsList = listThemes
		return
	}
	a.help, a.notesOpen = false, false
	if a.familiesAsked {
		return
	}
	a.familiesAsked = true
	// Both answers are read off the disk, so neither is paid for until the one
	// screen that needs them is asked for.
	a.background(func(context.Context) func() {
		found := reef.MonoFamilies()
		return func() { a.adoptFamilies(found) }
	})
	a.askSchemes()
}

// loadSchemes reads the schemes a person has dropped in. A missing directory
// is the ordinary case — most people never add one — and not an error.
func loadSchemes() ([]reef.Scheme, error) {
	dir, err := state.ThemesDir()
	if err != nil {
		return nil, err
	}
	found, err := reef.LoadSchemes(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	return found, err
}

// adoptSchemes takes what was on disk, with the built-in schemes at the head
// of the list, and puts the stored choice back in force now that it can be
// found.
func (a *App) adoptSchemes(found []reef.Scheme, err error) {
	a.schemes = append(reef.Builtin(), found...)
	a.schemesDone = true
	a.schemeSel = max(0, schemeIndex(a.schemes, a.settings.Theme))
	if a.schemes[a.schemeSel].ID != a.ui.Scheme().ID {
		a.ui.SetScheme(a.schemes[a.schemeSel])
	}
	// A scheme that no longer parses is worth saying out loud: the person
	// edited a file, and silence would look like the file being ignored.
	if err != nil {
		a.fail(err)
	}
}

func schemeIndex(list []reef.Scheme, id string) int {
	return slices.IndexFunc(list, func(s reef.Scheme) bool { return s.ID == id })
}

// moveScheme steps the cursor, which is the same thing as changing the colours.
func (a *App) moveScheme(delta int) {
	if len(a.schemes) == 0 {
		return
	}
	a.chooseScheme(clamp(a.schemeSel+delta, 0, len(a.schemes)-1))
}

func (a *App) chooseScheme(i int) {
	if i < 0 || i >= len(a.schemes) {
		return
	}
	a.schemeSel = i
	s := a.schemes[i]
	a.ui.SetScheme(s)
	a.settings.Theme = s.ID
	a.settings.Dark = a.ui.Dark
	a.saveSettings()

	// What is wrong with a scheme is said once, when it is chosen, rather than
	// left for the person to work out from a diff they cannot read.
	if faults := schemeFaults(s); faults != "" {
		a.note("%s — %s", s.Name, faults)
		return
	}
	a.note("drawn in %s", s.Name)
}

// schemeFaults summarises the worst of what Check found.
func schemeFaults(s reef.Scheme) string {
	worst := ""
	n := 0
	for _, p := range s.Check() {
		if p.Severity != reef.Fault {
			continue
		}
		if worst == "" {
			worst = p.String()
		}
		n++
	}
	if n > 1 {
		return fmt.Sprintf("%s, and %d more", worst, n-1)
	}
	return worst
}

// adoptFamilies takes the scan's answer, with the face in the binary at the
// head of it.
func (a *App) adoptFamilies(found []string) {
	list := make([]string, 0, len(found)+2)
	list = append(list, "")
	list = append(list, found...)
	// A family chosen before and since uninstalled is still what the interface
	// is set in, so it stays on the list rather than disappearing from the one
	// screen that could change it.
	if cur := a.ui.Family; cur != "" && familyIndex(list, cur) < 0 {
		list = append(list, cur)
	}
	a.families = list
	a.familiesDone = true
	a.familySel = max(0, familyIndex(list, a.ui.Family))
}

func familyIndex(list []string, name string) int { return slices.Index(list, name) }

// familyLabel names a choice. The empty family is the one that ships with the
// binary, which is named after the face rather than left blank.
func familyLabel(name string) string {
	if name == "" {
		return string(reef.Mono)
	}
	return name
}

// moveFamily steps the cursor, which is the same thing as changing the face.
func (a *App) moveFamily(delta int) {
	if len(a.families) == 0 {
		return
	}
	a.chooseFamily(clamp(a.familySel+delta, 0, len(a.families)-1))
}

func (a *App) chooseFamily(i int) {
	if i < 0 || i >= len(a.families) {
		return
	}
	a.familySel = i
	a.ui.SetFamily(a.families[i])
	a.settings.FontFamily = a.families[i]
	a.saveSettings()
	a.note("set in %s", familyLabel(a.families[i]))
}

// nudgeSize moves the body size a point either way, which the whole type scale
// is anchored on.
func (a *App) nudgeSize(delta int) {
	was := a.ui.Size
	a.ui.SetSize(was + unit.Sp(delta))
	if a.ui.Size == was {
		return
	}
	a.settings.FontSize = int(a.ui.Size)
	a.saveSettings()
	a.note("%d pt", int(a.ui.Size))
}

func (a *App) saveSettings() { a.fail(state.SaveSettings(a.settings)) }

// toggleDark inverts the palette and remembers which way round it was left. A
// scheme with only one mode — most base16 files — stays as it is, and says so
// rather than appearing to ignore the key.
func (a *App) toggleDark() {
	if a.ui.Scheme().Modes() < 2 {
		a.note("%s has only a %s mode", a.ui.Scheme().Name, darkName(a.ui.Dark))
		return
	}
	a.ui.ToggleDark()
	a.settings.Dark = a.ui.Dark
	a.saveSettings()
}

func darkName(dark bool) string {
	if dark {
		return "dark"
	}
	return "light"
}

// settingsKey is the whole keyboard while the sheet is up. It is a modal
// screen: the keys it does not use do nothing rather than reaching the review
// underneath, where they would move a selection nobody can see.
func (a *App) settingsKey(gtx layout.Context, name key.Name) {
	// While the key field has the caret it is an ordinary text field, and the
	// letters that are commands everywhere else are just letters.
	if a.keyField != nil && a.keyField.Focused() {
		switch name {
		case key.NameEscape, key.NameTab:
			a.settingsList = listThemes
			a.keyField.Defocus(gtx)
			a.adoptKey()
		}
		return
	}
	switch name {
	case key.NameEscape, key.NameReturn, key.NameEnter, ",":
		a.adoptKey()
		a.settingsList = listThemes
		a.settingsOpen = false
	case key.NameTab:
		a.settingsList = (a.settingsList + 1) % 3
		if a.settingsList == listKey {
			if a.keyField == nil {
				a.settingsList = listThemes
			} else {
				a.keyField.Focus(gtx)
			}
		}
	case key.NameUpArrow, "K":
		a.moveList(-1)
	case key.NameDownArrow, "J":
		a.moveList(1)
	case "T":
		a.toggleDark()
	case "-":
		a.nudgeSize(-1)
	case "=": // the unshifted spelling of +
		a.nudgeSize(1)
	}
}

// adoptKey takes what is in the field, records it and rebuilds the client, so
// that a key pasted in is usable without restarting. An empty field turns
// asking off, which is how a key is taken back out.
func (a *App) adoptKey() {
	if a.keyField == nil {
		return
	}
	key := strings.TrimSpace(a.keyField.Text())
	if key == a.settings.TypeSafeKey {
		return
	}
	a.settings.TypeSafeKey = key
	a.saveSettings()
	if a.settings.NoSemanticFind {
		a.jev = nil
		return
	}
	a.jev = jev.Resolve(key)
	switch {
	case a.jev == nil:
		a.note("key cleared, asking is off")
	case os.Getenv(jev.EnvKey) != "":
		a.note("key saved, though %s overrides it for this run", jev.EnvKey)
	default:
		a.note("key saved")
	}
}

// moveList steps whichever of the sheet's two lists has the cursor.
func (a *App) moveList(delta int) {
	switch a.settingsList {
	case listThemes:
		a.moveScheme(delta)
	case listFamilies:
		a.moveFamily(delta)
	}
}

// The sheet's two lists. Tab moves between them and j/k move inside the one
// that has the cursor; the other keeps its selection, drawn in the idle pair.
const (
	listThemes = iota
	listFamilies
	listKey
)

// familyTag and schemeTag name one row of each list for the pointer.
type familyTag struct{ i int }

type schemeTag struct{ i int }

const (
	tagFontSmaller tag = "font-smaller"
	tagFontBigger  tag = "font-bigger"
)

func (a *App) layoutSettings(gtx layout.Context) {
	if !a.settingsOpen {
		return
	}
	ui := a.ui
	size := gtx.Constraints.Max
	row := ui.TextRow(gtx, reef.SizeUI)
	code := ui.CodeRow(gtx)
	pad := gtx.Dp(reef.PadCard)
	cell := ui.Cell(gtx, reef.SizeUI, false)
	half := row / 2
	stepH := max(row, gtx.Dp(reef.ControlH))

	// The sheet is sized from its contents, and the two lists are the parts of
	// it that give way when the window is short.
	fieldH := ui.FieldHeight(gtx)
	fixed := row*8 + stepH + fieldH + code*2 + half*6 + pad*2
	themes := clamp(len(a.schemes), 1, 6)
	faces := clamp(len(a.families), 1, 10)
	for faces+themes > 2 && fixed+row*(faces+themes) > size.Y-gtx.Dp(48) {
		if faces >= themes && faces > 1 {
			faces--
		} else if themes > 1 {
			themes--
		} else {
			break
		}
	}

	w := min(size.X-gtx.Dp(80), 60*cell.X+pad*2)
	h := fixed + row*(faces+themes)
	x, y := (size.X-w)/2, max(gtx.Dp(24), (size.Y-h)/2)

	sheet := image.Rect(x, y, x+w, y+h)
	ui.Sheet(gtx, sheet)

	// One heading of the sheet, on the band that starts at ly.
	label := func(ly int, c reef.ColorNRGBA, txt string) {
		off := op.Offset(image.Pt(0, ly)).Push(gtx.Ops)
		ui.LabelAt(gtx, c, x+pad, row, x+w-pad, txt)
		off.Pop()
	}
	note := func(ly int, txt string) {
		off := op.Offset(image.Pt(0, ly)).Push(gtx.Ops)
		ui.TextRight(gtx, reef.Body(ui.P.Faint), x+w-pad, row, txt)
		off.Pop()
	}

	ly := y + pad
	label(ly, ui.P.Strong, "SETTINGS")
	ly += row + half

	// Colours first: it is the change a person notices from across the room.
	label(ly, ui.P.Faint, "THEME")
	if !a.schemesDone {
		note(ly, "looking…")
	}
	ly += row
	ly += a.drawSheetList(gtx, sheetList{
		x: x, y: ly, w: w, row: row, visible: themes,
		count: len(a.schemes), sel: a.schemeSel, first: &a.schemeFirst,
		focused: a.settingsList == listThemes,
		tag:     func(i int) event.Tag { return schemeTag{i} },
		text:    a.schemeRow,
		choose:  a.chooseScheme,
	})

	ly += half
	label(ly, ui.P.Faint, "TYPEFACE")
	if !a.familiesDone {
		note(ly, "looking…")
	}
	ly += row
	ly += a.drawSheetList(gtx, sheetList{
		x: x, y: ly, w: w, row: row, visible: faces,
		count: len(a.families), sel: a.familySel, first: &a.familyFirst,
		focused: a.settingsList == listFamilies,
		tag:     func(i int) event.Tag { return familyTag{i} },
		text: func(i int) (string, string) {
			if a.families[i] == "" {
				return familyLabel(a.families[i]), "built in"
			}
			return a.families[i], ""
		},
		choose: a.chooseFamily,
	})

	ly += half
	label(ly, ui.P.Faint, "SIZE")
	ly += row
	fit(gtx, image.Pt(0, ly), image.Pt(x+w-pad, stepH), func(gtx layout.Context) {
		minus, plus := ui.P.Action, ui.P.Action
		var smaller, bigger func()
		if ui.Size > reef.MinFontSize {
			smaller = func() { a.nudgeSize(-1) }
		} else {
			minus = ui.P.Faint
		}
		if ui.Size < reef.MaxFontSize {
			bigger = func() { a.nudgeSize(1) }
		} else {
			plus = ui.P.Faint
		}
		cx := x + pad
		cx += a.control(gtx, cx, stepH, tagFontSmaller, "−", minus, smaller) + gtx.Dp(reef.Sp5)
		// The figure keeps a fixed column so the control beyond it does not
		// move as the number changes width under the pointer.
		a.cellText(gtx, cx, stepH, x+w-pad, reef.WeightLabel, ui.P.Strong, fmt.Sprintf("%d PT", int(ui.Size)))
		cx += 6 * cell.X
		a.control(gtx, cx, stepH, tagFontBigger, "+", plus, bigger)
	})
	ly += stepH

	ly += half
	label(ly, ui.P.Faint, "ASKING")
	if a.settings.NoSemanticFind {
		note(ly, "turned off")
	} else if env := os.Getenv(jev.EnvKey); env != "" {
		note(ly, jev.EnvKey+" in force")
	} else if a.jev == nil {
		note(ly, "no key")
	} else {
		note(ly, "ready · SHIFT-F")
	}
	ly += row
	if a.keyField != nil {
		fit(gtx, image.Pt(x+pad, ly), image.Pt(w-pad*2, fieldH), func(gtx layout.Context) {
			a.keyField.Layout(gtx, ui.Theme)
		})
	}
	ly += fieldH
	label(ly, ui.P.Faint, "a TypeSafe key lets SHIFT-F ask about the diff")
	ly += row

	ly += half
	label(ly, ui.P.Faint, "SPECIMEN")
	ly += row
	// Two lines of the diff, in the colours, face and size the diff will be
	// set in: a number column, and a line with the glyphs a monospaced face is
	// usually chosen for the shape of.
	for n, line := range [2][2]string{
		{"41", "func (a *App) review(d *Diff) error {"},
		{"42", "    return d.Walk(0O1lI, `|—·`)"},
	} {
		cc := ui.Cell(gtx, reef.SizeCode, false)
		top := ly + n*code + (code-cc.Y)/2
		fit(gtx, image.Pt(x+pad, top), image.Pt(3*cc.X, cc.Y), func(gtx layout.Context) {
			ui.Text(gtx, reef.Run{Size: reef.SizeCode, Weight: font.Normal, Color: ui.P.Faint}, line[0])
		})
		fit(gtx, image.Pt(x+pad+4*cc.X, top), image.Pt(max(0, w-pad*2-4*cc.X), cc.Y), func(gtx layout.Context) {
			ui.Text(gtx, reef.Run{Size: reef.SizeCode, Weight: font.Normal, Color: ui.P.Fg}, line[1])
		})
	}
	ly += code * 2

	ly += half
	label(ly, ui.P.Faint, "TAB SECTION · J K CHOOSE · T INVERT · ESC CLOSE")
}

// schemeRow names one colour scheme and says what modes it has, so a person
// knows before choosing whether t will do anything.
func (a *App) schemeRow(i int) (string, string) {
	s := a.schemes[i]
	switch {
	case schemeIndex(reef.Builtin(), s.ID) >= 0:
		return s.Name, "built in · light + dark"
	case s.Modes() > 1:
		return s.Name, "light + dark"
	case s.Has(true):
		return s.Name, "dark"
	default:
		return s.Name, "light"
	}
}

// sheetList is one of the sheet's two lists of choices.
type sheetList struct {
	x, y, w, row int
	visible      int
	count        int
	sel          int
	first        *int
	focused      bool
	tag          func(int) event.Tag
	text         func(int) (name, note string)
	choose       func(int)
}

// drawSheetList draws the rows in view and returns the height it used. The
// window on to the list follows the cursor, which is the only way through it.
func (a *App) drawSheetList(gtx layout.Context, l sheetList) int {
	ui := a.ui
	pad := gtx.Dp(reef.PadCard)
	cell := ui.Cell(gtx, reef.SizeUI, false)

	*l.first = clamp(*l.first, max(0, l.sel-l.visible+1), l.sel)
	*l.first = clamp(*l.first, 0, max(0, l.count-l.visible))

	ly := l.y
	for n := range l.visible {
		i := *l.first + n
		if i >= l.count {
			break
		}
		box := image.Rect(l.x+gtx.Dp(reef.Sp3), ly, l.x+l.w-gtx.Dp(reef.Sp3), ly+l.row)
		tag := l.tag(i)
		switch {
		case i == l.sel:
			// The list without the cursor keeps its selection in the idle
			// pair, so both say what is chosen and only one says where the
			// keyboard is.
			ui.SelectionRect(gtx, box, l.focused)
		case a.hovered(tag):
			reef.FillRect(gtx, box, ui.P.Hover)
		}

		name, right := l.text(i)
		weight, ink, limit := font.Normal, ui.P.Fg, l.x+l.w-pad
		if i == l.sel {
			weight, ink = reef.WeightLabel, ui.P.Strong
		}
		fit(gtx, image.Pt(0, ly), image.Pt(l.x+l.w-pad, l.row), func(gtx layout.Context) {
			if right != "" {
				limit -= (len(right) + 2) * cell.X
				ui.TextRight(gtx, reef.Body(ui.P.Faint), l.x+l.w-pad, l.row, right)
			}
			a.cellText(gtx, l.x+pad, l.row, limit, weight, ink, name)
		})
		fill(gtx, box.Min, box.Size(), func(gtx layout.Context) {
			a.clickable(gtx, box.Size(), tag, func() { l.choose(i) })
		})
		ly += l.row
	}
	return l.row * l.visible
}
