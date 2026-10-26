package calendar

import (
	"fmt"
	"image/color"
	"sort"
	"strings"
	"time"

	"charm.land/lipgloss/v2"

	"caldo/internal/model"
	"caldo/internal/ui/theme"
)

// The day timeline under the month grid, in the style of Apple Calendar's
// day view: all-day items on top, then timed events as tinted blocks whose
// height follows their duration. Overlapping events share the width in
// columns, and free time between events collapses into a single row.

const (
	slotMinutes = 15 // one row of the timeline
	minSlots    = 2  // a block is at least 30 minutes tall: time and title
	daySlots    = 24 * 60 / slotMinutes
	gutterW     = 8 // "  07:00 " or "all-day "
	minDayViewW = 40
)

// Block background opacity over the terminal background.
const (
	alphaBlock    = 0.28
	alphaPast     = 0.12
	alphaSelected = 0.55
)

type dayBlock struct {
	idx        int // index into the day list
	o          model.Occurrence
	from, to   int // slots since midnight; to is exclusive
	col, ncols int
}

type itemHit struct {
	x0, x1, idx int
	day         time.Time
}

type dayLine struct {
	text string
	hits []itemHit
}

// dayCol is one day of a timeline: its items and its width in cells.
type dayCol struct {
	day  time.Time
	list []model.Occurrence
	w    int
}

// renderDays shows daysShown days as timeline columns under a header row.
func (m *Model) renderDays(w, h int, active bool) []string {
	th := m.th
	start := m.daysWindow()
	sep := th.BorderStyle.Render("│")
	cw := (w - gutterW - (daysShown - 1)) / daysShown
	td := today()
	hdr := strings.Repeat(" ", gutterW)
	rule := th.BorderStyle.Render(strings.Repeat("─", gutterW))
	cols := make([]dayCol, daysShown)
	for d := range cols {
		day := start.AddDate(0, 0, d)
		cols[d] = dayCol{day, m.dayOccurrences(day), cw}
	}
	cols[daysShown-1].w = w - gutterW - (daysShown - 1) - cw*(daysShown-1) // the remainder
	x := gutterW
	for d, col := range cols {
		label := " " + col.day.Format("Mon 2")
		st := th.Bold
		if col.day.Equal(td) {
			label += " · today"
			st = th.TodayStyle
		}
		if col.day.Equal(m.cursor) {
			alpha := 0.25
			if active {
				alpha = 0.5
			}
			st = st.Background(th.TintColor(th.Accent, alpha)).Bold(true)
		}
		hdr += st.Render(theme.Fit(label, col.w))
		rule += th.BorderStyle.Render(strings.Repeat("─", col.w))
		if d < daysShown-1 {
			hdr += sep
			rule += th.BorderStyle.Render("┼")
		}
		m.addHit(0, x, x+col.w, Hit{Kind: HitDay, Day: col.day})
		x += col.w + 1
	}
	if h < 3 {
		return []string{hdr, rule}[:max(0, h)]
	}
	lines, spans := m.timeline(cols, sep, active)
	out := append([]string{hdr, rule}, m.scrollTimeline(lines, spans, h-2, 2)...)
	// Carry the column separators down to the bottom of the pane.
	blank := strings.Repeat(" ", gutterW)
	for d, col := range cols {
		if d > 0 {
			blank += sep
		}
		blank += strings.Repeat(" ", col.w)
	}
	for len(out) < h {
		out = append(out, blank)
	}
	return out
}

// renderDay draws the timeline for list (the cursor day's items) starting
// at content row top. Narrow panes fall back to the one-line list.
func (m *Model) renderDay(list []model.Occurrence, w, h int, active bool, top int) []string {
	if h <= 0 {
		return nil
	}
	if len(list) == 0 || w < minDayViewW {
		return m.renderEventList(list, w, h, active, top)
	}
	lines, spans := m.timeline([]dayCol{{m.cursor, list, w - gutterW}}, "", active)
	return m.scrollTimeline(lines, spans, h, top)
}

// scrollTimeline shows h rows of lines with the selected item in view and
// registers their hit regions, starting at content row top.
func (m *Model) scrollTimeline(lines []dayLine, spans [][2]int, h, top int) []string {
	if m.sel >= 0 && m.sel < len(spans) {
		m.dayScroll = scrollToRange(spans[m.sel][0], spans[m.sel][1], m.dayScroll, h, len(lines))
	}
	m.dayScroll = max(0, min(m.dayScroll, len(lines)-h))
	var out []string
	for r := m.dayScroll; r < len(lines) && len(out) < h; r++ {
		for _, hh := range lines[r].hits {
			m.addHit(top+len(out), hh.x0, hh.x1, Hit{Kind: HitItem, Day: hh.day, Index: hh.idx})
		}
		out = append(out, lines[r].text)
	}
	return out
}

