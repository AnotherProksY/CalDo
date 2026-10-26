// Package calendar is the calendar pane: month grid + day timeline, 3-day, week and agenda views,
// navigated with a cursor over days and individual events.
package calendar

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"caldo/internal/model"
	"caldo/internal/ui/theme"
)

type View int

// Views in the order of their keys (1–4).
const (
	Month View = iota
	Days
	Week
	Agenda
)

// daysShown is how many days the Days view shows side by side.
const daysShown = 3

func ParseView(s string) View {
	switch s {
	case "days", "3days":
		return Days
	case "week":
		return Week
	case "agenda":
		return Agenda
	}
	return Month
}

func (v View) String() string {
	return [...]string{"Month", "3 days", "Week", "Agenda"}[v]
}

// Source provides occurrences and calendar colors.
type Source interface {
	Occurrences(from, to time.Time) []model.Occurrence
	CalendarColor(calendarID string) string
}

type Model struct {
	src        Source
	th         *theme.Theme
	view       View
	cursor     time.Time // selected day (midnight, local)
	listFocus  bool      // month view: true when the day list has focus
	sel        int       // selected occurrence index within the current list
	scroll     int
	dayScroll  int       // month and days views: first visible timeline row
	daysStart  time.Time // days view: first day shown
	weekStart  time.Weekday
	agendaDays int
	timeFmt    string
	filter     string

	// hits maps content cells to days and items; rebuilt by every Render.
	hits []hitRect

	// DimPast greys out events that have already ended.
	DimPast bool
	now     func() time.Time

	// cache of occurrences for the visible range
	cacheFrom, cacheTo time.Time
	cache              []model.Occurrence
	cacheValid         bool

	w, h int
}

func New(src Source, th *theme.Theme, view View, weekStart time.Weekday, agendaDays int, timeFmt string) Model {
	return Model{
		src: src, th: th, view: view, cursor: today(),
		weekStart: weekStart, agendaDays: agendaDays, timeFmt: timeFmt,
	}
}

func today() time.Time {
	n := time.Now()
	return time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, time.Local)
}

func (m *Model) SetSize(w, h int) { m.w, m.h = w, h }

// isPast reports an event that has ended. Tasks never count: an overdue task
// still needs doing.
func (m *Model) isPast(o model.Occurrence) bool {
	if !m.DimPast || o.Task != nil {
		return false
	}
	return !o.End.After(m.clock())
}

func (m *Model) clock() time.Time {
	if m.now != nil {
		return m.now()
	}
	return time.Now()
}

// SelectNow puts the list cursor on the item that is on now or comes next,
// skipping today's all-day items; if everything today is over, on the last.
func (m *Model) SelectNow() {
	m.sel, m.scroll, m.dayScroll = 0, 0, 0
	if !m.cursor.Equal(today()) {
		return
	}
	now := m.clock()
	last := -1
	for i, o := range m.currentList() {
		if o.Event.AllDay && !o.Start.After(now) {
			continue
		}
		if o.End.After(now) || (o.End.Equal(o.Start) && !o.Start.Before(now)) {
			m.sel = i
			return
		}
		last = i
	}
	if last >= 0 {
		m.sel = last
	}
}
func (m *Model) Invalidate()        { m.cacheValid = false }
func (m *Model) SetView(v View)     { m.view = v; m.daysStart = m.cursor; m.SelectNow() }
func (m Model) CurrentView() View   { return m.view }
func (m Model) Cursor() time.Time   { return m.cursor }
func (m *Model) SetFilter(f string) { m.filter = strings.ToLower(f); m.Invalidate(); m.clampSel() }
func (m Model) ListFocused() bool   { return m.view != Month || m.listFocus }

// --- data ----------------------------------------------------------------

