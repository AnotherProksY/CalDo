package ui

import (
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"caldo/internal/ui/theme"
)

// dialog is a modal overlay. Update returns done=true when it should close.
type dialog interface {
	Update(tea.KeyPressMsg) (cmd tea.Cmd, done bool)
	View(th *theme.Theme, maxW, maxH int) string
}

// anchoredDialog is drawn at a screen position (a context menu at the
// pointer) instead of centered.
type anchoredDialog interface {
	Anchor() (x, y int, ok bool)
}

// clickDialog handles a left click on row y of its own box (0 = top border).
type clickDialog interface {
	Click(y int) (cmd tea.Cmd, done bool)
}

// msgDialog is a dialog that also wants non-key messages: pastes
// (tea.PasteMsg, or the clipboard read behind ctrl+v) and cursor blinks.
type msgDialog interface {
	HandleMsg(tea.Msg) tea.Cmd
}

func frame(th *theme.Theme, title string, lines []string, w int, maxH int) string {
	h := min(len(lines)+2, maxH)
	return th.Box(title, lines, w, h, true)
}

func dialogWidth(maxW, want int) int { return max(20, min(maxW-4, want)) }

// --- confirm -------------------------------------------------------------

type confirmDialog struct {
	msg   string
	onYes func() tea.Cmd
}

func (d *confirmDialog) Update(k tea.KeyPressMsg) (tea.Cmd, bool) {
	switch k.String() {
	case "y", "Y", "enter":
		return d.onYes(), true
	case "n", "N", "esc", "q":
		return nil, true
	}
	return nil, false
}

func (d *confirmDialog) View(th *theme.Theme, maxW, maxH int) string {
	w := dialogWidth(maxW, max(40, theme.StringWidth(d.msg)+6))
	return frame(th, "Confirm", []string{d.msg, "", th.StatusKey.Render("y") + " yes   " + th.StatusKey.Render("n") + " no"}, w, maxH)
}

// --- input ---------------------------------------------------------------

type inputDialog struct {
	title    string
	hint     string
	input    textinput.Model
	onSubmit func(string) tea.Cmd
}

func newInput(title, hint, value string, onSubmit func(string) tea.Cmd) *inputDialog {
	ti := textinput.New()
	ti.Prompt = "› "
	ti.SetValue(value)
	ti.CursorEnd()
	ti.Focus()
	return &inputDialog{title: title, hint: hint, input: ti, onSubmit: onSubmit}
}

func (d *inputDialog) Update(k tea.KeyPressMsg) (tea.Cmd, bool) {
	switch k.String() {
	case "enter":
		return d.onSubmit(strings.TrimSpace(d.input.Value())), true
	case "esc":
		return nil, true
	}
	var cmd tea.Cmd
	d.input, cmd = d.input.Update(k)
	return cmd, false
}

func (d *inputDialog) HandleMsg(msg tea.Msg) tea.Cmd {
	var cmd tea.Cmd
	d.input, cmd = d.input.Update(msg)
	return cmd
}

func (d *inputDialog) View(th *theme.Theme, maxW, maxH int) string {
	w := dialogWidth(maxW, 70)
	d.input.SetWidth(w - 8)
	lines := []string{d.input.View()}
	if d.hint != "" {
		lines = append(lines, "", th.Faint.Render(d.hint))
	}
	return frame(th, d.title, lines, w, maxH)
}

// --- choice --------------------------------------------------------------

type choiceDialog struct {
	title   string
	options []string
	sel     int
	onPick  func(int) tea.Cmd
	// menu: a compact context menu anchored at (x, y).
	menu bool
	x, y int
}

func (d *choiceDialog) Anchor() (int, int, bool) { return d.x, d.y, d.menu }

func (d *choiceDialog) Click(y int) (tea.Cmd, bool) {
	if i := y - 1; i >= 0 && i < len(d.options) {
		return d.onPick(i), true
	}
	return nil, false
}

