package notify

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"caldo/internal/model"
	"caldo/internal/remind"
	"caldo/internal/store"
)

func TestRun(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	day := time.Date(2026, 11, 2, 0, 0, 0, 0, time.Local)
	add := func(e *model.Event) {
		t.Helper()
		e.CalendarID = store.LocalCalendarID
		if err := st.SaveEvent(e); err != nil {
			t.Fatal(err)
		}
	}
	add(&model.Event{Title: "Standup", Start: day.Add(10 * time.Hour), End: day.Add(10*time.Hour + 15*time.Minute),
		Location: "Room 1", Recurrence: []string{"RRULE:FREQ=DAILY"}})
	add(&model.Event{Title: "Quiet", Start: day.Add(10 * time.Hour), End: day.Add(11 * time.Hour), Reminders: []time.Duration{}})
	add(&model.Event{Title: "Flight", Start: day.AddDate(0, 0, 1).Add(7 * time.Hour), End: day.AddDate(0, 0, 1).Add(9 * time.Hour),
		Reminders: []time.Duration{-24 * time.Hour}})
	add(&model.Event{Title: "Holiday", Start: day, End: day.AddDate(0, 0, 1), AllDay: true})
	add(&model.Event{Title: "Trip", Start: day.AddDate(0, 0, 1), End: day.AddDate(0, 0, 3), AllDay: true,
		Reminders: []time.Duration{-6 * time.Hour}}) // the day before at 18:00

	defs := remind.Defaults{Timed: []time.Duration{-10 * time.Minute}, AllDay: []time.Duration{9 * time.Hour}}
	run := func(at time.Time) []Notification {
		t.Helper()
		var got []Notification
		if _, err := Run(st, defs, at, func(n Notification) error { got = append(got, n); return nil }); err != nil {
			t.Fatal(err)
		}
		return got
	}
	titles := func(ns []Notification) string {
		var s []string
		for _, n := range ns {
			s = append(s, n.Title)
		}
		return strings.Join(s, ",")
	}

	// The first run is silent: it only records reminders already due.
	if got := run(day.Add(-time.Hour)); len(got) != 0 {
		t.Fatalf("first run must be silent, got %s", titles(got))
	}
	if got := run(day.Add(6 * time.Hour)); len(got) != 0 {
		t.Fatalf("06:00: nothing due yet, got %s", titles(got))
	}
	got := run(day.Add(7 * time.Hour))
	if titles(got) != "Flight" || got[0].Body != "tomorrow 07:00–09:00 · in 1 d" {
		t.Fatalf("07:00: %+v", got)
	}
	got = run(day.Add(9 * time.Hour))
	if titles(got) != "Holiday" || got[0].Body != "All day · today" {
		t.Fatalf("09:00: %+v", got)
	}
	got = run(day.Add(9*time.Hour + 52*time.Minute))
	if titles(got) != "Standup" || got[0].Body != "10:00–10:15 · in 8 min\nRoom 1" {
		t.Fatalf("09:52: %+v", got)
	}
	if got := run(day.Add(9*time.Hour + 53*time.Minute)); len(got) != 0 {
		t.Fatalf("already shown reminders must not repeat: %s", titles(got))
	}
	if got := run(day.Add(18 * time.Hour)); titles(got) != "Trip" || !strings.HasPrefix(got[0].Body, "All day · tomorrow – Wed 4 Nov") {
		t.Fatalf("18:00: %+v", got)
	}
	// The next day's standup fires again.
	if got := run(day.AddDate(0, 0, 1).Add(9*time.Hour + 50*time.Minute)); titles(got) != "Standup" {
		t.Fatalf("next day: %s", titles(got))
	}
}

func TestDueSkipsStaleAndFinished(t *testing.T) {
	day := time.Date(2026, 11, 2, 0, 0, 0, 0, time.Local)
	e := &model.Event{ID: "e", Title: "Long", Start: day.AddDate(0, 0, -2), End: day.AddDate(0, 0, 1)}
	occ := []model.Occurrence{{Event: e, Start: e.Start, End: e.End}}
	defs := remind.Defaults{Timed: []time.Duration{-10 * time.Minute}}
	if got := Due(occ, defs, day.Add(12*time.Hour)); len(got) != 0 {
		t.Fatalf("reminder from two days ago should be dropped: %+v", got)
	}
	if got := Due(occ, defs, day.AddDate(0, 0, 2)); len(got) != 0 {
		t.Fatalf("finished event: %+v", got)
	}
}

func TestFirstRunIsSilent(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.Local)
	if err := st.SaveEvent(&model.Event{CalendarID: store.LocalCalendarID, Title: "Ongoing",
		Start: now.Add(-time.Hour), End: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	defs := remind.Defaults{Timed: []time.Duration{-10 * time.Minute}}
	count := func() int {
		n := 0
		if _, err := Run(st, defs, now, func(Notification) error { n++; return nil }); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := count(); n != 0 {
		t.Fatalf("first run sent %d", n)
	}
	if n := count(); n != 0 {
		t.Fatalf("reminder recorded on the first run was sent later (%d)", n)
	}
}

func TestDueTasks(t *testing.T) {
	now := time.Date(2026, 11, 2, 18, 30, 20, 0, time.Local)
	tasks := []*model.Task{
		{ID: "a", ProjectID: "p", Content: "Call mom", Due: &model.Due{Date: "2026-11-02T18:30:00"}},
		{ID: "b", ProjectID: "p", Content: "Later", Due: &model.Due{Date: "2026-11-02T19:00:00"}},
		{ID: "c", ProjectID: "p", Content: "Date only", Due: &model.Due{Date: "2026-11-02"}},
		{ID: "d", ProjectID: "p", Content: "Done", Checked: true, Due: &model.Due{Date: "2026-11-02T18:00:00"}},
		{ID: "e", ProjectID: "p", Content: "No due"},
	}
	names := map[string]string{"p": "Inbox"}
	defs := remind.Defaults{Tasks: []time.Duration{0}}
	got := DueTasks(tasks, names, defs, now)
	if len(got) != 1 || got[0].Title != "☐ Call mom" || got[0].Body != "18:30 · due now · Inbox" {
		t.Fatalf("at due time: %+v", got)
	}
	if got := DueTasks(tasks, names, defs, now.Add(-time.Minute)); len(got) != 0 {
		t.Fatalf("before due time: %+v", got)
	}
	defs.TasksAllDay = []time.Duration{9 * time.Hour}
	if got := DueTasks(tasks, names, defs, now); len(got) != 2 || got[1].Body != "Due today · Inbox" {
		t.Fatalf("tasks_all_day: %+v", got)
	}
}