func (m *Model) visibleRange() (time.Time, time.Time) {
	switch m.view {
	case Days:
		s := m.daysWindow()
		return s, s.AddDate(0, 0, daysShown)
	case Week:
		s := m.weekOf(m.cursor)
		return s, s.AddDate(0, 0, 7)
	case Agenda:
		return m.cursor, m.cursor.AddDate(0, 0, m.agendaDays)
	}
	first := time.Date(m.cursor.Year(), m.cursor.Month(), 1, 0, 0, 0, 0, time.Local)
	s := m.weekOf(first)
	return s, s.AddDate(0, 0, 42)
}

func (m *Model) occurrences() []model.Occurrence {
	from, to := m.visibleRange()
	if !m.cacheValid || !from.Equal(m.cacheFrom) || !to.Equal(m.cacheTo) {
		all := m.src.Occurrences(from, to)
		if m.filter != "" {
			kept := all[:0]
			for _, o := range all {
				if strings.Contains(strings.ToLower(o.Event.Title), m.filter) ||
					strings.Contains(strings.ToLower(o.Event.Location), m.filter) {
					kept = append(kept, o)
				}
			}
			all = kept
		}
		m.cache, m.cacheFrom, m.cacheTo, m.cacheValid = all, from, to, true
	}
	return m.cache
}

func (m *Model) dayOccurrences(day time.Time) []model.Occurrence {
	end := day.AddDate(0, 0, 1)
	var out []model.Occurrence
	for _, o := range m.occurrences() {
		if o.Start.Before(end) && (o.End.After(day) || (o.End.Equal(o.Start) && !o.Start.Before(day))) {
			out = append(out, o)
		}
	}
	return out
}

// currentList is the list the event cursor moves over in the active view.
func (m *Model) currentList() []model.Occurrence {
	if m.view == Agenda {
		return m.agendaList()
	}
	return m.dayOccurrences(m.cursor)
}

func (m *Model) agendaList() []model.Occurrence {
	var out []model.Occurrence
	for d := 0; d < m.agendaDays; d++ {
		out = append(out, m.dayOccurrences(m.cursor.AddDate(0, 0, d))...)
	}
	return out
}

// Selected returns the occurrence under the cursor, if the event list is focused.
func (m *Model) Selected() (model.Occurrence, bool) {
	if !m.ListFocused() {
		return model.Occurrence{}, false
	}
	l := m.currentList()
	if m.sel < 0 || m.sel >= len(l) {
		return model.Occurrence{}, false
	}
	return l[m.sel], true
}

// SelectedDay is the day new events are created on.
func (m *Model) SelectedDay() time.Time {
	if m.view == Agenda {
		if o, ok := m.Selected(); ok {
			return time.Date(o.Start.Year(), o.Start.Month(), o.Start.Day(), 0, 0, 0, 0, time.Local)
		}
	}
	return m.cursor
}

func (m *Model) clampSel() {
	n := len(m.currentList())
	if m.sel >= n {
		m.sel = n - 1
	}
	if m.sel < 0 {
		m.sel = 0
	}
}

// daysWindow is the first day of the Days view, shifted just enough to keep
// the cursor in view.
func (m *Model) daysWindow() time.Time {
	switch {
	case m.daysStart.IsZero() || m.cursor.Before(m.daysStart):
		m.daysStart = m.cursor
	case !m.cursor.Before(m.daysStart.AddDate(0, 0, daysShown)):
		m.daysStart = m.cursor.AddDate(0, 0, 1-daysShown)
	}
	return m.daysStart
}

func (m *Model) weekOf(t time.Time) time.Time {
	off := (int(t.Weekday()) - int(m.weekStart) + 7) % 7
	return time.Date(t.Year(), t.Month(), t.Day()-off, 0, 0, 0, 0, time.Local)
}

// --- navigation ----------------------------------------------------------

func (m *Model) moveDays(n int) {
	m.cursor = m.cursor.AddDate(0, 0, n)
	m.SelectNow()
}

func (m *Model) Left() {
	m.moveDays(-1)
}

func (m *Model) Right() {
	m.moveDays(1)
}