// scrollToRange keeps rows [r0, r1) on screen, showing the start of blocks
// taller than the view.
func scrollToRange(r0, r1, scroll, h, n int) int {
	switch {
	case r1-r0 >= h || r0 < scroll:
		scroll = r0
	case r1 > scroll+h:
		scroll = r1 - h
	}
	return max(0, min(scroll, n-h))
}

type tlRow struct {
	gutter string
	cells  []string // per column; "" is blank
	hits   []itemHit
}

// timeline lays out days side by side under one time gutter, columns joined
// by sep. Rows are shared, so the same time lines up across days, and free
// time collapses only where every day is free. It returns the rows and, for
// the cursor's day, the rows [from, to) each of its items covers.
func (m *Model) timeline(cols []dayCol, sep string, active bool) ([]dayLine, [][2]int) {
	multi := len(cols) > 1
	sepW := theme.StringWidth(sep)
	colX := make([]int, len(cols))
	x := gutterW
	var spans [][2]int
	cur := -1
	for c, col := range cols {
		colX[c] = x
		x += col.w + sepW
		if col.day.Equal(m.cursor) {
			cur = c
			spans = make([][2]int, len(col.list))
		}
	}
	selected := func(c, i int) bool { return c == cur && i == m.sel && m.ListFocused() }
	var rows []tlRow
	newRow := func(gutter string) int {
		rows = append(rows, tlRow{gutter: gutter, cells: make([]string, len(cols))})
		return len(rows) - 1
	}

	// All-day items (and timed ones covering the whole day) go on top.
	allDay := make([][]int, len(cols))
	blocks := make([][]dayBlock, len(cols))
	nAllDay := 0
	for c, col := range cols {
		day := col.day
		for i, o := range col.list {
			s, e := clipToDay(o, day)
			if o.Event.AllDay || (s.Equal(day) && e.Equal(day.AddDate(0, 0, 1))) {
				allDay[c] = append(allDay[c], i)
				continue
			}
			from := minutes(s, day) / slotMinutes
			to := (minutes(e, day) + slotMinutes - 1) / slotMinutes
			if e.Equal(day.AddDate(0, 0, 1)) {
				to = daySlots
			}
			to = max(to, from+minSlots)
			if to > daySlots {
				from, to = max(0, daySlots-minSlots), daySlots
			}
			blocks[c] = append(blocks[c], dayBlock{idx: i, o: o, from: from, to: to})
		}
		nAllDay = max(nAllDay, len(allDay[c]))
	}
	for r := range nAllDay {
		label := ""
		if r == 0 {
			label = "all-day"
		}
		row := newRow(m.gutter(label, false))
		for c, idxs := range allDay {
			if r >= len(idxs) {
				continue
			}
			i := idxs[r]
			o := cols[c].list[i]
			rows[row].cells[c] = m.blockLine(o, m.titleText(o), cols[c].w, rowTitle, selected(c, i), active)
			rows[row].hits = append(rows[row].hits, itemHit{colX[c], colX[c] + cols[c].w, i, cols[c].day})
			if c == cur {
				spans[i] = [2]int{row, row + 1}
			}
		}
	}

	busy := make([]bool, daySlots)
	lo, hi := daySlots, 0
	for c := range blocks {
		layoutColumns(blocks[c])
		for _, b := range blocks[c] {
			for s := b.from; s < b.to; s++ {
				busy[s] = true
			}
			lo, hi = min(lo, b.from), max(hi, b.to)
		}
	}
	nowSlot := -1
	now := m.clock()
	for _, col := range cols {
		if sameDay(now, col.day) {
			nowSlot = minutes(now, col.day) / slotMinutes
		}
	}
	gap := func(slots int, isNow bool) {
		label := ""
		if multi && slots > 0 {
			label = "⋯" + strings.ReplaceAll(freeText(slots), " ", "")
		}
		if isNow {
			label = now.Format(m.timeFmt)
		}
		row := newRow(m.gutter(label, isNow))
		if !multi && slots > 0 {
			rows[row].cells[0] = m.th.Faint.Render(theme.Fit(" ⋯ "+freeText(slots)+" free", cols[0].w))
		}
	}

	rowOf := map[int]int{}
	if hi > lo {
		if nowSlot >= 0 && nowSlot < lo {
			gap(lo-nowSlot, true)
		}
		for s := lo; s < hi; {
			if !busy[s] {
				e := s
				for e < hi && !busy[e] {
					e++
				}
				gap(e-s, nowSlot >= s && nowSlot < e)
				s = e
				continue
			}
			label := ""
			if s%(60/slotMinutes) == 0 || s == lo || !busy[s-1] {
				label = slotTime(cols[0].day, s).Format(m.timeFmt)
			}
			if s == nowSlot {
				label = now.Format(m.timeFmt)
			}
			rowOf[s] = newRow(m.gutter(label, s == nowSlot))
			s++
		}
	}
	if nowSlot >= 0 && (hi <= lo || nowSlot >= hi) {
		gap(0, true)
	}

	// Draw each day's blocks into the rows they cover.
	type seg struct {
		x, w int
		text string
	}
	for c, col := range cols {
		segs := map[int][]seg{}
		for _, b := range blocks[c] {
			bw := (col.w - (b.ncols - 1)) / b.ncols
			bx := b.col * (bw + 1)
			if b.col == b.ncols-1 {
				bw = col.w - bx
			}
			details := m.blockDetails(b.o, col.day, b.to-b.from)
			for s := b.from; s < b.to; s++ {
				kind, text := rowBlank, ""
				if k := s - b.from; k < len(details) {
					kind, text = details[k].kind, details[k].text
				}
				r := rowOf[s]
				segs[r] = append(segs[r], seg{bx, bw, m.blockLine(b.o, text, bw, kind, selected(c, b.idx), active)})
				rows[r].hits = append(rows[r].hits, itemHit{colX[c] + bx, colX[c] + bx + bw, b.idx, col.day})
			}
			if c == cur {
				spans[b.idx] = [2]int{rowOf[b.from], rowOf[b.to-1] + 1}
			}
		}
		for r, ss := range segs {
			sort.Slice(ss, func(i, j int) bool { return ss[i].x < ss[j].x })
			var sb strings.Builder
			at := 0
			for _, sg := range ss {
				sb.WriteString(strings.Repeat(" ", sg.x-at))
				sb.WriteString(sg.text)
				at = sg.x + sg.w
			}
			sb.WriteString(strings.Repeat(" ", max(0, col.w-at)))
			rows[r].cells[c] = sb.String()
		}
	}

	lines := make([]dayLine, len(rows))
	for r, row := range rows {
		var sb strings.Builder
		sb.WriteString(row.gutter)
		for c, cell := range row.cells {
			if c > 0 {
				sb.WriteString(sep)
			}
			if cell == "" {
				cell = strings.Repeat(" ", cols[c].w)
			}
			sb.WriteString(cell)
		}
		lines[r] = dayLine{text: sb.String(), hits: row.hits}
	}
	return lines, spans
}

