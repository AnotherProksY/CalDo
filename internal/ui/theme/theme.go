// Package theme holds lipgloss styles and the panel drawing helpers.
package theme

import (
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"caldo/internal/config"
)

type Theme struct {
	Accent, Muted, Border, Today, Success, Pending, Warning, Danger, Selection color.Color

	Title       lipgloss.Style
	TitleActive lipgloss.Style
	BorderStyle lipgloss.Style
	BorderOn    lipgloss.Style
	Faint       lipgloss.Style
	Bold        lipgloss.Style
	Header      lipgloss.Style
	Selected    lipgloss.Style
	SelectedDim lipgloss.Style
	TodayStyle  lipgloss.Style
	Done        lipgloss.Style
	DoneIcon    lipgloss.Style
	PendingIcon lipgloss.Style
	Overdue     lipgloss.Style
	DueToday    lipgloss.Style
	StatusBar   lipgloss.Style
	StatusKey   lipgloss.Style
	ErrorText   lipgloss.Style
	P1, P2, P3  lipgloss.Style
}

func New(c config.Theme) *Theme {
	t := &Theme{
		Accent:    lipgloss.Color(c.Accent),
		Muted:     lipgloss.Color(c.Muted),
		Border:    lipgloss.Color(c.Border),
		Today:     lipgloss.Color(c.Today),
		Success:   lipgloss.Color(c.Success),
		Pending:   lipgloss.Color(c.Pending),
		Warning:   lipgloss.Color(c.Warning),
		Danger:    lipgloss.Color(c.Danger),
		Selection: lipgloss.Color(c.Selection),
	}
	s := lipgloss.NewStyle
	t.Title = s().Foreground(t.Muted).Bold(true)
	t.TitleActive = s().Foreground(t.Accent).Bold(true)
	t.BorderStyle = s().Foreground(t.Border)
	t.BorderOn = s().Foreground(t.Accent)
	t.Faint = s().Foreground(t.Muted)
	t.Bold = s().Bold(true)
	t.Header = s().Foreground(t.Accent).Bold(true).Underline(true)
	t.Selected = s().Background(t.Selection).Bold(true)
	if c.SelectionFg != "" {
		t.Selected = t.Selected.Foreground(lipgloss.Color(c.SelectionFg))
	}
	t.SelectedDim = s().Underline(true)
	t.TodayStyle = s().Foreground(t.Today).Bold(true)
	t.Done = s().Foreground(t.Muted).Strikethrough(true)
	t.DoneIcon = s().Foreground(t.Success)
	t.PendingIcon = s().Foreground(t.Pending)
	t.Overdue = s().Foreground(t.Danger)
	t.DueToday = s().Foreground(t.Warning)
	t.StatusBar = s().Foreground(t.Muted)
	t.StatusKey = s().Foreground(t.Accent).Bold(true)
	t.ErrorText = s().Foreground(t.Danger).Bold(true)
	t.P1 = s().Foreground(orColor(c.P1, t.Danger)).Bold(true)
	t.P2 = s().Foreground(orColor(c.P2, t.Warning))
	t.P3 = s().Foreground(orColor(c.P3, t.Accent))
	return t
}

func orColor(hex string, fallback color.Color) color.Color {
	if hex == "" {
		return fallback
	}
	return lipgloss.Color(hex)
}

// Fg returns a foreground style for a hex color, falling back to accent.
func (t *Theme) Fg(hex string) lipgloss.Style {
	if hex == "" {
		return lipgloss.NewStyle().Foreground(t.Accent)
	}
	return lipgloss.NewStyle().Foreground(lipgloss.Color(hex))
}

// WidthMethod must match how the Bubble Tea renderer measures cells: wcwidth
// by default (tmux, most terminals), grapheme clusters once the terminal
// reports Unicode core mode 2027. Otherwise emoji with variation selectors
// shift the layout.
var WidthMethod = ansi.WcWidth

// StringWidth returns the display width of s in cells.
func StringWidth(s string) int { return WidthMethod.StringWidth(s) }

// Truncate cuts s to at most w cells, appending tail when cut.
func Truncate(s string, w int, tail string) string { return WidthMethod.Truncate(s, w, tail) }

