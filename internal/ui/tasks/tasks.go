// Package tasks is the task pane, rendered taskbook-style: groups with
// done/total counters, status icons, and a completion summary.
package tasks

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"caldo/internal/editor"
	"caldo/internal/model"
	"caldo/internal/ui/theme"
)

type GroupBy int

const (
	ByProject GroupBy = iota
	ByDate            // Overdue / Today / Tomorrow / Next 7 days / Later / No date
	ByDay             // one group per due date
	ByLabel
	numGroupBy
)

func ParseGroupBy(s string) GroupBy {
	switch s {
	case "date":
		return ByDate
	case "day":
		return ByDay
	case "label":
		return ByLabel
	}
	return ByProject
}

func (g GroupBy) String() string { return [...]string{"project", "date", "day", "label"}[g] }

// View picks which tasks the pane lists.
type View int

const (
	Today    View = iota // the calendar's selected day (today: also overdue)
	Upcoming             // everything with a due date
	All
	numViews
)

func ParseView(s string) View {
	switch s {
	case "upcoming":
		return Upcoming
	case "all":
		return All
	}
	return Today
}

func (v View) String() string { return [...]string{"Today", "Upcoming", "All"}[v] }

// Data is everything the pane renders.
type Data struct {
	Tasks    []*model.Task // includes completed
	Projects []model.Project
	Sections []model.Section
}

type row struct {
	header  string
	task    *model.Task
	depth   int
	section bool
}

type Model struct {
	th            *theme.Theme
	data          Data
	view          View
	groupBy       [numViews]GroupBy // remembered per view
	showCompleted bool
	filter        string
	// day is the calendar's selected day, which the Today view lists; zero
	// means today.
	day time.Time

	rows   []row
	cursor int // index into rows; always a task row when any exist
	scroll int
	// lineRows maps each rendered content line to a rows index (-1: none).
	lineRows []int
}

// New creates the pane; groups holds the grouping for Today, Upcoming and All.
func New(th *theme.Theme, view View, groups [3]GroupBy, showCompleted bool) Model {
	return Model{th: th, view: view, groupBy: groups, showCompleted: showCompleted}
}

func (m *Model) SetData(d Data) {
	var keep string
	if t := m.Selected(); t != nil {
		keep = t.ID
	}
	m.data = d
	m.rebuild(keep)
}

func (m *Model) SetFilter(f string) {
	m.filter = strings.ToLower(f)
	m.rebuild(m.selectedID())
}

// CycleGroupBy changes the current view's grouping.
func (m *Model) CycleGroupBy() GroupBy {
	m.groupBy[m.view] = (m.groupBy[m.view] + 1) % numGroupBy
	m.rebuild(m.selectedID())
	return m.groupBy[m.view]
}

func (m *Model) SetView(v View) {
	m.view = v
	m.scroll = 0
	m.rebuild(m.selectedID())
}

func (m Model) CurrentView() View { return m.view }

// SetDay makes the Today view list the tasks due on day.
func (m *Model) SetDay(day time.Time) {
	day = dayOf(day)
	if day.Equal(m.Day()) {
		return
	}
	m.day = day
	m.scroll = 0
	m.rebuild(m.selectedID())
}

// Day is the day the Today view lists.
func (m *Model) Day() time.Time {
	if m.day.IsZero() {
		return today()
	}
	return m.day
}

// onToday reports whether the Today view shows the actual today, which also
// lists overdue tasks.
func (m *Model) onToday() bool { return m.Day().Equal(today()) }

// dayLabel names the Today view's day: "today" or like "Tue 27 Oct".
func (m *Model) dayLabel() string {
	if m.onToday() {
		return "today"
	}
	return m.Day().Format("Mon 2 Jan")
}

func (m *Model) ToggleCompleted() bool {
	m.showCompleted = !m.showCompleted
	m.rebuild(m.selectedID())
	return m.showCompleted
}

func (m Model) GroupByMode() GroupBy { return m.groupBy[m.view] }

func (m *Model) selectedID() string {
	if t := m.Selected(); t != nil {
		return t.ID
	}
	return ""
}

func (m *Model) Selected() *model.Task {
	if m.cursor >= 0 && m.cursor < len(m.rows) {
		return m.rows[m.cursor].task
	}
	return nil
}