func (m *Model) Down() {
	if m.ListFocused() {
		if m.sel < len(m.currentList())-1 {
			m.sel++
		}
		return
	}
	m.moveDays(7)
}

func (m *Model) Up() {
	if m.ListFocused() {
		if m.sel > 0 {
			m.sel--
		}
		return
	}
	m.moveDays(-7)
}

func (m *Model) Top()    { m.sel = 0 }
func (m *Model) Bottom() { m.sel = max(0, len(m.currentList())-1) }

func (m *Model) Today() {
	m.cursor = today()
	m.daysStart = m.cursor
	m.SelectNow()
}

func (m *Model) Period(dir int) {
	switch m.view {
	case Days:
		m.daysStart = m.daysWindow().AddDate(0, 0, daysShown*dir)
		m.moveDays(daysShown * dir)
	case Week:
		m.moveDays(7 * dir)
	case Agenda:
		m.moveDays(m.agendaDays * dir)
	default:
		m.cursor = m.cursor.AddDate(0, dir, 0)
		m.SelectNow()
	}
}

// ToggleSubFocus switches between the month grid and the day list.
func (m *Model) ToggleSubFocus() {
	if m.view == Month {
		m.listFocus = !m.listFocus
		m.clampSel()
	}
}

func (m *Model) FocusList() {
	if m.view == Month {
		m.listFocus = true
		m.clampSel()
	}
}

// SelectEvent moves the cursor to an occurrence of event id on or after day.
func (m *Model) SelectEvent(id string, day time.Time) {
	if m.view != Agenda {
		m.cursor = time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, time.Local)
	}
	m.listFocus = true
	for i, o := range m.currentList() {
		if o.Event.ID == id {
			m.sel = i
			return
		}
	}
	m.clampSel()
}

// --- rendering -----------------------------------------------------------

func (m *Model) Title() string {
	switch m.view {
	case Days:
		s := m.daysWindow()
		e := s.AddDate(0, 0, daysShown-1)
		return fmt.Sprintf("3 days · %s – %s", s.Format("2 Jan"), e.Format("2 Jan 2006"))
	case Week:
		s := m.weekOf(m.cursor)
		e := s.AddDate(0, 0, 6)
		return fmt.Sprintf("Week · %s – %s", s.Format("2 Jan"), e.Format("2 Jan 2006"))
	case Agenda:
		return fmt.Sprintf("Agenda · %d days from %s", m.agendaDays, m.cursor.Format("2 Jan"))
	}
	return "Calendar · " + m.cursor.Format("January 2006")
}

// HitKind says what a content cell shows.
type HitKind int

const (
	HitDay  HitKind = iota + 1 // a day in the month grid or a week column header
	HitItem                    // an event or task in a list
)

// Hit is what lies under a content cell.
type Hit struct {
	Kind  HitKind
	Day   time.Time
	Index int // HitItem: index into the list the cursor moves over
}

type hitRect struct {
	row, x0, x1 int
	hit         Hit
}

func (m *Model) addHit(row, x0, x1 int, h Hit) {
	m.hits = append(m.hits, hitRect{row, x0, x1, h})
}

// HitTest maps a cell of the last rendered content (0-based) to its target.
func (m *Model) HitTest(x, y int) (Hit, bool) {
	for _, r := range m.hits {
		if r.row == y && x >= r.x0 && x < r.x1 {
			return r.hit, true
		}
	}
	return Hit{}, false
}

// Click moves the cursor to a hit: a day selects it, an item selects the item.
func (m *Model) Click(h Hit) {
	switch h.Kind {
	case HitDay:
		m.cursor, m.listFocus = h.Day, false
		m.SelectNow()
	case HitItem:
		if m.view != Agenda {
			m.cursor = h.Day
		}
		m.sel, m.listFocus = h.Index, true
	}
	m.clampSel()
}

