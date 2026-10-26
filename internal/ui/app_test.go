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
	"caldo/internal/ui/calendar"
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
	cfg := config.Default()
	cfg.Tasks.DefaultView = "all" // the sample tasks have no due dates
	m := New(cfg, st, nil)
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

func TestTasksInCalendar(t *testing.T) {
	m := newTestModel(t)
	today := time.Now().Format("2006-01-02")
	must(t, m.st.SaveTask(&model.Task{ProjectID: store.LocalInboxID, Content: "Pay rent", Priority: 4,
		Due: &model.Due{Date: today}}))
	must(t, m.st.SaveTask(&model.Task{ProjectID: store.LocalInboxID, Content: "Call mom",
		Due: &model.Due{Date: today + "T18:30:00"}}))
	m.Update(changed())

	s := screen(m)
	if !strings.Contains(s, "all day     ☐ Pay rent") || !strings.Contains(s, "18:30       ☐ Call mom") {
		t.Fatalf("dated tasks should be listed in the day:\n%s", s)
	}
	press(m, "3") // agenda
	if !strings.Contains(screen(m), "☐ Pay rent") {
		t.Fatal("agenda should list tasks")
	}
	press(m, "2") // week
	if !strings.Contains(screen(m), "☐") {
		t.Fatal("week view should list tasks")
	}

	// Complete the all-day task from the calendar: it is the first item today.
	press(m, "1", "tab", "g")
	o, ok := m.cal.Selected()
	if !ok || o.Task == nil || o.Task.Content != "Pay rent" {
		t.Fatalf("expected Pay rent selected, got %+v", o)
	}
	press(m, "x")
	m.Update(changed())
	if tk, _ := m.st.Task(o.Task.ID); !tk.Checked {
		t.Fatal("x in the calendar should complete the task")
	}
	if s := screen(m); strings.Contains(s, "☐ Pay rent") {
		t.Fatalf("completed task should leave the calendar:\n%s", s)
	}

	m.cfg.Calendar.ShowTasks = false
	m.Update(changed())
	if strings.Contains(screen(m), "18:30       ☐ Call mom") {
		t.Fatal("show_tasks = false should hide tasks from the calendar")
	}
}

// at finds text on the rendered screen and returns its cell coordinates.
func at(t *testing.T, m *Model, text string) (int, int) {
	t.Helper()
	for y, l := range strings.Split(screen(m), "\n") {
		if i := strings.Index(l, text); i >= 0 {
			return theme.StringWidth(l[:i]), y
		}
	}
	t.Fatalf("%q not on screen:\n%s", text, screen(m))
	return 0, 0
}

func clickAt(m *Model, b tea.MouseButton, x, y int) {
	m.Update(tea.MouseClickMsg{X: x, Y: y, Button: b})
}