// TaskAt returns the task drawn on content line y of the last Render.
func (m *Model) TaskAt(y int) *model.Task {
	if y < 0 || y >= len(m.lineRows) || m.lineRows[y] < 0 {
		return nil
	}
	return m.rows[m.lineRows[y]].task
}

// Select moves the cursor to the task with id, if visible.
func (m *Model) Select(id string) {
	for i, r := range m.rows {
		if r.task != nil && r.task.ID == id {
			m.cursor = i
			return
		}
	}
}

// CurrentProject is the project new tasks go into: the selected task's, or the group under the cursor.
func (m *Model) CurrentProject() (projectID, sectionID string) {
	if t := m.Selected(); t != nil {
		return t.ProjectID, t.SectionID
	}
	return "", ""
}

func (m *Model) Down() { m.step(1) }
func (m *Model) Up()   { m.step(-1) }

func (m *Model) step(dir int) {
	for i := m.cursor + dir; i >= 0 && i < len(m.rows); i += dir {
		if m.rows[i].task != nil {
			m.cursor = i
			return
		}
	}
}

func (m *Model) Top() {
	m.cursor = -1
	m.step(1)
}

func (m *Model) Bottom() {
	m.cursor = len(m.rows)
	m.step(-1)
}

// NextGroup / PrevGroup jump between groups.
func (m *Model) NextGroup() { m.jumpGroup(1) }
func (m *Model) PrevGroup() { m.jumpGroup(-1) }

func (m *Model) jumpGroup(dir int) {
	i := m.cursor
	// Move to the header of the current group, then to the next one.
	for i += dir; i >= 0 && i < len(m.rows); i += dir {
		if m.rows[i].header != "" && !m.rows[i].section {
			break
		}
	}
	if dir < 0 {
		for i--; i >= 0; i-- {
			if m.rows[i].header != "" && !m.rows[i].section {
				break
			}
		}
	}
	if i < 0 || i >= len(m.rows) {
		return
	}
	m.cursor = i
	m.step(1)
}

// --- building ------------------------------------------------------------

func (m *Model) visible(t *model.Task) bool {
	if t.Checked && !m.showCompleted {
		return false
	}
	if m.filter != "" && !strings.Contains(strings.ToLower(t.Content), m.filter) &&
		!strings.Contains(strings.ToLower(strings.Join(t.Labels, " ")), m.filter) {
		return false
	}
	return true
}

// viewTasks is the subset of tasks the current view lists.
func (m *Model) viewTasks() []*model.Task {
	if m.view == All {
		return m.data.Tasks
	}
	day := m.Day()
	var out []*model.Task
	for _, t := range m.data.Tasks {
		d, _, ok := t.Due.Time()
		if !ok {
			continue
		}
		dd := dayOf(d)
		switch {
		case m.view == Today && t.Checked:
			// Done tasks stay on their own day; only open ones can be overdue.
			if !dd.Equal(day) {
				continue
			}
		case m.view == Today:
			if !dd.Equal(day) && (!m.onToday() || !dd.Before(day)) {
				continue
			}
		case t.Checked && dd.Before(today()):
			continue // Upcoming: a done task from the past is not upcoming
		}
		out = append(out, t)
	}
	return out
}

func dayOf(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.Local)
}

func (m *Model) rebuild(keepID string) {
	m.rows = m.rows[:0]
	ts := m.viewTasks()
	switch m.groupBy[m.view] {
	case ByDate:
		m.buildByDate(ts)
	case ByDay:
		m.buildByDay(ts)
	case ByLabel:
		m.buildByLabel(ts)
	default:
		m.buildByProject(ts)
	}
	m.cursor = -1
	if keepID != "" {
		m.Select(keepID)
	}
	if m.cursor < 0 {
		m.Top()
	}
}

func (m *Model) header(name string, tasks []*model.Task) string {
	done := 0
	for _, t := range tasks {
		if t.Checked {
			done++
		}
	}
	return m.th.Header.Render(name) + m.th.Faint.Render(fmt.Sprintf(" [%d/%d]", done, len(tasks)))
}