// Render returns the pane content lines for an inner area of w×h.
func (m *Model) Render(w, h int, active bool) []string {
	m.clampSel()
	m.hits = m.hits[:0]
	switch m.view {
	case Days:
		return m.renderDays(w, h, active)
	case Week:
		return m.renderWeek(w, h, active)
	case Agenda:
		return m.renderAgenda(w, h, active)
	}
	return m.renderMonth(w, h, active)
}

func (m *Model) weekdayNames() []string {
	names := make([]string, 7)
	for i := range 7 {
		names[i] = time.Weekday((int(m.weekStart) + i) % 7).String()[:2]
	}
	return names
}

func (m *Model) renderMonth(w, h int, active bool) []string {
	th := m.th
	cw := max(3, min(w/7, 8))
	gridW := cw * 7
	pad := strings.Repeat(" ", max(0, (w-gridW)/2))

	var lines []string
	hdr := ""
	for i, n := range m.weekdayNames() {
		cell := fmt.Sprintf("%*s", cw-1, n) + " "
		wd := time.Weekday((int(m.weekStart) + i) % 7)
		if wd == time.Saturday || wd == time.Sunday {
			cell = th.Faint.Render(cell)
		} else {
			cell = th.Bold.Render(cell)
		}
		hdr += cell
	}
	lines = append(lines, pad+hdr)

	first := time.Date(m.cursor.Year(), m.cursor.Month(), 1, 0, 0, 0, 0, time.Local)
	start := m.weekOf(first)
	busy := map[string]bool{}
	for _, o := range m.occurrences() {
		first := time.Date(o.Start.Year(), o.Start.Month(), o.Start.Day(), 0, 0, 0, 0, time.Local)
		busy[first.Format(time.DateOnly)] = true
		for d := first.AddDate(0, 0, 1); d.Before(o.End); d = d.AddDate(0, 0, 1) {
			busy[d.Format(time.DateOnly)] = true
		}
	}
	td := today()
	// Leave a blank line between week rows when there is plenty of room.
	spacious := h >= 6*2+1+8
	for wk := 0; wk < 6; wk++ {
		row := ""
		for d := 0; d < 7; d++ {
			day := start.AddDate(0, 0, wk*7+d)
			m.addHit(len(lines), len(pad)+d*cw, len(pad)+(d+1)*cw, Hit{Kind: HitDay, Day: day})
			mark := " "
			if busy[day.Format(time.DateOnly)] {
				mark = "•"
			}
			num := fmt.Sprintf("%*d", cw-1, day.Day())
			st := th.Bold
			if day.Month() != m.cursor.Month() {
				st = th.Faint
			} else if day.Weekday() == time.Saturday || day.Weekday() == time.Sunday {
				st = th.Faint.Bold(true)
			}
			if day.Equal(td) {
				st = th.TodayStyle
			}
			if day.Equal(m.cursor) {
				// A pill around the number and its mark: solid while the grid
				// has focus, a tint of the accent otherwise, so it never fades out.
				digits := fmt.Sprint(day.Day())
				hl := lipgloss.NewStyle().Background(th.TintColor(th.Accent, 0.35)).Bold(true)
				if active && !m.listFocus {
					hl = th.Selected.Foreground(th.Accent)
				}
				if day.Equal(td) {
					hl = hl.Foreground(th.Today)
				}
				row += strings.Repeat(" ", max(0, cw-2-len(digits))) + hl.Render(" "+digits+mark)
				continue
			}
			row += st.Render(num) + th.Fg("").Render(mark)
		}
		lines = append(lines, pad+row)
		if spacious && wk < 5 {
			lines = append(lines, "")
		}
	}

	lines = append(lines, "")
	label := m.cursor.Format("Monday, 2 January")
	if m.cursor.Equal(td) {
		label += " · today"
	}
	lines = append(lines, th.Rule(label, w, active && m.listFocus))
	listH := h - len(lines)
	lines = append(lines, m.renderDay(m.dayOccurrences(m.cursor), w, listH, active && m.listFocus, len(lines))...)
	return lines
}

