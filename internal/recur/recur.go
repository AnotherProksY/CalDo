// Package recur expands recurring event series into concrete occurrences.
package recur

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/teambition/rrule-go"

	"caldo/internal/model"
)

// Occurrences turns raw events (singles, masters, exceptions) into the sorted list
// of occurrences overlapping [from, to). Cancelled occurrences are dropped.
func Occurrences(events []*model.Event, from, to time.Time) []model.Occurrence {
	masters := map[string]*model.Event{}
	exceptions := map[string][]*model.Event{}
	var out []model.Occurrence

	for _, e := range events {
		switch {
		case e.IsException():
			exceptions[e.RecurringEventID] = append(exceptions[e.RecurringEventID], e)
		case e.IsRecurring():
			masters[e.ID] = e
		default:
			if e.Status != model.StatusCancelled && overlaps(e.Start, e.End, from, to) {
				out = append(out, model.Occurrence{Event: e, Start: e.Start, End: e.End})
			}
		}
	}

	for id, m := range masters {
		// Exceptions replace the slot they were moved from.
		replaced := map[int64]bool{}
		for _, ex := range exceptions[id] {
			if ex.OriginalStart != nil {
				replaced[slotKey(*ex.OriginalStart, m.AllDay)] = true
			}
			if ex.Status != model.StatusCancelled && overlaps(ex.Start, ex.End, from, to) {
				inst := ex.Start
				if ex.OriginalStart != nil {
					inst = *ex.OriginalStart
				}
				out = append(out, model.Occurrence{Event: ex, Start: ex.Start, End: ex.End, Master: m, InstanceStart: inst})
			}
		}
		if m.Status == model.StatusCancelled {
			continue
		}
		starts, err := Expand(m, from, to)
		if err != nil {
			// A malformed rule still shows its first occurrence rather than vanishing.
			starts = []time.Time{m.Start}
		}
		dur := m.End.Sub(m.Start)
		for _, st := range starts {
			if replaced[slotKey(st, m.AllDay)] {
				continue
			}
			end := st.Add(dur)
			if m.AllDay {
				end = addDaysKeepClock(st, daysBetween(m.Start, m.End))
			}
			if overlaps(st, end, from, to) {
				out = append(out, model.Occurrence{Event: m, Start: st, End: end, Master: m, InstanceStart: st})
			}
		}
	}

	Sort(out)
	return out
}

// Sort orders occurrences for display: all-day items first on their day,
// then by start; events come before tasks at the same time.
func Sort(out []model.Occurrence) {
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Event.AllDay != b.Event.AllDay && sameDay(a.Start, b.Start) {
			return a.Event.AllDay
		}
		if !a.Start.Equal(b.Start) {
			return a.Start.Before(b.Start)
		}
		if (a.Task == nil) != (b.Task == nil) {
			return a.Task == nil
		}
		return a.Event.Title < b.Event.Title
	})
}

// Expand returns series start times whose occurrence could overlap [from, to).
func Expand(m *model.Event, from, to time.Time) ([]time.Time, error) {
	loc := time.Local
	if m.Timezone != "" {
		if l, err := time.LoadLocation(m.Timezone); err == nil {
			loc = l
		}
	}
	dtstart := m.Start.In(loc)
	if m.AllDay {
		// All-day series are floating dates; expand at local midnight.
		dtstart = time.Date(m.Start.Year(), m.Start.Month(), m.Start.Day(), 0, 0, 0, 0, time.Local)
		loc = time.Local
	}
	lines := make([]string, 0, len(m.Recurrence)+1)
	lines = append(lines, "DTSTART"+dtstartParam(dtstart, m.AllDay))
	for _, l := range m.Recurrence {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		if !strings.Contains(l, ":") {
			l = "RRULE:" + l
		}
		lines = append(lines, normalizeLine(l))
	}
	set, err := rrule.StrSliceToRRuleSetInLoc(lines, loc)
	if err != nil {
		return nil, fmt.Errorf("event %q: %w", m.Title, err)
	}
	// Widen the lower bound by the event length so occurrences that started
	// before `from` but are still running are included.
	dur := m.End.Sub(m.Start)
	starts := set.Between(from.Add(-dur), to, true)
	for i := range starts {
		starts[i] = starts[i].In(time.Local)
	}
	return starts, nil
}