func sortTasks(ts []*model.Task) {
	sort.SliceStable(ts, func(i, j int) bool {
		if ts[i].Checked != ts[j].Checked {
			return !ts[i].Checked
		}
		return ts[i].Sort < ts[j].Sort
	})
}

func (m *Model) buildByProject(tasks []*model.Task) {
	byProject := map[string][]*model.Task{}
	for _, t := range tasks {
		byProject[t.ProjectID] = append(byProject[t.ProjectID], t)
	}
	for _, p := range m.data.Projects {
		all := byProject[p.ID]
		children := map[string][]*model.Task{}
		var top []*model.Task
		ids := map[string]bool{}
		for _, t := range all {
			ids[t.ID] = true
		}
		for _, t := range all {
			if t.ParentID != "" && ids[t.ParentID] {
				children[t.ParentID] = append(children[t.ParentID], t)
			} else {
				top = append(top, t)
			}
		}
		sortTasks(top)
		var rows []row
		var addTree func(t *model.Task, depth int)
		addTree = func(t *model.Task, depth int) {
			kids := children[t.ID]
			sortTasks(kids)
			var sub []row
			for _, k := range kids {
				before := len(rows)
				addTree(k, depth+1)
				sub = append(sub, rows[before:]...)
				rows = rows[:before]
			}
			// A parent stays visible while any visible child does, to keep context.
			if m.visible(t) || len(sub) > 0 {
				rows = append(rows, row{task: t, depth: depth})
				rows = append(rows, sub...)
			}
		}
		// Group top-level tasks by section, unsectioned first.
		sections := []model.Section{{}}
		for _, s := range m.data.Sections {
			if s.ProjectID == p.ID {
				sections = append(sections, s)
			}
		}
		for _, s := range sections {
			before := len(rows)
			for _, t := range top {
				if t.SectionID == s.ID || (s.ID == "" && !m.sectionKnown(t.SectionID)) {
					addTree(t, 0)
				}
			}
			if s.ID != "" && len(rows) > before {
				hdr := row{header: m.th.Faint.Render("— " + s.Name), section: true}
				rows = append(rows[:before], append([]row{hdr}, rows[before:]...)...)
			}
		}
		if len(rows) == 0 {
			continue
		}
		m.rows = append(m.rows, row{header: m.header("@"+p.Name, all)})
		m.rows = append(m.rows, rows...)
	}
}

func (m *Model) sectionKnown(id string) bool {
	if id == "" {
		return false
	}
	for _, s := range m.data.Sections {
		if s.ID == id {
			return true
		}
	}
	return false
}

func (m *Model) buildByDate(tasks []*model.Task) {
	td := today()
	names := []string{"Overdue", "Today", "Tomorrow", "Next 7 days", "Later", "No date"}
	groups := make([][]*model.Task, len(names))
	for _, t := range tasks {
		d, _, ok := t.Due.Time()
		g := 5
		if ok {
			day := time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, time.Local)
			switch {
			case day.Before(td):
				g = 0
			case day.Equal(td):
				g = 1
			case day.Equal(td.AddDate(0, 0, 1)):
				g = 2
			case day.Before(td.AddDate(0, 0, 8)):
				g = 3
			default:
				g = 4
			}
		}
		groups[g] = append(groups[g], t)
	}
	for i, ts := range groups {
		sortByDue(ts)
		m.addFlatGroup(names[i], ts)
	}
}

func sortByDue(ts []*model.Task) {
	sort.SliceStable(ts, func(a, b int) bool {
		if ts[a].Checked != ts[b].Checked {
			return !ts[a].Checked
		}
		da, _, _ := ts[a].Due.Time()
		db, _, _ := ts[b].Due.Time()
		if !da.Equal(db) {
			return da.Before(db)
		}
		return ts[a].Priority > ts[b].Priority
	})
}

