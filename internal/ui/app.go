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

	"caldo/internal/agenda"
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
	// dataVersion detects writes by other processes; see watchTick.
	dataVersion int64
	offline     bool
	syncErr     string

	calColors map[string]string
	// The task selected in each pane after the last update; see linkPanes.
	lastTaskSel, lastCalTask string
	// Geometry of the last frame, for mouse hit-testing.
	layout    layout
	dlgRect   rect
	lastClick click
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
	m.cal.DimPast = cfg.Calendar.DimPast
	m.tasks = tasks.New(th, tasks.ParseView(cfg.Tasks.DefaultView), [3]tasks.GroupBy{
		tasks.ParseGroupBy(cfg.Tasks.GroupToday),
		tasks.ParseGroupBy(cfg.Tasks.GroupUpcoming),
		tasks.ParseGroupBy(cfg.Tasks.GroupAll),
	}, cfg.Tasks.ShowCompleted)
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
	occs := recur.Occurrences(evs, from, to)
	if m.cfg.Calendar.ShowTasks {
		occs = append(occs, m.taskOccurrences(from, to)...)
		recur.Sort(occs)
	}
	return occs
}

func (m *Model) taskOccurrences(from, to time.Time) []model.Occurrence {
	ts, err := m.st.Tasks(false)
	if err != nil {
		m.setError(err)
		return nil
	}
	return agenda.TaskOccurrences(ts, from, to)
}

func (m *Model) CalendarColor(id string) string { return m.calColors[id] }

// --- messages ------------------------------------------------------------

type dataChangedMsg struct{}

type syncDoneMsg struct {
	offline bool
	err     error
}

type syncTickMsg struct{}

type watchTickMsg struct{}

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
	// Tasks in the calendar are colored by project; ids never collide.
	if ps, err := m.st.Projects(); err == nil {
		for _, p := range ps {
			m.calColors[p.ID] = p.Color
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
	m.cal.SelectNow()
	m.dataVersion, _ = m.st.DataVersion()
	// Re-check the size once the resize listener is up: a tiling WM can
	// resize the terminal between the initial size read and SIGWINCH setup.
	cmds := []tea.Cmd{tea.RequestWindowSize, tea.RequestBackgroundColor, watchTick()}
	if m.syncer != nil && m.syncer.Enabled() {
		cmds = append(cmds, m.startSync())
	}
	return tea.Batch(cmds...)
}

// watchTick polls for writes by other processes (background `caldo sync`).
func watchTick() tea.Cmd {
	return tea.Tick(15*time.Second, func(time.Time) tea.Msg { return watchTickMsg{} })
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
	model, cmd := m.update(msg)
	m.linkPanes()
	return model, cmd
}

// linkPanes keeps both panes on the same day: the tasks pane's Today view
// lists the calendar's selected day, a task selected in one pane is selected
// in the other, and moving through Upcoming or All jumps the calendar to the
// selected task's due date.
func (m *Model) linkPanes() {
	if t := m.tasks.Selected(); m.focus == paneTasks && t != nil && t.ID != m.lastTaskSel {
		if d, _, ok := t.Due.Time(); ok {
			day := time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, time.Local)
			// Today already lists the calendar's day; its overdue tasks must not move it.
			if m.tasks.CurrentView() != tasks.Today || day.Equal(m.cal.Cursor()) {
				m.cal.SelectEvent("task:"+t.ID, day)
			}
		}
	}
	m.tasks.SetDay(m.cal.SelectedDay())
	calTask := ""
	if o, ok := m.cal.Selected(); ok && o.Task != nil {
		calTask = o.Task.ID
	}
	if m.focus == paneCalendar && calTask != "" && calTask != m.lastCalTask {
		m.tasks.Select(calTask)
	}
	m.lastCalTask = calTask
	m.lastTaskSel = ""
	if t := m.tasks.Selected(); t != nil {
		m.lastTaskSel = t.ID
	}
}

