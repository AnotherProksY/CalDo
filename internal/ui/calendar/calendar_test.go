package calendar

import (
	"testing"
	"time"

	"caldo/internal/model"
)

func TestIsPast(t *testing.T) {
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.Local)
	day := time.Date(2026, 11, 2, 0, 0, 0, 0, time.Local)
	m := Model{DimPast: true, now: func() time.Time { return now }}
	occ := func(start, end time.Time, allDay bool, task *model.Task) model.Occurrence {
		return model.Occurrence{Event: &model.Event{AllDay: allDay}, Start: start, End: end, Task: task}
	}
	cases := []struct {
		name string
		o    model.Occurrence
		want bool
	}{
		{"ended this morning", occ(day.Add(9*time.Hour), day.Add(10*time.Hour), false, nil), true},
		{"in progress", occ(day.Add(11*time.Hour), day.Add(13*time.Hour), false, nil), false},
		{"later today", occ(day.Add(15*time.Hour), day.Add(16*time.Hour), false, nil), false},
		{"all day today", occ(day, day.AddDate(0, 0, 1), true, nil), false},
		{"all day yesterday", occ(day.AddDate(0, 0, -1), day, true, nil), true},
		{"overdue task", occ(day.Add(9*time.Hour), day.Add(9*time.Hour), false, &model.Task{}), false},
	}
	for _, c := range cases {
		if got := m.isPast(c.o); got != c.want {
			t.Errorf("%s: isPast = %v, want %v", c.name, got, c.want)
		}
	}
	m.DimPast = false
	if m.isPast(cases[0].o) {
		t.Error("dim_past = false must never dim")
	}
}

type fakeSource []model.Occurrence

func (f fakeSource) Occurrences(from, to time.Time) []model.Occurrence { return f }
func (f fakeSource) CalendarColor(string) string                       { return "" }

func TestSelectNow(t *testing.T) {
	day := today()
	ev := func(title string, start, end time.Duration, allDay bool) model.Occurrence {
		return model.Occurrence{Event: &model.Event{Title: title, AllDay: allDay},
			Start: day.Add(start), End: day.Add(end)}
	}
	src := fakeSource{
		ev("Holiday", 0, 24*time.Hour, true),
		ev("Standup", 9*time.Hour, 9*time.Hour+15*time.Minute, false),
		ev("Lunch", 13*time.Hour, 14*time.Hour, false),
		ev("Dinner", 19*time.Hour, 20*time.Hour, false),
	}
	m := New(src, nil, Month, time.Monday, 14, "15:04")
	for _, c := range []struct {
		at   time.Duration
		want string
	}{
		{7 * time.Hour, "Standup"},               // before everything: the first timed event
		{13*time.Hour + 30*time.Minute, "Lunch"}, // in progress
		{15 * time.Hour, "Dinner"},               // next one
		{21 * time.Hour, "Dinner"},               // all over: the last
	} {
		at := day.Add(c.at)
		m.now = func() time.Time { return at }
		m.Today()
		if o := m.currentList()[m.sel]; o.Event.Title != c.want {
			t.Errorf("at %s: selected %q, want %q", at.Format("15:04"), o.Event.Title, c.want)
		}
	}
}
