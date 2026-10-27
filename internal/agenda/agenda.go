// Package agenda gathers what is on in a time range: expanded events from
// enabled calendars plus open dated tasks, in display order. The TUI calendar
// and the `caldo agenda`/`next`/`waybar` commands share it.
package agenda

import (
	"time"

	"caldo/internal/model"
	"caldo/internal/recur"
	"caldo/internal/store"
)

// Items returns occurrences overlapping [from, to); withTasks adds open tasks
// due in the range.
func Items(st *store.Store, from, to time.Time, withTasks bool) ([]model.Occurrence, error) {
	evs, err := st.EventsForRange(from, to)
	if err != nil {
		return nil, err
	}
	occs := recur.Occurrences(evs, from, to)
	if withTasks {
		ts, err := st.Tasks(false)
		if err != nil {
			return nil, err
		}
		occs = append(occs, TaskOccurrences(ts, from, to)...)
		recur.Sort(occs)
	}
	return occs, nil
}

// TaskOccurrences shows open tasks with a due date as calendar items: all-day
// on their date, or as a point in time when the due date has a time.
func TaskOccurrences(ts []*model.Task, from, to time.Time) []model.Occurrence {
	var out []model.Occurrence
	for _, t := range ts {
		if t.Checked {
			continue
		}
		due, hasTime, ok := t.Due.Time()
		if !ok {
			continue
		}
		start, end := due, due
		if !hasTime {
			start = Day(due)
			end = start.AddDate(0, 0, 1)
		}
		if !start.Before(to) || (end.Before(from) || (end.Equal(from) && !end.Equal(start))) {
			continue
		}
		ev := &model.Event{ID: "task:" + t.ID, CalendarID: t.ProjectID, Title: t.Content,
			Description: t.Description, Start: start, End: end, AllDay: !hasTime, Dirty: t.Dirty}
		out = append(out, model.Occurrence{Event: ev, Start: start, End: end, Task: t})
	}
	return out
}

// Overdue lists open tasks due before today, oldest first.
func Overdue(st *store.Store, now time.Time) ([]*model.Task, error) {
	ts, err := st.Tasks(false)
	if err != nil {
		return nil, err
	}
	today := Day(now)
	var out []*model.Task
	for _, t := range ts {
		if d, _, ok := t.Due.Time(); ok && Day(d).Before(today) {
			out = append(out, t)
		}
	}
	return out, nil
}

// Next is the item that is on now or starts next, skipping all-day items
// that have already begun; ok is false when nothing is left.
func Next(occs []model.Occurrence, now time.Time) (model.Occurrence, bool) {
	for _, o := range occs {
		if o.Event.AllDay && !o.Start.After(now) {
			continue
		}
		if o.End.After(now) || (o.End.Equal(o.Start) && !o.Start.Before(now)) {
			return o, true
		}
	}
	return model.Occurrence{}, false
}

// Day is local midnight of t's date.
func Day(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.Local)
}