func (d *choiceDialog) Update(k tea.KeyPressMsg) (tea.Cmd, bool) {
	switch k.String() {
	case "j", "down", "tab":
		d.sel = (d.sel + 1) % len(d.options)
	case "k", "up", "shift+tab":
		d.sel = (d.sel - 1 + len(d.options)) % len(d.options)
	case "enter", "space":
		return d.onPick(d.sel), true
	case "esc", "q":
		return nil, true
	default:
		// Number keys pick directly.
		if s := k.String(); len(s) == 1 && s[0] >= '1' && int(s[0]-'1') < len(d.options) {
			return d.onPick(int(s[0] - '1')), true
		}
	}
	return nil, false
}

func (d *choiceDialog) View(th *theme.Theme, maxW, maxH int) string {
	w := dialogWidth(maxW, 50)
	if d.menu {
		w = theme.StringWidth(d.title) + 6
		for _, o := range d.options {
			w = max(w, theme.StringWidth(o)+6)
		}
		w = min(w, maxW)
	}
	var lines []string
	for i, o := range d.options {
		l := "  " + o
		if i == d.sel {
			l = th.Selected.Render(theme.Fit("› "+o, w-4))
		}
		lines = append(lines, l)
	}
	return frame(th, d.title, lines, w, maxH)
}

// --- text (detail / help) ------------------------------------------------

type textDialog struct {
	title  string
	lines  []string
	scroll int
	width  int
}

func (d *textDialog) Update(k tea.KeyPressMsg) (tea.Cmd, bool) {
	switch k.String() {
	case "j", "down":
		d.scroll++
	case "k", "up":
		d.scroll = max(0, d.scroll-1)
	case "esc", "q", "enter", "?":
		return nil, true
	}
	return nil, false
}

func (d *textDialog) View(th *theme.Theme, maxW, maxH int) string {
	w := dialogWidth(maxW, max(d.width, 50))
	body := maxH - 4
	d.scroll = min(d.scroll, max(0, len(d.lines)-body))
	lines := d.lines[d.scroll:]
	return frame(th, d.title, lines, w, maxH-2)
}

// --- checklist (filters) -------------------------------------------------

type checkItem struct {
	label   string
	color   string
	checked bool
	header  bool
	toggle  func(bool) error
}

type checklistDialog struct {
	title  string
	items  []checkItem
	sel    int
	onDone func() tea.Cmd
	err    error
}

func (d *checklistDialog) Update(k tea.KeyPressMsg) (tea.Cmd, bool) {
	move := func(dir int) {
		for i := d.sel + dir; i >= 0 && i < len(d.items); i += dir {
			if !d.items[i].header {
				d.sel = i
				return
			}
		}
	}
	switch k.String() {
	case "j", "down":
		move(1)
	case "k", "up":
		move(-1)
	case "space", "x", "enter":
		it := &d.items[d.sel]
		if !it.header {
			if err := it.toggle(!it.checked); err != nil {
				d.err = err
			} else {
				it.checked = !it.checked
			}
		}
	case "esc", "q", "f":
		return d.onDone(), true
	}
	return nil, false
}

func (d *checklistDialog) View(th *theme.Theme, maxW, maxH int) string {
	w := dialogWidth(maxW, 50)
	var lines []string
	for i, it := range d.items {
		if it.header {
			if i > 0 {
				lines = append(lines, "")
			}
			lines = append(lines, th.Header.Render(it.label))
			continue
		}
		box := "[ ]"
		if it.checked {
			box = "[x]"
		}
		l := " " + box + " " + th.Fg(it.color).Render("●") + " " + it.label
		if i == d.sel {
			l = th.Selected.Render(theme.Fit(" "+box+" ● "+it.label, w-4))
		}
		lines = append(lines, l)
	}
	lines = append(lines, "", th.Faint.Render("space toggle · esc close"))
	if d.err != nil {
		lines = append(lines, th.ErrorText.Render(d.err.Error()))
	}
	return frame(th, d.title, lines, w, maxH)
}
