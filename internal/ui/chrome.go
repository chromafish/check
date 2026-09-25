package ui

import (
	"fmt"
	"image"
	"io"
	"strings"
	"time"

	"gioui.org/f32"
	"gioui.org/font"
	"gioui.org/io/clipboard"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/system"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"

	"github.com/chromafish/check/reef"
)

// layoutHeader draws the top strip: what is being reviewed on the left, the
// revset that selects it on the right.
func (a *App) layoutHeader(gtx layout.Context, width, height int) {
	ui := a.ui
	ui.Masthead(gtx, image.Pt(width, height))
	pad := gtx.Dp(reef.Sp6)
	cell := ui.Cell(gtx, reef.SizeUI, false)
	baseline := (height - cell.Y) / 2

	// The masthead opens with the application mark at the same visual scale as
	// the repository control beside it.
	x := pad
	{
		sub := gtx
		size := min(gtx.Dp(reef.GapRow), height-gtx.Dp(reef.Sp3)*2)
		sub.Constraints = layout.Exact(image.Pt(size, size))
		macro := op.Record(gtx.Ops)
		d := layoutLogo(sub, ui.P.Action, ui.P.Accent)
		call := macro.Stop()

		off := op.Offset(image.Pt(x, (height-d.Size.Y)/2)).Push(gtx.Ops)
		call.Add(gtx.Ops)
		off.Pop()
		x += d.Size.X + gtx.Dp(reef.Sp6)
	}
	name, nameColor := a.repoName, ui.P.Fg
	if name == "" {
		name, nameColor = "choose a repository", ui.P.Action
	}

	// The name, the caret that says it can be acted on and the printed
	// shortcut are one control, so the hit area is the whole run and it tints
	// under the pointer the way a row does. The run is drawn into a macro
	// first because the tint has to go down before the text.
	macro := op.Record(gtx.Ops)
	runEnd := x
	runEnd += a.headerText(gtx, runEnd, baseline, width, reef.WeightLabel, nameColor, name)
	runEnd += a.headerText(gtx, runEnd+gtx.Dp(reef.Sp2), baseline, width, font.Normal, ui.P.Muted, "▾") + gtx.Dp(reef.PadInline)
	runEnd += a.headerText(gtx, runEnd, baseline, width, font.Normal, ui.P.Faint, "⌘O")
	run := macro.Stop()

	ch := gtx.Dp(reef.ControlH)
	hit := image.Rect(x-gtx.Dp(reef.Sp3), (height-ch)/2, runEnd+gtx.Dp(reef.Sp3), (height-ch)/2+ch)
	if a.hovered(&a.openButton) {
		reef.FillRect(gtx, hit, ui.P.Hover)
	}
	run.Add(gtx.Ops)
	a.hover(gtx, hit, &a.openButton, a.chooseFolder)
	x = hit.Max.X + gtx.Dp(reef.Sp5)

	// The revset field takes the right third of the strip, labelled in place
	// of a border.
	fieldW := min(max(width/3, gtx.Dp(220)), width-x-gtx.Dp(80))
	if a.repo != nil && fieldW > gtx.Dp(120) {
		fx := width - fieldW - gtx.Dp(reef.Sp4)

		// The label is measured into the gap left before the field and set
		// against it, so that at a larger body size it gives way instead of
		// printing over what it labels.
		macro := op.Record(gtx.Ops)
		sub := gtx
		sub.Constraints.Min = image.Point{}
		sub.Constraints.Max.X = max(0, fx-x-gtx.Dp(reef.Sp5))
		d := ui.Label(sub, ui.P.Muted, a.backend.QueryLabel)
		label := macro.Stop()
		off := op.Offset(image.Pt(fx-gtx.Dp(reef.Sp5)-d.Size.X, (height-d.Size.Y)/2)).Push(gtx.Ops)
		label.Add(gtx.Ops)
		off.Pop()

		// The well follows the type: a field that cannot hold its own line of
		// text hides what was typed into it.
		fh := max(gtx.Dp(reef.ControlHSm), ui.Cell(gtx, reef.SizeUI, false).Y+gtx.Dp(reef.Sp2))
		fill(gtx, image.Pt(fx, (height-fh)/2), image.Pt(fieldW, fh), func(gtx layout.Context) {
			a.revsetInput.Layout(gtx, ui.Theme)
		})
	}
}

