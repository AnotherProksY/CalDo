package ui

import (
	"time"

	tea "charm.land/bubbletea/v2"

	"caldo/internal/config"
	"caldo/internal/model"
	"caldo/internal/ui/calendar"
)

type layout struct {
	split          bool
	calW           int
	tasksX, tasksW int
}

type rect struct{ x, y, w, h int }

func (r rect) contains(x, y int) bool {
	return x >= r.x && x < r.x+r.w && y >= r.y && y < r.y+r.h
}

type click struct {
	x, y int
	at   time.Time
}

const doubleClick = 400 * time.Millisecond

// isDouble records a left click and reports whether it completes a double click.
func (m *Model) isDouble(x, y int) bool {
	now := time.Now()
	d := m.lastClick.x == x && m.lastClick.y == y && now.Sub(m.lastClick.at) < doubleClick
	m.lastClick = click{x, y, now}
	if d {
		m.lastClick = click{}
	}
	return d
}

// paneAt returns the pane under screen column x and the content-area
// coordinates of (x, y) inside it (panes draw "│ " before their content).
func (m *Model) paneAt(x, y int) (pane, int, int) {
	p := m.focus
	if m.layout.split {
		p = paneCalendar
		if x >= m.layout.tasksX {
			p = paneTasks
		}
	}
	x0 := 0
	if p == paneTasks {
		x0 = m.layout.tasksX
	}
	return p, x - x0 - 2, y - 1
}

func (m *Model) handleClick(mo tea.Mouse) tea.Cmd {
	if m.dlg != nil {
		return m.clickDialog(mo)
	}
	m.message, m.isError = "", false
	if mo.Y >= m.h-1 {
		// Status bar: the first hint ("spc switch") switches panes.
		if mo.Button == tea.MouseLeft && mo.X < 12 {
			return m.action(config.ActToggleFocus)
		}
		return nil
	}
	p, cx, cy := m.paneAt(mo.X, mo.Y)
	m.focus = p
	if p == paneCalendar {
		return m.clickCalendar(mo, cx, cy)
	}
	return m.clickTasks(mo, cy)
}

func (m *Model) clickDialog(mo tea.Mouse) tea.Cmd {
	cur := m.dlg
	if !m.dlgRect.contains(mo.X, mo.Y) {
		m.dlg = nil // clicking outside a dialog closes it
		return nil
	}
	d, ok := cur.(clickDialog)
	if !ok || mo.Button != tea.MouseLeft {
		return nil
	}
	cmd, done := d.Click(mo.Y - m.dlgRect.y)
	if done && m.dlg == cur {
		m.dlg = nil
	}
	return cmd
}

func (m *Model) clickCalendar(mo tea.Mouse, cx, cy int) tea.Cmd {
	hit, ok := m.cal.HitTest(cx, cy)
	if !ok {
		return nil
	}
	m.cal.Click(hit)
	o, isItem := m.cal.Selected()
	isItem = isItem && hit.Kind == calendar.HitItem
	switch mo.Button {
	case tea.MouseLeft:
		if m.isDouble(mo.X, mo.Y) && isItem {
			return m.action(config.ActOpen)
		}
	case tea.MouseMiddle:
		switch {
		case isItem && o.Task != nil:
			return m.toggleTask(o.Task)
		case isItem:
			return m.editEvent(o)
		default:
			return m.quickAddEvent()
		}
	case tea.MouseRight:
		if isItem {
			m.dlg = m.itemMenu(o, mo.X, mo.Y)
		} else {
			m.dlg = m.dayMenu(mo.X, mo.Y)
		}
	}
	return nil
}

func (m *Model) clickTasks(mo tea.Mouse, cy int) tea.Cmd {
	t := m.tasks.TaskAt(cy)
	if t == nil {
		return nil
	}
	m.tasks.Select(t.ID)
	switch mo.Button {
	case tea.MouseLeft:
		if m.isDouble(mo.X, mo.Y) {
			m.dlg = m.taskDetail(t)
		}
	case tea.MouseMiddle:
		return m.toggleTask(t)
	case tea.MouseRight:
		m.dlg = m.taskMenu(t, mo.X, mo.Y)
	}
	return nil
}

func (m *Model) handleWheel(mo tea.Mouse) tea.Cmd {
	up := mo.Button == tea.MouseWheelUp
	if mo.Button != tea.MouseWheelUp && mo.Button != tea.MouseWheelDown {
		return nil
	}
	if m.dlg != nil {
		// Scroll dialogs the way the arrow keys do.
		k := tea.KeyPressMsg{Code: tea.KeyDown}
		if up {
			k = tea.KeyPressMsg{Code: tea.KeyUp}
		}
		cmd, _ := m.dlg.Update(k)
		return cmd
	}
	if mo.Y >= m.h-1 {
		return nil
	}
	p, cx, cy := m.paneAt(mo.X, mo.Y)
	m.focus = p
	act := config.ActDown
	if up {
		act = config.ActUp
	}
	if p == paneCalendar {
		if hit, ok := m.cal.HitTest(cx, cy); ok && hit.Kind == calendar.HitDay {
			// Over the month grid or week header the wheel flips periods.
			act = config.ActNextPeriod
			if up {
				act = config.ActPrevPeriod
			}
		} else if m.cal.CurrentView() == calendar.Month {
			m.cal.FocusList()
		}
	}
	return m.action(act)
}

// --- context menus ---------------------------------------------------------

type menuItem struct {
	label string
	run   func() tea.Cmd
}

func menu(title string, x, y int, items []menuItem) dialog {
	labels := make([]string, len(items))
	for i, it := range items {
		labels[i] = it.label
	}
	// Open just below the pointer so the clicked row stays visible.
	return &choiceDialog{title: title, options: labels, menu: true, x: x, y: y + 1,
		onPick: func(i int) tea.Cmd { return items[i].run() }}
}

func (m *Model) itemMenu(o model.Occurrence, x, y int) dialog {
	if o.Task != nil {
		return m.taskMenu(o.Task, x, y)
	}
	return menu("Event", x, y, []menuItem{
		{"Open", func() tea.Cmd { m.dlg = m.eventDetail(o); return nil }},
		{"Edit in $EDITOR", func() tea.Cmd { return m.editEvent(o) }},
		{"Move", func() tea.Cmd { return m.moveEvent(o) }},
		{"Delete", func() tea.Cmd { return m.deleteEvent(o) }},
	})
}

func (m *Model) taskMenu(t *model.Task, x, y int) dialog {
	done := "Complete"
	if t.Checked {
		done = "Reopen"
	}
	return menu("Task", x, y, []menuItem{
		{"Open", func() tea.Cmd { m.dlg = m.taskDetail(t); return nil }},
		{"Edit in $EDITOR", func() tea.Cmd { return m.editTask(t) }},
		{done, func() tea.Cmd { return m.toggleTask(t) }},
		{"Reschedule", func() tea.Cmd { return m.rescheduleTask(t) }},
		{"Priority", func() tea.Cmd { return m.cyclePriority(t) }},
		{"Delete", func() tea.Cmd { return m.deleteTask(t) }},
	})
}

func (m *Model) dayMenu(x, y int) dialog {
	day := m.cal.SelectedDay().Format("Mon 2 Jan")
	return menu(day, x, y, []menuItem{
		{"Add event", m.quickAddEvent},
		{"Add event in $EDITOR", m.newEventInEditor},
	})
}