// emojiVS is the emoji presentation selector (U+FE0F). Terminals disagree on
// whether it widens the preceding character (tmux 3.4+ does, foot and wcwidth
// do not), which shifts everything after it, so it is dropped before drawing.
const emojiVS = "\uFE0F"

// Fit pads or truncates s to exactly w cells.
func Fit(s string, w int) string {
	if w <= 0 {
		return ""
	}
	s = strings.ReplaceAll(s, emojiVS, "")
	sw := StringWidth(s)
	if sw <= w {
		return s + strings.Repeat(" ", w-sw)
	}
	// Truncate and StringWidth can disagree on clusters like keycaps (3️⃣),
	// so measure the result and shorten until it fits.
	t := Truncate(s, w, "…")
	for cut := w - 1; StringWidth(t) > w && cut > 0; cut-- {
		t = Truncate(s, cut, "…")
	}
	return t + strings.Repeat(" ", max(0, w-StringWidth(t)))
}

// Box draws a rounded panel with a title in the top border, calcurse-style.
// Content lines are fitted to the inner width; missing lines are blank.
func (t *Theme) Box(title string, lines []string, w, h int, active bool) string {
	if w < 4 || h < 2 {
		return ""
	}
	bs, ts := t.BorderStyle, t.Title
	if active {
		bs, ts = t.BorderOn, t.TitleActive
	}
	inner := w - 4
	var b strings.Builder

	if title != "" {
		tt := Truncate(" "+strings.ReplaceAll(title, emojiVS, "")+" ", w-4, "…")
		b.WriteString(bs.Render("╭─") + ts.Render(tt) +
			bs.Render(strings.Repeat("─", max(0, w-3-StringWidth(tt)))+"╮"))
	} else {
		b.WriteString(bs.Render("╭" + strings.Repeat("─", w-2) + "╮"))
	}
	b.WriteByte('\n')

	side := bs.Render("│")
	for i := 0; i < h-2; i++ {
		var l string
		if i < len(lines) {
			l = lines[i]
		}
		b.WriteString(side + " " + Fit(l, inner) + " " + side + "\n")
	}
	b.WriteString(bs.Render("╰" + strings.Repeat("─", w-2) + "╯"))
	return b.String()
}

// Rule draws a horizontal separator with an optional centered label.
func (t *Theme) Rule(label string, w int, active bool) string {
	ls := t.Title
	if active {
		ls = t.TitleActive
	}
	if label == "" {
		return t.BorderStyle.Render(strings.Repeat("─", w))
	}
	lbl := " " + label + " "
	lw := StringWidth(lbl)
	if lw >= w {
		return ls.Render(Truncate(lbl, w, "…"))
	}
	left := (w - lw) / 2
	return t.BorderStyle.Render(strings.Repeat("─", left)) + ls.Render(lbl) +
		t.BorderStyle.Render(strings.Repeat("─", w-lw-left))
}

// Size returns the width and height of a multi-line block in cells.
func Size(s string) (w, h int) {
	lines := strings.Split(s, "\n")
	for _, l := range lines {
		w = max(w, StringWidth(l))
	}
	return w, len(lines)
}

// Overlay draws fg centered over bg, cell-accurately.
func Overlay(bg, fg string, width, height int) string {
	fw, fh := Size(fg)
	return OverlayAt(bg, fg, max(0, (width-fw)/2), max(0, (height-fh)/2), width, height)
}

// OverlayAt draws fg over bg with its top-left corner at (x, y).
func OverlayAt(bg, fg string, x, y, width, height int) string {
	bgLines := strings.Split(bg, "\n")
	for len(bgLines) < height {
		bgLines = append(bgLines, "")
	}
	fgLines := strings.Split(fg, "\n")
	for i, l := range fgLines {
		row := y + i
		if row >= len(bgLines) {
			break
		}
		base := Fit(bgLines[row], width)
		left := Fit(Truncate(base, x, ""), x) // pad if a wide char straddles x
		right := WidthMethod.TruncateLeft(base, x+StringWidth(l), "")
		bgLines[row] = left + "\x1b[0m" + l + "\x1b[0m" + right
	}
	return strings.Join(bgLines, "\n")
}