// layoutLogo draws the mark in final-size vectors. Keeping the two planes as
// geometry avoids the blur that comes from shrinking the large brand PNG into
// the masthead's small pixel grid.
func layoutLogo(gtx layout.Context, upper, lower reef.ColorNRGBA) layout.Dimensions {
	size := min(gtx.Constraints.Max.X, gtx.Constraints.Max.Y)
	scale := float32(size) / 28
	drawLogoPlane(gtx, scale, upper, []f32.Point{
		{X: 3, Y: 15}, {X: 4, Y: 11}, {X: 10, Y: 3}, {X: 14, Y: 2},
		{X: 18, Y: 4}, {X: 24, Y: 10}, {X: 23, Y: 12}, {X: 6, Y: 22},
		{X: 3, Y: 19},
	})
	drawLogoPlane(gtx, scale, lower, []f32.Point{
		{X: 7, Y: 23}, {X: 24, Y: 13}, {X: 26, Y: 16}, {X: 25, Y: 20},
		{X: 19, Y: 25}, {X: 14, Y: 27}, {X: 10, Y: 26},
	})
	return layout.Dimensions{Size: image.Pt(size, size)}
}

func drawLogoPlane(gtx layout.Context, scale float32, c reef.ColorNRGBA, points []f32.Point) {
	var path clip.Path
	path.Begin(gtx.Ops)
	path.MoveTo(points[0].Mul(scale))
	for _, point := range points[1:] {
		path.LineTo(point.Mul(scale))
	}
	path.Close()
	paint.FillShape(gtx.Ops, c, clip.Outline{Path: path.End()}.Op())
}

// headerText draws one run of the masthead at an explicit baseline, so the
// name, the caret and the shortcut sit on one line however tall the strip is.
func (a *App) headerText(gtx layout.Context, x, y, width int, weight font.Weight, c reef.ColorNRGBA, txt string) int {
	cell := a.ui.Cell(gtx, reef.SizeUI, false)
	off := op.Offset(image.Pt(0, y)).Push(gtx.Ops)
	w := a.ui.TextAt(gtx, reef.Run{Size: reef.SizeUI, Weight: weight, Color: c}, x, cell.Y, width, txt)
	off.Pop()
	return w
}

// layoutStatus draws the bottom strip: the current message or error on the
// left, standing facts about the session on the right.
func (a *App) layoutStatus(gtx layout.Context) {
	ui := a.ui
	size := gtx.Constraints.Max
	ui.StatusBar(gtx, size)

	pad := gtx.Dp(reef.PadInline)
	lh := ui.Cell(gtx, reef.SizeLabel, true).Y
	y := (size.Y - lh) / 2

	a.expireStatus(gtx)

	// The right of the strip is measured first — the controls and the standing
	// figures own their space — and the keys legend takes what is left. Two
	// runs of text never overprint.
	rightX := size.X - pad
	rightX -= a.controlRight(gtx, rightX, size.Y, tagHelp, "KEYS  ?", ui.P.Muted, func() {
		a.help = !a.help
	}) + gtx.Dp(reef.Sp3)
	rightX -= a.controlRight(gtx, rightX, size.Y, tagSettings, "TYPE  ⌘,", ui.P.Muted,
		a.toggleSettings) + gtx.Dp(reef.Sp3)
	if a.repo != nil {
		c := ui.P.Muted
		if a.sondaOpen() {
			c = ui.P.Action
		}
		rightX -= a.controlRight(gtx, rightX, size.Y, tagSonda, "SONDA  S", c, a.toggleSonda) + gtx.Dp(reef.Sp3)
	}

	if n := a.openCount(); n > 0 {
		label := fmt.Sprintf("COPY %d NOTES FOR AGENT", n)
		if n == 1 {
			label = "COPY 1 NOTE FOR AGENT"
		}
		rightX -= a.controlRight(gtx, rightX, size.Y, tagCopy, label, ui.P.Ok, a.copyNotes) + gtx.Dp(reef.Sp3)
		rightX -= a.controlRight(gtx, rightX, size.Y, tagNotes, "LIST  ⇧N", ui.P.Muted, func() {
			a.notesOpen = !a.notesOpen
		}) + gtx.Dp(reef.Sp3)
	}

	if right := a.progress(); a.busy > 0 || right != "" {
		if a.busy > 0 && right == "" {
			right = "WORKING"
		}
		sub := gtx
		sub.Constraints.Min = image.Point{}
		sub.Constraints.Max.X = size.X
		macro := op.Record(gtx.Ops)
		d := ui.Label(sub, ui.P.Faint, strings.ToUpper(right))
		call := macro.Stop()
		if x := rightX - d.Size.X; x > pad {
			off := op.Offset(image.Pt(x, y)).Push(gtx.Ops)
			call.Add(gtx.Ops)
			off.Pop()
			rightX = x - gtx.Dp(reef.Sp5)
		}
	}

	left, color := a.statusLeft(), ui.P.Muted
	if a.failure != "" {
		left, color = "ERROR "+firstLine(a.failure), ui.P.Error
	}
	if rightX-pad > 0 {
		fit(gtx, image.Pt(pad, y), image.Pt(rightX-pad, gtx.Constraints.Max.Y), func(gtx layout.Context) {
			ui.Label(gtx, color, strings.ToUpper(left))
		})
	}
}