func (m *Model) update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		return m, nil

	case tea.BackgroundColorMsg:
		// Event blocks in the day view are tinted against the real background.
		m.th.Background = msg.Color
		return m, nil

	case tea.ModeReportMsg:
		// The renderer switches to grapheme widths when the terminal reports
		// mode 2027; layout has to measure the same way.
		if msg.Mode == ansi.ModeUnicodeCore && (msg.Value == ansi.ModeReset ||
			msg.Value == ansi.ModeSet || msg.Value == ansi.ModePermanentlySet) {
			theme.WidthMethod = ansi.GraphemeWidth
		}
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

	case watchTickMsg:
		if v, err := m.st.DataVersion(); err == nil && v != m.dataVersion {
			m.dataVersion = v
			m.reload()
		}
		return m, watchTick()

	case tea.MouseClickMsg:
		return m, m.handleClick(msg.Mouse())

	case tea.MouseWheelMsg:
		return m, m.handleWheel(msg.Mouse())

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
	if d, ok := m.dlg.(msgDialog); ok {
		return m, d.HandleMsg(msg)
	}
	return m, nil
}

func (m *Model) handleKey(k tea.KeyPressMsg) tea.Cmd {
	key := k.String()
	if key == "esc" && m.filter != "" {
		m.applyFilter("")
		return nil
	}
	return m.action(m.keys.action(key))
}

// action runs a key-bound action; mouse handling reuses it.
func (m *Model) action(act string) tea.Cmd {
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
	case config.ActViewMonth, config.ActViewDays, config.ActViewWeek, config.ActViewAgenda:
		// 1–4 switch the views of the focused pane; the tasks pane has three.
		i := map[string]int{config.ActViewMonth: 0, config.ActViewDays: 1, config.ActViewWeek: 2, config.ActViewAgenda: 3}[act]
		if m.focus == paneTasks {
			if i < 3 {
				m.tasks.SetView(tasks.View(i))
			}
		} else {
			m.cal.SetView(calendar.View(i))
		}
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
			if o.Task != nil {
				m.dlg = m.taskDetail(o.Task)
			} else {
				m.dlg = m.eventDetail(o)
			}
		} else {
			c.FocusList()
		}
	case config.ActAdd:
		return m.quickAddEvent()
	case config.ActAddFull:
		return m.newEventInEditor()
	case config.ActEdit, config.ActDelete, config.ActPostpone, config.ActComplete, config.ActPriority:
		o, ok := c.Selected()
		if !ok {
			return nil
		}
		if o.Task != nil {
			return m.calendarTaskKey(act, o.Task)
		}
		switch act {
		case config.ActEdit:
			return m.editEvent(o)
		case config.ActDelete:
			return m.deleteEvent(o)
		case config.ActPostpone:
			return m.moveEvent(o)
		}
	}
	return nil
}

// calendarTaskKey runs task actions on a task selected in the calendar.
func (m *Model) calendarTaskKey(act string, t *model.Task) tea.Cmd {
	switch act {
	case config.ActEdit:
		return m.editTask(t)
	case config.ActDelete:
		return m.deleteTask(t)
	case config.ActPostpone:
		return m.rescheduleTask(t)
	case config.ActComplete:
		return m.toggleTask(t)
	case config.ActPriority:
		return m.cyclePriority(t)
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
	if m.cfg.General.Mouse {
		v.MouseMode = tea.MouseModeCellMotion
	}
	bodyH := m.h - 1
	var body string
	m.layout = layout{calW: m.w, tasksX: 0, tasksW: m.w}
	if m.split && m.w >= 60 {
		calW := int(float64(m.w) * m.cfg.General.SplitRatio)
		m.layout = layout{split: true, calW: calW, tasksX: calW, tasksW: m.w - calW}
		left := m.renderCalendar(calW, bodyH)
		right := m.renderTasks(m.w-calW, bodyH)
		body = joinColumns(left, right)
	} else if m.focus == paneCalendar {
		body = m.renderCalendar(m.w, bodyH)
	} else {
		body = m.renderTasks(m.w, bodyH)
	}
	screen := body + "\n" + m.statusBar()
	m.dlgRect = rect{}
	if m.dlg != nil {
		fg := m.dlg.View(m.th, m.w, m.h)
		fw, fh := theme.Size(fg)
		x, y := (m.w-fw)/2, (m.h-fh)/2
		if a, ok := m.dlg.(anchoredDialog); ok {
			if ax, ay, ok := a.Anchor(); ok {
				x, y = ax, ay
			}
		}
		x, y = max(0, min(x, m.w-fw)), max(0, min(y, m.h-fh))
		m.dlgRect = rect{x, y, fw, fh}
		screen = theme.OverlayAt(screen, fg, x, y, m.w, m.h)
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
	return theme.Fit(left, m.w-theme.StringWidth(r)) + r
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