// eventLine renders one occurrence row: colored bar, time, title, markers.
func (m *Model) eventLine(o model.Occurrence, day time.Time, w int, selected, active bool) string {
	th := m.th
	bar := th.Fg(m.src.CalendarColor(o.Event.CalendarID)).Render("▌")
	var when string
	switch {
	case o.Event.AllDay:
		when = "all day    "
	case o.End.Equal(o.Start):
		when = o.Start.Format(m.timeFmt) // a task due at a time
	case !sameDay(o.Start, day):
		when = "… " + o.End.Format(m.timeFmt) + "    "
	case !sameDay(o.End, day) && o.End.After(day.AddDate(0, 0, 1)):
		when = o.Start.Format(m.timeFmt) + " …    "
	default:
		when = o.Start.Format(m.timeFmt) + "–" + o.End.Format(m.timeFmt)
	}
	when = theme.Fit(when, 11)
	title := o.Event.Title
	if title == "" {
		title = "(no title)"
	}
	var marks string
	if o.IsRecurring() || (o.Task != nil && o.Task.Due.IsRecurring) {
		marks += " ↻"
	}
	if o.Event.Dirty {
		marks += " ●"
	}
	if o.Event.Location != "" {
		marks += " @ " + o.Event.Location
	}
	text := " " + when + " " + title
	plainText := text
	if o.Task != nil {
		plainText = " " + when + " ☐ " + title
		text = " " + when + " " + th.Faint.Render("☐ ") + m.priorityStyle(o.Task.Priority).Render(title)
	}
	content := theme.Fit(text+th.Faint.Render(marks), w-1)
	if m.isPast(o) {
		bar = th.Fg(m.src.CalendarColor(o.Event.CalendarID)).Faint(true).Render("▌")
		content = th.Faint.Render(theme.Fit(plainText+marks, w-1))
	}
	if selected {
		if active {
			content = th.Selected.Render(theme.Fit(plainText+marks, w-1))
		} else {
			content = th.SelectedDim.Render(theme.Fit(plainText+marks, w-1))
		}
	}
	return bar + content
}

// priorityStyle colors task titles in the calendar like the task list does.
func (m *Model) priorityStyle(p model.Priority) lipgloss.Style {
	switch p.Display() {
	case 1:
		return m.th.P1
	case 2:
		return m.th.P2
	case 3:
		return m.th.P3
	}
	return lipgloss.NewStyle()
}

// renderEventList draws the day list starting at content row top.
func (m *Model) renderEventList(list []model.Occurrence, w, h int, active bool, top int) []string {
	if h <= 0 {
		return nil
	}
	if len(list) == 0 {
		return []string{m.th.Faint.Render("  No events — press a to add")}
	}
	m.scroll = scrollFor(m.sel, m.scroll, h, len(list))
	var out []string
	for i := m.scroll; i < len(list) && len(out) < h; i++ {
		m.addHit(top+len(out), 0, w, Hit{Kind: HitItem, Day: m.cursor, Index: i})
		out = append(out, m.eventLine(list[i], m.cursor, w, i == m.sel && m.ListFocused(), active))
	}
	return out
}