// progress summarises how far through the change the review has got, the one
// standing fact worth a permanent place on screen.
func (a *App) progress() string {
	if a.repo == nil || len(a.files) == 0 {
		return a.withVersion("")
	}
	added, removed, notes := 0, 0, 0
	for _, f := range a.files {
		added += f.Added
		removed += f.Removed
		notes += f.Open
	}
	viewed, total := a.readCount()
	out := fmt.Sprintf("%d/%d read · +%d −%d", viewed, total, added, removed)
	if notes > 0 {
		out += fmt.Sprintf(" · %d open notes", notes)
	}
	return a.withVersion(out)
}

// withVersion appends the backend's version to the standing facts. It sits down here
// rather than in the masthead: it is a fact about the session, not part of
// what is being reviewed, and the top of the screen is quieter without it.
func (a *App) withVersion(out string) string {
	if a.backend.Version == "" {
		return out
	}
	name := a.backend.Name + " " + a.backend.Version
	if out == "" {
		return name
	}
	return out + " · " + name
}

// statusLife is how long a message stays up before the status bar goes back to
// showing what the keys do.
const statusLife = 5 * time.Second

// expireStatus drops a message once it has been up long enough, and asks for
// the frame that will do it.
func (a *App) expireStatus(gtx layout.Context) {
	if a.status == "" && a.failure == "" {
		return
	}
	if gtx.Now.Sub(a.statusAt) >= statusLife {
		a.status = ""
		a.failure = ""
		return
	}
	gtx.Execute(op.InvalidateCmd{At: a.statusAt.Add(statusLife)})
}

func (a *App) statusLeft() string {
	if a.status != "" {
		return a.status
	}
	if a.repo == nil {
		if a.cloning != nil {
			return "esc stop"
		}
		return "⌘o choose a folder · tab paste a link · j/k recent · enter open"
	}
	if a.sondaOpen() {
		return "r run · x stop · j/k move · tab pane · / filter · l level · w raw · esc back"
	}
	switch {
	case a.focus == PaneRevs && !a.classic():
		return "j/k branch or commit · enter jev · a ask · b classic · r refresh · ? keys"
	case a.focus == PaneFiles && !a.classic():
		return "j/k lead · enter read it · a ask · b classic · ? keys"
	}
	switch a.focus {
	case PaneRevs:
		return fmt.Sprintf("j/k move · enter files · / %s · r refresh · ? keys",
			strings.ToLower(a.backend.QueryLabel))
	case PaneFiles:
		return "j/k move · f filter · u unread · enter diff · v viewed · ? keys"
	default:
		return "j/k line · f find · u unread · o open · c comment · [ ] file · ? keys"
	}
}