// buildByDay makes one group per due date, like Todoist's Upcoming; overdue
// tasks share one group at the top and undated ones go last.
func (m *Model) buildByDay(tasks []*model.Task) {
	td := today()
	var overdue, undated []*model.Task
	byDay := map[time.Time][]*model.Task{}
	var days []time.Time
	for _, t := range tasks {
		d, _, ok := t.Due.Time()
		switch {
		case !ok:
			undated = append(undated, t)
		case dayOf(d).Before(td):
			overdue = append(overdue, t)
		default:
			day := dayOf(d)
			if _, seen := byDay[day]; !seen {
				days = append(days, day)
			}
			byDay[day] = append(byDay[day], t)
		}
	}
	sort.Slice(days, func(i, j int) bool { return days[i].Before(days[j]) })
	sortByDue(overdue)
	m.addFlatGroup("Overdue", overdue)
	for _, day := range days {
		name := day.Format("Mon 2 Jan")
		if day.Year() != td.Year() {
			name = day.Format("Mon 2 Jan 2006")
		}
		switch {
		case day.Equal(td):
			name = "Today · " + name
		case day.Equal(td.AddDate(0, 0, 1)):
			name = "Tomorrow · " + name
		}
		sortByDue(byDay[day])
		m.addFlatGroup(name, byDay[day])
	}
	sortByDue(undated)
	m.addFlatGroup("No date", undated)
}

func (m *Model) buildByLabel(tasks []*model.Task) {
	byLabel := map[string][]*model.Task{}
	var names []string
	var none []*model.Task
	for _, t := range tasks {
		if len(t.Labels) == 0 {
			none = append(none, t)
		}
		for _, l := range t.Labels {
			if _, ok := byLabel[l]; !ok {
				names = append(names, l)
			}
			byLabel[l] = append(byLabel[l], t)
		}
	}
	sort.Strings(names)
	for _, n := range names {
		sortTasks(byLabel[n])
		m.addFlatGroup("@"+n, byLabel[n])
	}
	sortTasks(none)
	m.addFlatGroup("No label", none)
}

func (m *Model) addFlatGroup(name string, ts []*model.Task) {
	var rows []row
	for _, t := range ts {
		if m.visible(t) {
			rows = append(rows, row{task: t})
		}
	}
	if len(rows) == 0 {
		return
	}
	m.rows = append(m.rows, row{header: m.header(name, ts)})
	m.rows = append(m.rows, rows...)
}

// --- rendering -----------------------------------------------------------

func (m *Model) Title() string {
	name := m.view.String()
	if m.view == Today && !m.onToday() {
		name = m.dayLabel()
	}
	return "Tasks · " + name + " · by " + m.groupBy[m.view].String()
}

func (m *Model) Render(w, h int, active bool) []string {
	th := m.th
	footer := m.footer()
	bodyH := h - len(footer) - 1
	var lines []string
	m.lineRows = m.lineRows[:0]
	if len(m.rows) == 0 {
		empty := map[View]string{Today: "Nothing due " + m.dayOn(), Upcoming: "No upcoming tasks", All: "No tasks"}[m.view]
		lines = append(lines, th.Faint.Render("  "+empty+" — press a to add one"))
	} else {
		// Blank line before each top-level group header except the first.
		type line struct {
			text string
			row  int
		}
		var all []line
		for i, r := range m.rows {
			if r.header != "" && !r.section && i > 0 {
				all = append(all, line{"", -1})
			}
			if r.header != "" {
				ind := " "
				if r.section {
					ind = "   "
				}
				all = append(all, line{ind + r.header, i})
				continue
			}
			all = append(all, line{m.taskLine(r, w, i == m.cursor, active), i})
		}
		cur := 0
		for i, l := range all {
			if l.row == m.cursor {
				cur = i
			}
		}
		// Keep the group header in view when the cursor is on its first task.
		anchor := cur
		if cur > 0 && all[cur-1].row >= 0 && m.rows[all[cur-1].row].header != "" {
			anchor = cur - 1
		}
		m.scroll = scrollFor(cur, min(m.scroll, anchor), bodyH, len(all))
		for i := m.scroll; i < len(all) && len(lines) < bodyH; i++ {
			lines = append(lines, all[i].text)
			m.lineRows = append(m.lineRows, all[i].row)
		}
	}
	for len(lines) < bodyH {
		lines = append(lines, "")
	}
	lines = append(lines, "")
	return append(lines, footer...)
}