// layoutColumns puts overlapping blocks side by side: each cluster of
// overlapping blocks gets as many columns as it needs at its busiest.
func layoutColumns(blocks []dayBlock) {
	sort.SliceStable(blocks, func(i, j int) bool { return blocks[i].from < blocks[j].from })
	for start := 0; start < len(blocks); {
		end, clusterTo := start, blocks[start].to
		var colEnds []int
		for end < len(blocks) && (end == start || blocks[end].from < clusterTo) {
			b := &blocks[end]
			b.col = -1
			for c, ce := range colEnds {
				if ce <= b.from {
					b.col, colEnds[c] = c, b.to
					break
				}
			}
			if b.col < 0 {
				b.col = len(colEnds)
				colEnds = append(colEnds, b.to)
			}
			clusterTo = max(clusterTo, b.to)
			end++
		}
		for i := start; i < end; i++ {
			blocks[i].ncols = len(colEnds)
		}
		start = end
	}
}

type rowKind int

const (
	rowBlank rowKind = iota
	rowTime
	rowTitle
	rowDetail
)

type blockRow struct {
	kind rowKind
	text string
}

// blockDetails is what a block of the given height shows, top to bottom:
// time, title, location, then description lines.
func (m *Model) blockDetails(o model.Occurrence, day time.Time, height int) []blockRow {
	when := m.whenText(o, day)
	if o.IsRecurring() || (o.Task != nil && o.Task.Due.IsRecurring) {
		when += " ↻"
	}
	if o.Event.Dirty {
		when += " ●"
	}
	loc := theme.OneLine(o.Event.Location)
	if height <= minSlots && loc != "" {
		when += " · " + loc
		loc = ""
	}
	rows := []blockRow{{rowTime, when}, {rowTitle, m.titleText(o)}}
	if loc != "" {
		rows = append(rows, blockRow{rowDetail, "@ " + loc})
	}
	for _, l := range strings.Split(o.Event.Description, "\n") {
		if l = strings.TrimSpace(l); l != "" && len(rows) < height {
			rows = append(rows, blockRow{rowDetail, l})
		}
	}
	return rows
}