// handleKeys reads the keyboard. Single letters are only treated as commands
// when no text field has focus, so typing a revset never triggers one.
func (a *App) handleKeys(gtx layout.Context) {
	// Both fields are drained every frame so their focus state is current
	// before it is used to decide what a keystroke means.
	text, submitted := a.revsetInput.Update(gtx)
	if submitted {
		a.revset = strings.TrimSpace(text)
		a.revsetInput.Defocus(gtx)
		a.reload(false)
		a.note("revset %s", a.revset)
	}
	editing := a.revsetInput.Focused()
	if a.draft != nil {
		a.draft.field.Update(gtx)
		editing = editing || a.draft.field.Focused()
	}
	if a.sonda != nil {
		a.sonda.filter.Update(gtx)
		editing = editing || a.sonda.filter.Focused()
	}
	// A field that submits swallows Return, so the settings sheet's fields
	// are taken here rather than from the sheet's own keys.
	for _, f := range []*reef.Field{a.keyField, a.cloneField} {
		if f == nil {
			continue
		}
		if _, submitted := f.Update(gtx); submitted {
			f.Defocus(gtx)
			a.settingsList = listThemes
			a.adoptFields()
		}
		editing = editing || f.Focused()
	}
	if a.urlField != nil {
		if text, submitted := a.urlField.Update(gtx); submitted {
			a.cloneLink(text)
		}
		editing = editing || a.urlField.Focused()
	}
	if a.askField != nil {
		if text, submitted := a.askField.Update(gtx); submitted {
			if q := strings.TrimSpace(text); q != "" {
				a.askField.Defocus(gtx)
				a.focus = PaneFiles
				a.askJev(q)
			}
		}
		editing = editing || a.askField.Focused()
	}
	findEditing := a.updateFind(gtx)
	fileEditing := a.updateFileFilter(gtx)
	editing = editing || findEditing || fileEditing

	filters := []event.Filter{
		key.Filter{Name: key.NameEscape},
		key.Filter{Name: "R", Required: key.ModShortcut},
		key.Filter{Name: "O", Required: key.ModShortcut},
		key.Filter{Name: "C", Required: key.ModShortcut | key.ModShift},
		key.Filter{Name: "W", Required: key.ModShortcut},
		key.Filter{Name: ",", Required: key.ModShortcut},
		key.Filter{Name: key.NameReturn, Optional: key.ModShortcut | key.ModShift},
		key.Filter{Name: key.NameEnter, Optional: key.ModShortcut | key.ModShift},
	}
	if !editing {
		filters = append(filters,
			// Copying the selection is only the application's business when no
			// field has focus; inside one, copy belongs to the editor.
			key.Filter{Name: "C", Required: key.ModShortcut},
			key.Filter{Name: key.NameTab, Optional: key.ModShift},
			key.Filter{Name: key.NameUpArrow, Optional: key.ModShift},
			key.Filter{Name: key.NameDownArrow, Optional: key.ModShift},
			key.Filter{Name: key.NameLeftArrow},
			key.Filter{Name: key.NameRightArrow},
			key.Filter{Name: key.NameSpace, Optional: key.ModShift},
			key.Filter{Name: key.NamePageUp},
			key.Filter{Name: key.NamePageDown},
			key.Filter{Name: key.NameHome},
			key.Filter{Name: key.NameEnd},
		)
		for _, n := range commandKeys {
			filters = append(filters, key.Filter{Name: n, Optional: key.ModShift})
		}
	}

	for {
		ev, ok := gtx.Event(filters...)
		if !ok {
			break
		}
		ke, ok := ev.(key.Event)
		if !ok || ke.State != key.Press {
			continue
		}
		a.command(gtx, ke, editing)
	}
}

// commandKeys are the letters and symbols bound to commands. They are declared
// as a list so the filters and the help sheet cannot drift apart.
var commandKeys = []key.Name{
	"J", "K", "H", "L", "G", "V", "C", "D", "R", "T", "N", "P", "Y",
	"Z", "E", "S", "X", "W", "1", "2", "F", "U", "O", "A", "B",
	// The settings sheet's own keys. Outside it they are bound to nothing, and
	// a key bound to nothing is not a key another pane gets to reinterpret.
	",", "-", "=",
	"/", "[", "]", "\\",
}