func TestMouse(t *testing.T) {
	m := newTestModel(t)
	if m.View().MouseMode != tea.MouseModeCellMotion {
		t.Fatal("mouse should be enabled by default")
	}

	// Left click on a task focuses the tasks pane and selects it.
	x, y := at(t, m, "Buy milk")
	clickAt(m, tea.MouseLeft, x, y)
	if m.focus != paneTasks || m.tasks.Selected() == nil || m.tasks.Selected().Content != "Buy milk" {
		t.Fatalf("click should select Buy milk in the tasks pane, focus=%v sel=%+v", m.focus, m.tasks.Selected())
	}
	// Double click opens its card; clicking outside closes it.
	clickAt(m, tea.MouseLeft, x, y)
	if m.dlg == nil || !strings.Contains(screen(m), "Task") {
		t.Fatal("double click should open the task card")
	}
	clickAt(m, tea.MouseLeft, 0, 0)
	if m.dlg != nil {
		t.Fatal("click outside should close the dialog")
	}

	// Right click opens a context menu; clicking "Complete" completes the task.
	m.lastClick = click{}
	milk := m.tasks.Selected().ID
	clickAt(m, tea.MouseRight, x, y)
	if _, ok := m.dlg.(*choiceDialog); !ok {
		t.Fatalf("right click should open a context menu, got %T", m.dlg)
	}
	if os.Getenv("CALDO_SHOW") != "" {
		t.Log("\n" + screen(m))
	}
	cx, cy := at(t, m, "Complete")
	clickAt(m, tea.MouseLeft, cx, cy)
	m.Update(changed())
	if tk, _ := m.st.Task(milk); tk == nil || !tk.Checked {
		t.Fatal("menu Complete should complete the task")
	}

	// Click an event in the calendar: focus moves there and it is selected.
	x, y = at(t, m, "Lunch")
	clickAt(m, tea.MouseLeft, x, y)
	if o, ok := m.cal.Selected(); m.focus != paneCalendar || !ok || o.Event.Title != "Lunch" {
		t.Fatalf("click should select Lunch, focus=%v", m.focus)
	}

	// Wheel over the month grid flips months.
	month := m.cal.Cursor().Month()
	gx, gy := at(t, m, "Mo")
	m.Update(tea.MouseWheelMsg{X: gx + 1, Y: gy + 1, Button: tea.MouseWheelDown})
	if m.cal.Cursor().Month() == month {
		t.Fatal("wheel over the grid should move to the next month")
	}

	// Middle click on a day opens quick add for that day.
	clickAt(m, tea.MouseMiddle, gx+1, gy+1)
	if _, ok := m.dlg.(*inputDialog); !ok {
		t.Fatalf("middle click on a day should open quick add, got %T", m.dlg)
	}
}

func TestTaskViews(t *testing.T) {
	m := newTestModel(t)
	day := func(n int) string { return time.Now().AddDate(0, 0, n).Format("2006-01-02") }
	for _, tk := range []*model.Task{
		{Content: "Late", Due: &model.Due{Date: day(-2)}},
		{Content: "Now", Due: &model.Due{Date: day(0)}},
		{Content: "Soon", Due: &model.Due{Date: day(1)}},
		{Content: "Later on", Due: &model.Due{Date: day(5)}},
	} {
		tk.ProjectID = store.LocalInboxID
		must(t, m.st.SaveTask(tk))
	}
	m.Update(changed())
	press(m, "space") // focus tasks

	press(m, "1")
	s := screen(m)
	if !strings.Contains(s, "Tasks · Today · by date") || !strings.Contains(s, "Late") || !strings.Contains(s, "Now") ||
		strings.Contains(s, "Soon") || strings.Contains(s, "Buy milk") {
		t.Fatalf("Today should list overdue and today only:\n%s", s)
	}
	if m.cal.CurrentView() != calendar.Month {
		t.Fatal("1 in the tasks pane must not change the calendar view")
	}

	press(m, "2")
	s = screen(m)
	if !strings.Contains(s, "Tasks · Upcoming · by day") || !strings.Contains(s, "Tomorrow · ") ||
		!strings.Contains(s, "Later on") || strings.Contains(s, "Buy milk") {
		t.Fatalf("Upcoming should group dated tasks by day:\n%s", s)
	}

	// Grouping is remembered per view.
	press(m, "s") // upcoming: day -> label
	press(m, "3")
	if s := screen(m); !strings.Contains(s, "Tasks · All · by project") || !strings.Contains(s, "Buy milk") {
		t.Fatalf("All keeps its own grouping:\n%s", s)
	}
	press(m, "2")
	if !strings.Contains(screen(m), "Upcoming · by label") {
		t.Fatal("Upcoming should keep the grouping chosen for it")
	}

	// Quick add in Today gives the task a due date of today.
	press(m, "1", "a")
	for _, r := range "Fresh" {
		m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	press(m, "enter")
	ts, _ := m.st.Tasks(false)
	for _, tk := range ts {
		if tk.Content == "Fresh" {
			if tk.Due == nil || tk.Due.Date != day(0) {
				t.Fatalf("task added in Today should be due today: %+v", tk.Due)
			}
			return
		}
	}
	t.Fatal("quick-added task not found")
}
