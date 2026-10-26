// Package ui is the root Bubble Tea model: two panes (calendar, tasks) shown
// side by side or one at a time, a status bar, and modal dialogs.
package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"caldo/internal/config"
	"caldo/internal/model"
	"caldo/internal/recur"
	"caldo/internal/store"
	"caldo/internal/ui/calendar"
	"caldo/internal/ui/tasks"
	"caldo/internal/ui/theme"
)

// Syncer runs one push+pull cycle across configured providers.
type Syncer interface {
	Enabled() bool
	Sync(ctx context.Context) (offline bool, err error)
}

type pane int

const (
	paneCalendar pane = iota
	paneTasks
)

type Model struct {
	cfg    *config.Config
	st     *store.Store
	syncer Syncer
	th     *theme.Theme
	keys   keymap

	cal   calendar.Model
	tasks tasks.Model

	focus pane
	split bool

	dlg     dialog
	message string
	isError bool
	filter  string

	syncing  bool
	lastSync time.Time
	offline  bool
	syncErr  string

	calColors map[string]string
	w, h      int
}

func New(cfg *config.Config, st *store.Store, syncer Syncer) *Model {
	th := theme.New(cfg.Theme)
	ws := time.Monday
	if strings.EqualFold(cfg.Calendar.WeekStart, "sunday") {
		ws = time.Sunday
	}
	m := &Model{
		cfg:    cfg,
		st:     st,
		syncer: syncer,
		th:     th,
		keys:   newKeymap(cfg.Keys),
		split:  cfg.General.Layout != "full",
	}
	m.cal = calendar.New(m, th, calendar.ParseView(cfg.Calendar.DefaultView), ws, cfg.Calendar.AgendaDays, cfg.Calendar.TimeFormat)
	m.tasks = tasks.New(th, tasks.ParseGroupBy(cfg.Tasks.GroupBy), cfg.Tasks.ShowCompleted)
	m.reload()
	return m
}

// --- calendar.Source -----------------------------------------------------

func (m *Model) Occurrences(from, to time.Time) []model.Occurrence {
	evs, err := m.st.EventsForRange(from, to)
	if err != nil {
		m.setError(err)
		return nil
	}
	return recur.Occurrences(evs, from, to)
}

func (m *Model) CalendarColor(id string) string { return m.calColors[id] }

// --- messages ------------------------------------------------------------

type dataChangedMsg struct{}

type syncDoneMsg struct {
	offline bool
	err     error
}

type syncTickMsg struct{}

type statusMsg struct {
	text  string
	isErr bool
}

func changed() tea.Msg { return dataChangedMsg{} }

func (m *Model) reload() {
	m.calColors = map[string]string{}
	if cals, err := m.st.Calendars(); err == nil {
		for _, c := range cals {
			m.calColors[c.ID] = c.Color
		}
	}
	m.cal.Invalidate()
	ts, err := m.st.Tasks(true)
	if err != nil {
		m.setError(err)
		return
	}
	ps, _ := m.st.Projects()
	var enabled []model.Project
	for _, p := range ps {
		if p.Enabled {
			enabled = append(enabled, p)
		}
	}
	ss, _ := m.st.Sections()
	m.tasks.SetData(tasks.Data{Tasks: ts, Projects: enabled, Sections: ss})
}

func (m *Model) setError(err error) {
	m.message, m.isError = err.Error(), true
}

func (m *Model) setMessage(s string) {
	m.message, m.isError = s, false
}

func (m *Model) Init() tea.Cmd {
	if m.syncer != nil && m.syncer.Enabled() {
		return m.startSync()
	}
	return nil
}

func (m *Model) startSync() tea.Cmd {
	if m.syncer == nil || !m.syncer.Enabled() {
		m.setMessage("No providers configured — run `caldo auth google` / `caldo auth todoist`")
		return nil
	}
	if m.syncing {
		return nil
	}
	m.syncing = true
	s := m.syncer
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		offline, err := s.Sync(ctx)
		return syncDoneMsg{offline: offline, err: err}
	}
}