func (a *App) command(gtx layout.Context, ke key.Event, editing bool) {
	shift := ke.Modifiers.Contain(key.ModShift)
	cmd := ke.Modifiers.Contain(key.ModShortcut)

	if cmd && ke.Name == "O" {
		a.chooseFolder()
		return
	}
	if cmd && shift && ke.Name == "C" {
		a.copyNotes()
		return
	}
	if cmd && ke.Name == "C" && !editing {
		a.copySelection()
		return
	}
	if cmd && ke.Name == "W" && a.win != nil {
		a.win.Perform(system.ActionClose)
		return
	}
	if cmd && ke.Name == "," {
		a.toggleSettings()
		return
	}
	if cmd && ke.Name == "F" {
		a.startFind(gtx)
		return
	}

	// The settings sheet is modal: while it is up it is the whole keyboard,
	// so a keystroke meant for it never also moves something underneath it.
	if a.settingsOpen {
		a.settingsKey(gtx, ke.Name)
		return
	}
	// So is the sonda screen: the review is not on screen while it is up.
	if a.sondaOpen() {
		a.sondaKey(gtx, ke, editing)
		return
	}

	// With no repository open, the only navigation is through the recent list.
	if a.repo == nil {
		if a.urlField != nil && a.urlField.Focused() {
			if ke.Name == key.NameEscape && !a.stopClone() {
				a.urlField.Defocus(gtx)
				gtx.Execute(key.FocusCmd{Tag: nil})
			}
			return
		}
		switch ke.Name {
		case key.NameTab:
			a.urlField.Focus(gtx)
		case key.NameUpArrow, "K":
			a.moveRecent(-1)
		case key.NameDownArrow, "J":
			a.moveRecent(1)
		case key.NameReturn, key.NameEnter:
			a.openSelectedRecent()
		case key.NameEscape:
			if a.stopClone() {
				return
			}
			a.openErr = ""
			a.failure = ""
		}
		return
	}

	if editing {
		switch ke.Name {
		case key.NameEscape:
			if a.askField != nil && a.askField.Focused() {
				a.askField.Defocus(gtx)
				gtx.Execute(key.FocusCmd{Tag: nil})
				return
			}
			if a.findField != nil && a.findField.Focused() {
				a.closeFind(gtx)
				return
			}
			if a.fileFilter != nil && a.fileFilter.Focused() {
				a.clearFileFilter(gtx)
				return
			}
			a.revsetInput.Defocus(gtx)
			a.cancelDraft(gtx)
		case key.NameReturn, key.NameEnter:
			if cmd {
				a.saveDraft(gtx)
			}
			// Find uses submit to jump; handled in updateFind.
			// File filter keeps text on Enter.
			if a.fileFilter != nil && a.fileFilter.Focused() {
				a.fileFilter.Defocus(gtx)
				gtx.Execute(key.FocusCmd{Tag: nil})
			}
		}
		return
	}

	switch ke.Name {
	case key.NameEscape:
		if a.fileFilter != nil && a.fileFilter.Text() != "" {
			a.clearFileFilter(gtx)
			return
		}
		if a.findField != nil && a.findField.Text() != "" {
			a.closeFind(gtx)
			return
		}
		a.help = false
		a.notesOpen = false
		a.status = ""
		a.failure = ""
		a.clearSelection()
	case key.NameTab:
		if shift {
			a.moveFocus(-1)
		} else {
			a.moveFocus(1)
		}
	case key.NameLeftArrow, "H":
		a.moveFocus(-1)
	case key.NameRightArrow, "L":
		a.moveFocus(1)
	case key.NameUpArrow, "K":
		if shift && a.focus == PaneDiff {
			a.extendSelection(-1)
			return
		}
		a.move(-1)
	case key.NameDownArrow, "J":
		if shift && a.focus == PaneDiff {
			a.extendSelection(1)
			return
		}
		a.move(1)
	case key.NameHome:
		a.moveTo(0)
	case key.NameEnd:
		a.moveTo(1 << 30)
	case key.NameSpace, key.NamePageDown, key.NamePageUp:
		step := 1
		if ke.Name == key.NamePageUp || (ke.Name == key.NameSpace && shift) {
			step = -1
		}
		a.page(gtx, step)
	case key.NameReturn, key.NameEnter:
		a.enter(gtx)
	case "G":
		if shift {
			a.moveTo(1 << 30)
		} else {
			a.moveTo(0)
		}
	case "E":
		if shift {
			a.expandWholeFile()
		} else {
			a.expandHere()
		}
	case "1":
		a.togglePane(PaneRevs)
	case "2":
		a.togglePane(PaneFiles)
	case "Z":
		a.focusCode()
	case "R":
		a.reload(true)
		a.note("refreshed")
	case "S":
		a.openSonda()
	case "/":
		// Shifted punctuation arrives as the unshifted key with a modifier, so
		// "?" is spelled this way rather than as its own binding.
		if shift {
			a.help = !a.help
		} else {
			a.revsetInput.Focus(gtx)
		}
	case ",":
		a.toggleSettings()
	case "T":
		a.toggleDark()
	case "V":
		if shift {
			a.markAllViewed()
		} else {
			a.toggleViewed()
		}
	case "C":
		a.startComment()
	case "D":
		a.deleteCommentUnderCursor()
	case "N":
		if shift {
			a.notesOpen = !a.notesOpen
		} else {
			a.jumpComment(1)
		}
	case "P":
		if a.diff != nil && a.diff.Cursor >= 0 && a.diff.Cursor < len(a.diff.Rows) {
			if fd := a.diff.FileAt(a.diff.Cursor); fd != nil && fd.IsDoc {
				if idx := a.diff.FileOf(a.diff.Cursor); idx >= 0 {
					a.togglePretty(idx)
					break
				}
			}
		}
		a.jumpComment(-1)
	case "Y":
		a.copyPath(gtx)
	case "[":
		if shift {
			a.stepHunk(-1)
		} else {
			a.stepFile(-1)
		}
	case "]":
		if shift {
			a.stepHunk(1)
		} else {
			a.stepFile(1)
		}
	case "\\":
		a.toggleSplit()
	case "W":
		a.toggleWrap()
	case "A":
		if a.classic() {
			a.startAsk(gtx)
		} else {
			a.focusAsk(gtx)
		}
	case "B":
		a.toggleClassic()
	case "F":
		switch {
		case shift && a.classic():
			a.startAsk(gtx)
		case shift, !a.classic() && a.focus == PaneFiles:
			a.focusAsk(gtx)
		case a.focus == PaneFiles:
			a.startFileFilter(gtx)
		default:
			a.startFind(gtx)
		}
	case "U":
		if shift {
			a.jumpUnread(-1)
		} else {
			a.jumpUnread(1)
		}
	case "O":
		if shift {
			a.jumpOpen(-1)
		} else {
			a.jumpOpen(1)
		}
	}
}