func (m *Model) whenText(o model.Occurrence, day time.Time) string {
	switch {
	case o.End.Equal(o.Start):
		return o.Start.Format(m.timeFmt) // a task due at a time
	case !sameDay(o.Start, day):
		return "… " + o.End.Format(m.timeFmt)
	case o.End.After(day.AddDate(0, 0, 1)):
		return o.Start.Format(m.timeFmt) + " …"
	}
	return o.Start.Format(m.timeFmt) + "–" + o.End.Format(m.timeFmt)
}

func (m *Model) titleText(o model.Occurrence) string {
	title := o.Event.Title
	if title == "" {
		title = "(no title)"
	}
	if o.Task != nil {
		title = "☐ " + title
	}
	if o.Event.AllDay && (o.IsRecurring() || (o.Task != nil && o.Task.Due.IsRecurring)) {
		title += " ↻"
	}
	return title
}

// blockLine renders one row of a block: a solid bar in the calendar color
// and the text on a translucent tint of it.
func (m *Model) blockLine(o model.Occurrence, text string, w int, kind rowKind, selected, active bool) string {
	th := m.th
	hex := m.src.CalendarColor(o.Event.CalendarID)
	past := m.isPast(o)
	alpha := alphaBlock
	switch {
	case selected && active:
		alpha = alphaSelected
	case past:
		alpha = alphaPast
	}
	bg := th.Tint(hex, alpha)
	barFg := th.Tint(hex, 1)
	if past && !selected {
		barFg = th.Tint(hex, 0.45)
	}
	bar := lipgloss.NewStyle().Foreground(barFg).Background(bg).Render("▌")

	st := lipgloss.NewStyle().Background(bg)
	switch kind {
	case rowTime:
		st = st.Foreground(m.timeColor(hex, past))
	case rowTitle:
		st = st.Bold(true)
		if o.Task != nil {
			if p := m.priorityStyle(o.Task.Priority); p.GetForeground() != nil {
				st = st.Foreground(p.GetForeground())
			}
		}
		if past {
			st = st.Foreground(th.Muted)
		}
		if selected && !active {
			st = st.Underline(true)
		}
	case rowDetail:
		st = st.Foreground(th.Muted)
	}
	if selected && active {
		st = st.Bold(true)
	}
	return bar + st.Render(theme.Fit(" "+text, w-1))
}

func (m *Model) timeColor(hex string, past bool) color.Color {
	if past {
		return m.th.Muted
	}
	return m.th.Tint(hex, 1)
}

// gutter renders the time column; isNow marks the current time.
func (m *Model) gutter(label string, isNow bool) string {
	g := theme.Fit(fmt.Sprintf("%*s", gutterW-1, label), gutterW-1)
	if isNow {
		return m.th.ErrorText.Render(g + "▸")
	}
	return m.th.Faint.Render(g) + " "
}

// freeText formats a number of free slots, like "1h 30m".
func freeText(slots int) string {
	h, mm := slots*slotMinutes/60, slots*slotMinutes%60
	switch {
	case h == 0:
		return fmt.Sprintf("%dm", mm)
	case mm == 0:
		return fmt.Sprintf("%dh", h)
	}
	return fmt.Sprintf("%dh %dm", h, mm)
}

func clipToDay(o model.Occurrence, day time.Time) (time.Time, time.Time) {
	s, e := o.Start, o.End
	if s.Before(day) {
		s = day
	}
	if next := day.AddDate(0, 0, 1); e.After(next) {
		e = next
	}
	return s, e
}

// minutes since day's midnight, clamped to the day.
func minutes(t, day time.Time) int {
	return max(0, min(24*60, int(t.Sub(day).Minutes())))
}

func slotTime(day time.Time, slot int) time.Time {
	return time.Date(day.Year(), day.Month(), day.Day(), 0, slot*slotMinutes, 0, 0, time.Local)
}