func (m *Model) renderWeek(w, h int, active bool) []string {
	th := m.th
	start := m.weekOf(m.cursor)
	colW := max(6, (w-6)/7)
	td := today()
	var lines []string

	cols := make([][]string, 7)
	for d := range 7 {
		day := start.AddDate(0, 0, d)
		hdr := day.Format("Mon 2")
		st := th.Bold
		if day.Equal(td) {
			st = th.TodayStyle
		}
		hdr = theme.Fit(hdr, colW)
		if day.Equal(m.cursor) {
			if active {
				hdr = th.Selected.Inherit(st).Render(hdr)
			} else {
				hdr = th.SelectedDim.Inherit(st).Render(hdr)
			}
		} else {
			hdr = st.Render(hdr)
		}
		col := []string{hdr, th.BorderStyle.Render(strings.Repeat("─", colW))}
		x0 := d * (colW + 1)
		m.addHit(0, x0, x0+colW, Hit{Kind: HitDay, Day: day})
		occ := m.dayOccurrences(day)
		// Each event takes two lines (time, title) so narrow columns stay readable.
		for i, o := range occ {
			when := "all day"
			if !o.Event.AllDay {
				when = o.Start.Format(m.timeFmt)
				if !sameDay(o.Start, day) {
					when = "… " + o.End.Format(m.timeFmt)
				}
			}
			title := o.Event.Title
			if o.Task != nil {
				when += " ☐"
				title = m.priorityStyle(o.Task.Priority).Render(title)
			}
			if o.IsRecurring() || (o.Task != nil && o.Task.Due.IsRecurring) {
				when += " ↻"
			}
			bar := th.Fg(m.src.CalendarColor(o.Event.CalendarID)).Render("▌")
			l1, l2 := theme.Fit(when, colW-1), theme.Fit(title, colW-1)
			if day.Equal(m.cursor) && i == m.sel {
				sel := th.SelectedDim
				if active {
					sel = th.Selected
				}
				l1, l2 = sel.Render(ansi.Strip(l1)), sel.Render(ansi.Strip(l2))
			} else {
				l1 = th.Faint.Render(l1)
				if m.isPast(o) {
					bar = th.Fg(m.src.CalendarColor(o.Event.CalendarID)).Faint(true).Render("▌")
					l2 = th.Faint.Render(ansi.Strip(l2))
				}
			}
			for _, r := range []int{len(col), len(col) + 1} {
				if r < h {
					m.addHit(r, x0, x0+colW, Hit{Kind: HitItem, Day: day, Index: i})
				}
			}
			col = append(col, bar+l1, bar+l2)
		}
		cols[d] = col
	}
	sep := th.BorderStyle.Render("│")
	for r := 0; r < h; r++ {
		var row strings.Builder
		for d := range 7 {
			cell := ""
			if r < len(cols[d]) {
				cell = cols[d][r]
			}
			row.WriteString(theme.Fit(cell, colW))
			if d < 6 {
				row.WriteString(sep)
			}
		}
		lines = append(lines, row.String())
	}
	return lines
}

func (m *Model) renderAgenda(w, h int, active bool) []string {
	th := m.th
	td := today()
	type row struct {
		text string
		idx  int // occurrence index, -1 for headers
	}
	var rows []row
	idx := 0
	for d := 0; d < m.agendaDays; d++ {
		day := m.cursor.AddDate(0, 0, d)
		occ := m.dayOccurrences(day)
		if len(occ) == 0 {
			continue
		}
		label := day.Format("Monday, 2 January")
		switch {
		case day.Equal(td):
			label = th.TodayStyle.Render(label + " · today")
		case day.Equal(td.AddDate(0, 0, 1)):
			label = th.Header.Render(label) + th.Faint.Render(" · tomorrow")
		default:
			label = th.Header.Render(label)
		}
		if len(rows) > 0 {
			rows = append(rows, row{"", -1})
		}
		rows = append(rows, row{label, -1})
		for _, o := range occ {
			rows = append(rows, row{m.eventLine(o, day, w-2, idx == m.sel, active), idx})
			idx++
		}
	}
	if len(rows) == 0 {
		return []string{th.Faint.Render(fmt.Sprintf("  Nothing in the next %d days", m.agendaDays))}
	}
	selRow := 0
	for i, r := range rows {
		if r.idx == m.sel {
			selRow = i
		}
	}
	m.scroll = scrollFor(selRow, m.scroll, h, len(rows))
	var out []string
	for i := m.scroll; i < len(rows) && len(out) < h; i++ {
		if rows[i].idx >= 0 {
			m.addHit(len(out), 0, w, Hit{Kind: HitItem, Index: rows[i].idx})
			out = append(out, "  "+rows[i].text)
		} else {
			out = append(out, rows[i].text)
		}
	}
	return out
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

func sameDay(a, b time.Time) bool {
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	return ay == by && am == bm && ad == bd
}