func (a *App) moveFocus(delta int) {
	// A column that has been put away is not a place focus can land.
	for range int(numPanes) {
		a.focus = Pane((int(a.focus) + delta + int(numPanes)) % int(numPanes))
		if !a.splits.Hidden(int(a.focus)) {
			return
		}
	}
}

// enter moves rightwards through the panes, which is the natural reading
// order: pick a revision, pick a file, read the diff.
func (a *App) enter(gtx layout.Context) {
	switch a.focus {
	case PaneRevs:
		a.moveFocus(1)
	case PaneFiles:
		if !a.classic() {
			a.enterBrief()
			return
		}
		a.focus = PaneDiff
	case PaneDiff:
		a.toggleResolvedUnderCursor()
	}
}

func (a *App) copyPath(gtx layout.Context) {
	if a.fileSel < len(a.files) {
		path := a.files[a.fileSel].Path
		gtx.Execute(clipboard.WriteCmd{Type: "application/text", Data: io.NopCloser(strings.NewReader(path))})
		a.note("copied %s", path)
	}
}

// helpSheet lists every binding, drawn as a bordered card over the interface.
var helpSheet = [][2]string{
	{"B", "brief view (jev) / classic view"},
	{"A", "ask jev about the change (⏎ asks)"},
	{"BRIEF: J K / ENTER", "walk jev's leads / read one in the diff"},
	{"TAB / SHIFT-TAB", "move between panes"},
	{"H L ← →", "focus pane left / right"},
	{"J K ↑ ↓", "move selection"},
	{"ENTER", "step rightwards, resolve comment in diff"},
	{"G / SHIFT-G", "first / last"},
	{"SPACE / SHIFT-SPACE", "page down / up"},
	{"[ ]", "previous / next file"},
	{"SHIFT-[ SHIFT-]", "previous / next hunk"},
	{"/", "edit the revision query"},
	{"F / CMD-F", "find in diff (⏎ next, ESC close)"},
	{"FILE: F", "filter manifest (ESC clear)"},
	{"R / CMD-R", "reload from the repository"},
	{"V / SHIFT-V", "mark file viewed / all files viewed"},
	{"U / SHIFT-U", "next / previous unread file"},
	{"O / SHIFT-O", "next / previous open note"},
	{"C", "comment on the current line"},
	{"CMD-ENTER", "save comment"},
	{"D", "delete comment under the cursor"},
	{"N / P", "next / previous note"},
	{"SHIFT-N", "list every note on the change"},
	{"CMD-SHIFT-C", "copy the notes, with code, for an agent"},
	{"SHIFT-N → CLEAR ALL", "throw away every note on the change"},
	{"Y", "copy path"},
	{"\\", "side by side"},
	{"W", "wrap long lines"},
	{"DRAG / SHIFT-J K", "select code"},
	{"CMD-C", "copy the selected code"},
	{"E", "expand the unchanged lines the diff left out"},
	{"SHIFT-E", "whole file, and back to the hunks"},
	{"1 / 2", "hide or show the revisions / manifest column"},
	{"Z", "code only: both trees away, and back"},
	{"S", "sonda: run the change beside its baseline, and back"},
	{"SONDA: R / X", "run both / stop both"},
	{"SONDA: / L W", "filter by text / by level / raw lines"},
	{"T", "invert palette"},
	{", / CMD-,", "settings: typeface and size"},
	{"SHIFT-/", "this sheet"},
	{"CMD-O", "open another repository"},
}