func (m *Model) scheduleSync() tea.Cmd {
	interval := m.cfg.SyncInterval()
	if m.offline {
		// Retry sooner while offline, but not aggressively.
		interval = min(interval, time.Minute)
	}
	return tea.Tick(interval, func(time.Time) tea.Msg { return syncTickMsg{} })
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		return m, nil

	case dataChangedMsg:
		m.reload()
		return m, nil

	case statusMsg:
		m.message, m.isError = msg.text, msg.isErr
		return m, nil

	case editorDoneMsg:
		return m, m.finishEditor(msg)

	case syncDoneMsg:
		m.syncing = false
		m.offline = msg.offline
		if msg.err != nil && !msg.offline {
			m.syncErr = msg.err.Error()
			m.setError(fmt.Errorf("sync: %w", msg.err))
		} else {
			m.syncErr = ""
		}
		if msg.err == nil {
			m.lastSync = time.Now()
		}
		m.reload()
		return m, m.scheduleSync()

	case syncTickMsg:
		return m, m.startSync()

	case tea.KeyPressMsg:
		if m.dlg != nil {
			cur := m.dlg
			cmd, done := cur.Update(msg)
			// A dialog's callback may open the next dialog; only close if it did not.
			if done && m.dlg == cur {
				m.dlg = nil
			}
			return m, cmd
		}
		m.message, m.isError = "", false
		return m, m.handleKey(msg)
	}
	return m, nil
}

func (m *Model) handleKey(k tea.KeyPressMsg) tea.Cmd {
	key := k.String()
	if key == "esc" && m.filter != "" {
		m.applyFilter("")
		return nil
	}
	act := m.keys.action(key)
	switch act {
	case config.ActQuit:
		return tea.Quit
	case config.ActToggleFocus:
		if m.focus == paneCalendar {
			m.focus = paneTasks
		} else {
			m.focus = paneCalendar
		}
		return nil
	case config.ActToggleSplit:
		m.split = !m.split
		return nil
	case config.ActViewMonth, config.ActViewWeek, config.ActViewAgenda:
		v := map[string]calendar.View{config.ActViewMonth: calendar.Month, config.ActViewWeek: calendar.Week, config.ActViewAgenda: calendar.Agenda}[act]
		m.cal.SetView(v)
		m.focus = paneCalendar
		return nil
	case config.ActSync:
		if m.syncing {
			m.setMessage("Sync already running…")
			return nil
		}
		return m.startSync()
	case config.ActHelp:
		m.dlg = m.helpDialog()
		return nil
	case config.ActFilters:
		m.dlg = m.filtersDialog()
		return nil
	case config.ActSearch:
		m.dlg = newInput("Filter", "matches event titles, task titles and labels · empty clears", m.filter, func(s string) tea.Cmd {
			m.applyFilter(s)
			return nil
		})
		return nil
	case config.ActConflicts:
		m.dlg = m.conflictsDialog()
		return nil
	}
	if m.focus == paneCalendar {
		return m.calendarKey(act)
	}
	return m.tasksKey(act)
}

func (m *Model) applyFilter(s string) {
	m.filter = s
	m.cal.SetFilter(s)
	m.tasks.SetFilter(s)
}

func (m *Model) calendarKey(act string) tea.Cmd {
	c := &m.cal
	switch act {
	case config.ActLeft:
		c.Left()
	case config.ActRight:
		c.Right()
	case config.ActDown:
		c.Down()
	case config.ActUp:
		c.Up()
	case config.ActTop:
		c.Top()
	case config.ActBottom:
		c.Bottom()
	case config.ActToday:
		c.Today()
	case config.ActPrevPeriod:
		c.Period(-1)
	case config.ActNextPeriod:
		c.Period(1)
	case config.ActSubFocus:
		c.ToggleSubFocus()
	case config.ActOpen:
		if o, ok := c.Selected(); ok {
			m.dlg = m.eventDetail(o)
		} else {
			c.FocusList()
		}
	case config.ActAdd:
		return m.quickAddEvent()
	case config.ActAddFull:
		return m.newEventInEditor()
	case config.ActEdit:
		if o, ok := c.Selected(); ok {
			return m.editEvent(o)
		}
	case config.ActDelete:
		if o, ok := c.Selected(); ok {
			return m.deleteEvent(o)
		}
	case config.ActPostpone:
		if o, ok := c.Selected(); ok {
			return m.moveEvent(o)
		}
	}
	return nil
}

func (m *Model) tasksKey(act string) tea.Cmd {
	t := &m.tasks
	switch act {
	case config.ActDown:
		t.Down()
	case config.ActUp:
		t.Up()
	case config.ActTop:
		t.Top()
	case config.ActBottom:
		t.Bottom()
	case config.ActRight, config.ActNextPeriod:
		t.NextGroup()
	case config.ActLeft, config.ActPrevPeriod:
		t.PrevGroup()
	case config.ActGroupBy:
		g := t.CycleGroupBy()
		m.setMessage("Grouping tasks by " + g.String())
	case config.ActShowDone:
		if t.ToggleCompleted() {
			m.setMessage("Showing completed tasks")
		} else {
			m.setMessage("Hiding completed tasks")
		}
	case config.ActAdd:
		return m.quickAddTask()
	case config.ActAddFull:
		return m.newTaskInEditor()
	}
	sel := t.Selected()
	if sel == nil {
		return nil
	}
	switch act {
	case config.ActOpen:
		m.dlg = m.taskDetail(sel)
	case config.ActEdit:
		return m.editTask(sel)
	case config.ActComplete:
		return m.toggleTask(sel)
	case config.ActDelete:
		return m.deleteTask(sel)
	case config.ActPriority:
		return m.cyclePriority(sel)
	case config.ActPostpone:
		return m.rescheduleTask(sel)
	}
	return nil
}