func (m *Model) taskLine(r row, w int, selected, active bool) string {
	th := m.th
	t := r.task
	indent := strings.Repeat("  ", r.depth+1) + " "
	icon := th.PendingIcon.Render("☐")
	if t.Checked {
		icon = th.DoneIcon.Render("✔")
	}
	title := t.Content
	titleStyle := lipgloss.NewStyle()
	switch t.Priority.Display() {
	case 1:
		titleStyle = th.P1
	case 2:
		titleStyle = th.P2
	case 3:
		titleStyle = th.P3
	}
	if t.Checked {
		titleStyle = th.Done
	}

	var meta []string
	if p := t.Priority.Display(); p < 4 && !t.Checked {
		meta = append(meta, titleStyle.Render(fmt.Sprintf("p%d", p)))
	}
	if t.Due != nil {
		meta = append(meta, m.dueLabel(t))
	}
	if t.Deadline != "" {
		meta = append(meta, th.Overdue.Render("⚑ "+t.Deadline))
	}
	for _, l := range t.Labels {
		meta = append(meta, th.Faint.Render("@"+l))
	}
	if t.Description != "" {
		meta = append(meta, th.Faint.Render("≡"))
	}
	if t.Dirty {
		meta = append(meta, th.Faint.Render("●"))
	}
	metaStr := ""
	if len(meta) > 0 {
		metaStr = "  " + strings.Join(meta, " ")
	}

	if selected {
		plain := indent + iconText(t) + " " + title
		sel := th.Selected
		if !active {
			sel = th.SelectedDim
		}
		return sel.Render(theme.Fit(plain+ansi.Strip(metaStr), w))
	}
	return indent + icon + " " + titleStyle.Render(title) + metaStr
}

func iconText(t *model.Task) string {
	if t.Checked {
		return "✔"
	}
	return "☐"
}

func (m *Model) dueLabel(t *model.Task) string {
	th := m.th
	d, hasTime, ok := t.Due.Time()
	text := t.Due.String
	if ok {
		td := today()
		day := time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, time.Local)
		switch {
		case day.Equal(td):
			text = "today"
		case day.Equal(td.AddDate(0, 0, 1)):
			text = "tomorrow"
		case day.Equal(td.AddDate(0, 0, -1)):
			text = "yesterday"
		case day.After(td) && day.Before(td.AddDate(0, 0, 7)):
			text = d.Format("Mon")
		case d.Year() == td.Year():
			text = d.Format("2 Jan")
		default:
			text = d.Format("2 Jan 2006")
		}
		if hasTime {
			text += " " + d.Format("15:04")
		}
		if t.Due.IsRecurring {
			text += " ↻"
		}
		switch {
		case t.Checked:
			return th.Faint.Render("⏲ " + text)
		case day.Before(td):
			return th.Overdue.Render("⏲ " + text)
		case day.Equal(td):
			return th.DueToday.Render("⏲ " + text)
		}
		return th.Faint.Render("⏲ " + text)
	}
	if text == "" {
		text = editor.DueText(t.Due)
	}
	return th.Faint.Render("⏲ " + text)
}

func (m *Model) footer() []string {
	th := m.th
	done, pending := 0, 0
	for _, t := range m.viewTasks() {
		if t.Checked {
			done++
		} else {
			pending++
		}
	}
	total := done + pending
	pct := 0
	if total > 0 {
		pct = done * 100 / total
	}
	return []string{
		th.Faint.Render(fmt.Sprintf("  %d%% of %s complete.", pct,
			map[View]string{Today: "tasks due " + m.dayOn(), Upcoming: "upcoming tasks", All: "all tasks"}[m.view])),
		"  " + th.DoneIcon.Render(fmt.Sprintf("%d", done)) + th.Faint.Render(" done · ") +
			th.PendingIcon.Render(fmt.Sprintf("%d", pending)) + th.Faint.Render(" pending"),
	}
}

// dayOn is "today" or "on Tue 27 Oct".
func (m *Model) dayOn() string {
	if m.onToday() {
		return "today"
	}
	return "on " + m.dayLabel()
}

func today() time.Time {
	n := time.Now()
	return time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, time.Local)
}

func scrollFor(sel, scroll, h, n int) int {
	if h <= 0 {
		return 0
	}
	if sel < scroll {
		scroll = sel
	}
	if sel >= scroll+h {
		scroll = sel - h + 1
	}
	if scroll > n-h {
		scroll = max(0, n-h)
	}
	return max(0, scroll)
}
