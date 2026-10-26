package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"caldo/internal/config"
	"caldo/internal/model"
	"caldo/internal/store"
	"caldo/internal/ui/theme"
)

func newTestModel(t *testing.T) *Model {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	n := time.Now()
	day := time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, time.Local)
	must(t, st.SaveEvent(&model.Event{CalendarID: store.LocalCalendarID, Title: "Standup",
		Start: day.Add(10 * time.Hour), End: day.Add(10*time.Hour + 15*time.Minute),
		Recurrence: []string{"RRULE:FREQ=DAILY"}}))
	must(t, st.SaveEvent(&model.Event{CalendarID: store.LocalCalendarID, Title: "Lunch",
		Start: day.Add(13 * time.Hour), End: day.Add(14 * time.Hour)}))
	must(t, st.SaveTask(&model.Task{ProjectID: store.LocalInboxID, Content: "Buy milk", Priority: 4}))
	must(t, st.SaveTask(&model.Task{ProjectID: store.LocalInboxID, Content: "Read", Checked: true}))
	m := New(config.Default(), st, nil)
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 34})
	return m
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func screen(m *Model) string { return ansi.Strip(m.View().Content) }

func press(m *Model, keys ...string) {
	for _, k := range keys {
		var msg tea.KeyPressMsg
		switch k {
		case "space":
			msg = tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
		case "tab":
			msg = tea.KeyPressMsg{Code: tea.KeyTab}
		case "esc":
			msg = tea.KeyPressMsg{Code: tea.KeyEscape}
		case "enter":
			msg = tea.KeyPressMsg{Code: tea.KeyEnter}
		default:
			msg = tea.KeyPressMsg{Code: rune(k[0]), Text: k}
		}
		m.Update(msg)
	}
}

func TestSplitLayoutAndFocus(t *testing.T) {
	m := newTestModel(t)
	s := screen(m)
	if os.Getenv("CALDO_SHOW") != "" {
		t.Log("\n" + s)
	}
	lines := strings.Split(s, "\n")
	if len(lines) != 34 {
		t.Fatalf("screen has %d lines, want 34", len(lines))
	}
	for i, l := range lines {
		if w := theme.StringWidth(l); w != 120 {
			t.Errorf("line %d width %d, want 120: %q", i, w, l)
		}
	}
	if !strings.Contains(s, "Calendar ·") || !strings.Contains(s, "Tasks ·") {
		t.Fatal("split view should show both panes")
	}
	if !strings.Contains(s, "Standup") || !strings.Contains(s, "Buy milk") {
		t.Fatal("expected sample event and task on screen")
	}

	press(m, "\\") // full screen
	if s := screen(m); strings.Contains(s, "Tasks ·") {
		t.Fatal("full-screen calendar should hide tasks")
	}
	press(m, "space")
	s = screen(m)
	if !strings.Contains(s, "Tasks ·") || strings.Contains(s, "Calendar ·") {
		t.Fatal("space in full-screen should switch to the tasks pane")
	}
	if os.Getenv("CALDO_SHOW") != "" {
		t.Log("\n" + s)
	}
}

func TestEventCursorAndViews(t *testing.T) {
	m := newTestModel(t)
	press(m, "tab")
	o, ok := m.cal.Selected()
	if !ok || o.Event.Title != "Standup" {
		t.Fatalf("expected Standup selected, got %+v %v", o.Event, ok)
	}
	press(m, "j")
	if o, _ := m.cal.Selected(); o.Event.Title != "Lunch" {
		t.Fatalf("j should move to Lunch, got %q", o.Event.Title)
	}
	for _, v := range []string{"2", "3"} {
		press(m, v)
		s := screen(m)
		if os.Getenv("CALDO_SHOW") != "" {
			t.Log("\n" + s)
		}
		if !strings.Contains(s, "Standup") {
			t.Errorf("view %s should show Standup", v)
		}
	}
}

func TestTaskCompleteAndQuickAdd(t *testing.T) {
	m := newTestModel(t)
	press(m, "space") // focus tasks
	if sel := m.tasks.Selected(); sel == nil || sel.Content != "Buy milk" {
		t.Fatalf("expected Buy milk selected, got %+v", sel)
	}
	press(m, "x")
	m.Update(changed())
	press(m, "a")
	for _, r := range "Call bob p1 tomorrow" {
		m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	press(m, "enter")
	ts, _ := m.st.Tasks(true)
	var found *model.Task
	for _, tk := range ts {
		if tk.Content == "Call bob" {
			found = tk
		}
		if tk.Content == "Buy milk" && !tk.Checked {
			t.Error("Buy milk should be completed")
		}
	}
	if found == nil || found.Priority.Display() != 1 || found.Due == nil || found.Due.String != "tomorrow" {
		t.Fatalf("quick add parsed wrong: %+v", found)
	}
}

// Emoji with variation selectors (☀️, 🍽️, 3️⃣) are one cell under wcwidth but
// two as grapheme clusters; every line must match the renderer's method.
func TestEmojiWidths(t *testing.T) {
	m := newTestModel(t)
	n := time.Now()
	day := time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, time.Local)
	for i, title := range []string{"☀️Breakfast", "🐶🍽️Lunch №1", "3️⃣Interview with a candidate (round three) | @someone long title"} {
		must(t, m.st.SaveEvent(&model.Event{CalendarID: store.LocalCalendarID, Title: title,
			Start: day.Add(time.Duration(7+i) * time.Hour), End: day.Add(time.Duration(8+i) * time.Hour)}))
	}
	must(t, m.st.SaveTask(&model.Task{ProjectID: store.LocalInboxID, Content: "🌤️ task with emoji"}))
	defer func(old ansi.Method) { theme.WidthMethod = old }(theme.WidthMethod)
	for _, method := range []ansi.Method{ansi.WcWidth, ansi.GraphemeWidth} {
		theme.WidthMethod = method
		m.Update(changed())
		for _, view := range []string{"1", "2", "3"} {
			press(m, view)
			sc := screen(m)
			if strings.Contains(sc, "\uFE0F") {
				t.Fatal("emoji presentation selectors must be stripped before drawing")
			}
			for i, l := range strings.Split(sc, "\n") {
				if w := method.StringWidth(l); w != 120 {
					t.Errorf("method %d view %s line %d width %d: %q", method, view, i, w, l)
				}
			}
		}
	}
}

func TestPasteIntoInput(t *testing.T) {
	m := newTestModel(t)
	press(m, "space", "a")
	m.Update(tea.PasteMsg{Content: "Pasted task p2"})
	press(m, "enter")
	ts, _ := m.st.Tasks(false)
	for _, tk := range ts {
		if tk.Content == "Pasted task" {
			return
		}
	}
	t.Fatal("pasted text did not reach the quick-add input")
}