// helpRows is the sheet as it stands for this run. Asking a question is only
// listed when there is a key for it: a binding that would answer "you cannot"
// is not one worth reading past every time the sheet is opened.
func (a *App) helpRows() [][2]string {
	if a.jev == nil {
		return helpSheet
	}
	rows := make([][2]string, 0, len(helpSheet)+1)
	for _, r := range helpSheet {
		rows = append(rows, r)
		if r[0] == "F / CMD-F" {
			rows = append(rows, [2]string{"SHIFT-F", "ask about the change (⏎ asks)"})
		}
	}
	return rows
}

func (a *App) layoutHelp(gtx layout.Context) {
	if !a.help {
		return
	}
	ui := a.ui
	size := gtx.Constraints.Max
	cell := ui.Cell(gtx, reef.SizeUI, false)
	row := ui.TextRow(gtx, reef.SizeUI)
	pad := gtx.Dp(reef.PadCard)

	rows := a.helpRows()
	keyW := 0
	for _, h := range rows {
		keyW = max(keyW, len(h[0]))
	}
	w := min(size.X-gtx.Dp(80), (keyW+46)*cell.X+pad*2)
	h := min(size.Y-gtx.Dp(80), row*(len(rows)+4)+pad*2)
	x, y := (size.X-w)/2, (size.Y-h)/2

	// A sheet that floats is drawn on the raised surface behind an ink hairline.
	sheet := image.Rect(x, y, x+w, y+h)
	ui.Sheet(gtx, sheet)

	fit(gtx, image.Pt(x+pad, y+pad), image.Pt(w-pad*2, row), func(gtx layout.Context) {
		ui.Label(gtx, ui.P.Strong, "KEYS")
	})

	for i, entry := range rows {
		ly := y + pad + row*(i+2)
		if ly+row > y+h-pad {
			break
		}
		fit(gtx, image.Pt(x+pad, ly), image.Pt(w-pad*2, row), func(gtx layout.Context) {
			ui.Text(gtx, reef.Run{Size: reef.SizeUI, Weight: reef.WeightLabel, Style: font.Regular, Color: ui.P.Strong}, entry[0])
		})
		fit(gtx, image.Pt(x+pad+(keyW+2)*cell.X, ly), image.Pt(w-pad*2-(keyW+2)*cell.X, row), func(gtx layout.Context) {
			ui.Text(gtx, reef.Run{Size: reef.SizeUI, Weight: font.Normal, Style: font.Regular, Color: ui.P.Muted}, entry[1])
		})
	}

	// The colophon: the two Plex faces, shipped with the binary under the SIL
	// Open Font License.
	// The face the interface is set in, and — because the wordmark is always
	// Plex whatever the setting — the licence the embedded one carries.
	colophon := "SET IN " + strings.ToUpper(ui.FontName) + " · SIL OPEN FONT LICENSE 1.1"
	if ui.Family != "" {
		colophon = "SET IN " + strings.ToUpper(ui.Family) + " · IBM PLEX UNDER THE SIL OPEN FONT LICENSE 1.1"
	}
	fit(gtx, image.Pt(x+pad, y+h-pad-row), image.Pt(w-pad*2, row), func(gtx layout.Context) {
		ui.Label(gtx, ui.P.Faint, colophon)
	})
}