// normalizeLine rewrites date-only values, which rrule-go cannot parse, as
// floating local datetimes: EXDATE;VALUE=DATE:20260301 → EXDATE:20260301T000000,
// UNTIL=20260301 → UNTIL=20260301T235959.
func normalizeLine(l string) string {
	name, value, ok := strings.Cut(l, ":")
	if !ok {
		return l
	}
	upper := strings.ToUpper(name)
	if strings.Contains(upper, "VALUE=DATE") && !strings.Contains(upper, "VALUE=DATE-TIME") {
		base, _, _ := strings.Cut(name, ";")
		dates := strings.Split(value, ",")
		for i, d := range dates {
			if len(d) == 8 {
				dates[i] = d + "T000000"
			}
		}
		return base + ":" + strings.Join(dates, ",")
	}
	if upper == "RRULE" {
		parts := strings.Split(value, ";")
		for i, p := range parts {
			if k, v, ok := strings.Cut(p, "="); ok && strings.EqualFold(k, "UNTIL") && len(v) == 8 {
				parts[i] = k + "=" + v + "T235959"
			}
		}
		return name + ":" + strings.Join(parts, ";")
	}
	return l
}

func dtstartParam(t time.Time, allDay bool) string {
	if allDay {
		// Floating time: interpreted in the location passed to the parser.
		return ":" + t.Format("20060102T150405")
	}
	if t.Location() == time.UTC {
		return ":" + t.Format("20060102T150405Z")
	}
	name := t.Location().String()
	if name == "Local" || name == "" {
		// Floating time in the local zone keeps the wall clock across DST changes.
		return ":" + t.Format("20060102T150405")
	}
	return ";TZID=" + name + ":" + t.Format("20060102T150405")
}

func overlaps(s, e, from, to time.Time) bool {
	if !e.After(s) {
		// Zero-length events count if they start inside the range.
		return !s.Before(from) && s.Before(to)
	}
	return s.Before(to) && e.After(from)
}

func slotKey(t time.Time, allDay bool) int64 {
	if allDay {
		y, m, d := t.Date()
		return int64(y)*10000 + int64(m)*100 + int64(d)
	}
	return t.Unix()
}

func sameDay(a, b time.Time) bool {
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	return ay == by && am == bm && ad == bd
}

func daysBetween(a, b time.Time) int {
	a = time.Date(a.Year(), a.Month(), a.Day(), 0, 0, 0, 0, time.UTC)
	b = time.Date(b.Year(), b.Month(), b.Day(), 0, 0, 0, 0, time.UTC)
	return int(b.Sub(a).Hours() / 24)
}

func addDaysKeepClock(t time.Time, n int) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day()+n, t.Hour(), t.Minute(), t.Second(), 0, t.Location())
}

// UntilBefore returns recurrence lines with the RRULE ended just before instance,
// used to split a series for "this and following" edits.
func UntilBefore(lines []string, instance time.Time, allDay bool) []string {
	var until string
	if allDay {
		until = instance.AddDate(0, 0, -1).Format("20060102")
	} else {
		until = instance.Add(-time.Second).UTC().Format("20060102T150405Z")
	}
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		if !strings.HasPrefix(strings.ToUpper(l), "RRULE:") {
			out = append(out, l)
			continue
		}
		parts := strings.Split(l[len("RRULE:"):], ";")
		kept := parts[:0]
		for _, p := range parts {
			up := strings.ToUpper(p)
			if strings.HasPrefix(up, "UNTIL=") || strings.HasPrefix(up, "COUNT=") {
				continue
			}
			kept = append(kept, p)
		}
		kept = append(kept, "UNTIL="+until)
		out = append(out, "RRULE:"+strings.Join(kept, ";"))
	}
	return out
}

// TailRecurrence returns recurrence lines for the new series that starts at
// instance when a series is split. A COUNT is reduced by the occurrences that
// stay with the original series.
func TailRecurrence(m *model.Event, instance time.Time) []string {
	before := 0
	if starts, err := Expand(m, m.Start, instance); err == nil {
		for _, st := range starts {
			if st.Before(instance) {
				before++
			}
		}
	}
	out := make([]string, 0, len(m.Recurrence))
	for _, l := range m.Recurrence {
		if !strings.HasPrefix(strings.ToUpper(l), "RRULE:") {
			out = append(out, l)
			continue
		}
		parts := strings.Split(l[len("RRULE:"):], ";")
		for i, p := range parts {
			if strings.HasPrefix(strings.ToUpper(p), "COUNT=") {
				var n int
				if _, err := fmt.Sscanf(p[len("COUNT="):], "%d", &n); err != nil {
					continue
				}
				if n-before < 1 {
					n = before + 1
				}
				parts[i] = fmt.Sprintf("COUNT=%d", n-before)
			}
		}
		out = append(out, "RRULE:"+strings.Join(parts, ";"))
	}
	return out
}