// --- view ----------------------------------------------------------------

func (m *Model) View() tea.View {
	var v tea.View
	v.AltScreen = true
	v.WindowTitle = "CalDo"
	if m.w == 0 || m.h == 0 {
		v.SetContent("")
		return v
	}
	bodyH := m.h - 1
	var body string
	if m.split && m.w >= 60 {
		calW := int(float64(m.w) * m.cfg.General.SplitRatio)
		left := m.renderCalendar(calW, bodyH)
		right := m.renderTasks(m.w-calW, bodyH)
		body = joinColumns(left, right)
	} else if m.focus == paneCalendar {
		body = m.renderCalendar(m.w, bodyH)
	} else {
		body = m.renderTasks(m.w, bodyH)
	}
	screen := body + "\n" + m.statusBar()
	if m.dlg != nil {
		screen = theme.Overlay(screen, m.dlg.View(m.th, m.w, m.h), m.w, m.h)
	}
	v.SetContent(screen)
	return v
}

func (m *Model) renderCalendar(w, h int) string {
	active := m.focus == paneCalendar
	m.cal.SetSize(w-4, h-2)
	return m.th.Box(m.cal.Title(), m.cal.Render(w-4, h-2, active), w, h, active)
}

func (m *Model) renderTasks(w, h int) string {
	active := m.focus == paneTasks
	return m.th.Box(m.tasks.Title(), m.tasks.Render(w-4, h-2, active), w, h, active)
}

func joinColumns(a, b string) string {
	al, bl := strings.Split(a, "\n"), strings.Split(b, "\n")
	n := max(len(al), len(bl))
	var sb strings.Builder
	for i := range n {
		if i > 0 {
			sb.WriteByte('\n')
		}
		if i < len(al) {
			sb.WriteString(al[i])
		}
		if i < len(bl) {
			sb.WriteString(bl[i])
		}
	}
	return sb.String()
}

func (m *Model) statusBar() string {
	th := m.th
	var left string
	switch {
	case m.message != "" && m.isError:
		left = th.ErrorText.Render(" " + m.message)
	case m.message != "":
		left = " " + m.message
	default:
		hints := [][2]string{{"spc", "switch"}, {"a", "add"}, {"e", "edit"}, {"d", "del"}, {"r", "sync"}, {"?", "help"}}
		if m.focus == paneTasks {
			hints = [][2]string{{"spc", "switch"}, {"a", "add"}, {"x", "done"}, {"e", "edit"}, {"p", "prio"}, {"s", "group"}, {"?", "help"}}
		}
		var parts []string
		for _, h := range hints {
			parts = append(parts, th.StatusKey.Render(h[0])+th.StatusBar.Render(" "+h[1]))
		}
		left = " " + strings.Join(parts, "  ")
	}
	if m.filter != "" {
		left += th.DueToday.Render("  filter: " + m.filter + " (esc)")
	}

	var right []string
	if n := m.st.ConflictCount(); n > 0 {
		right = append(right, th.ErrorText.Render(fmt.Sprintf("⚠ %d conflict(s) · C", n)))
	}
	pending := m.st.PendingCount()
	switch {
	case m.syncing:
		right = append(right, th.DueToday.Render("⟳ syncing…"))
	case m.syncer == nil || !m.syncer.Enabled():
		right = append(right, th.Faint.Render("local only"))
	case m.offline:
		right = append(right, th.Overdue.Render(fmt.Sprintf("offline · %d pending", pending)))
	case m.syncErr != "":
		right = append(right, th.Overdue.Render("sync error"))
	case m.lastSync.IsZero():
		right = append(right, th.Faint.Render("not synced"))
	default:
		s := "✓ synced " + ago(m.lastSync)
		if pending > 0 {
			s += fmt.Sprintf(" · %d pending", pending)
		}
		right = append(right, th.DoneIcon.Render(s))
	}
	r := strings.Join(right, th.Faint.Render("  │  ")) + " "
	return theme.Fit(left, m.w-ansi.StringWidth(r)) + r
}

func ago(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	default:
		return t.Format("15:04")
	}
}
