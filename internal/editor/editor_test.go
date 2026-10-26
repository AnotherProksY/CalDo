package editor

import (
	"errors"
	"strings"
	"testing"
	"time"

	"caldo/internal/model"
)

var lookup = Lookup{
	Calendars: []model.Calendar{{ID: "c1", Name: "Work"}, {ID: "c2", Name: "Home"}, {ID: "ro", Name: "Holidays", ReadOnly: true}},
	Projects:  []model.Project{{ID: "p1", Name: "Inbox"}, {ID: "p2", Name: "Work"}},
	Sections:  []model.Section{{ID: "s1", ProjectID: "p2", Name: "Next"}},
	Tasks:     []*model.Task{{ID: "t0", ProjectID: "p2", Content: "Parent"}},
}

func TestEventRoundTrip(t *testing.T) {
	start := time.Date(2026, 10, 27, 14, 0, 0, 0, time.Local)
	e := &model.Event{ID: "e", CalendarID: "c1", Title: "Встреча", Start: start, End: start.Add(time.Hour),
		Location: "Room 1", Recurrence: []string{"RRULE:FREQ=WEEKLY"}, Description: "Agenda:\n- one"}
	doc := EncodeEvent(e, lookup)
	got, err := DecodeEvent(doc, e, lookup)
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != e.Title || !got.Start.Equal(e.Start) || !got.End.Equal(e.End) || got.Location != e.Location ||
		got.Description != e.Description || len(got.Recurrence) != 1 || got.CalendarID != "c1" {
		t.Fatalf("round trip mismatch:\n%s\n%+v", doc, got)
	}

	edited := strings.Replace(string(doc), "calendar: Work", "calendar: Home", 1)
	edited = strings.Replace(edited, "all_day: false", "all_day: true", 1)
	edited = strings.Replace(edited, "start: 2026-10-27 14:00", "start: 2026-10-27", 1)
	edited = strings.Replace(edited, "end: 2026-10-27 15:00", "end: 2026-10-28", 1)
	got, err = DecodeEvent([]byte(edited), e, lookup)
	if err != nil {
		t.Fatal(err)
	}
	if got.CalendarID != "c2" || !got.AllDay || !got.End.Equal(time.Date(2026, 10, 29, 0, 0, 0, 0, time.Local)) {
		t.Fatalf("all-day edit: %+v", got)
	}
}

func TestEventReminders(t *testing.T) {
	start := time.Date(2026, 10, 27, 14, 0, 0, 0, time.Local)
	e := &model.Event{CalendarID: "c1", Title: "x", Start: start, End: start.Add(time.Hour)}
	doc := string(EncodeEvent(e, lookup))
	if !strings.Contains(doc, "reminders: default\n") {
		t.Fatalf("expected default reminders in:\n%s", doc)
	}
	decode := func(t *testing.T, d string) *model.Event {
		t.Helper()
		got, err := DecodeEvent([]byte(d), e, lookup)
		if err != nil {
			t.Fatalf("%v in:\n%s", err, d)
		}
		return got
	}
	if got := decode(t, doc); got.Reminders != nil {
		t.Fatalf("default should decode to nil, got %v", got.Reminders)
	}
	got := decode(t, strings.Replace(doc, "\nreminders: default", "\nreminders: [10m, 1h30m]", 1))
	if len(got.Reminders) != 2 || got.Reminders[1] != -90*time.Minute {
		t.Fatalf("list: %v", got.Reminders)
	}
	if again := string(EncodeEvent(got, lookup)); !strings.Contains(again, "reminders: [10m, 1h30m]\n") {
		t.Fatalf("list should encode in flow style:\n%s", again)
	}
	if got := decode(t, strings.Replace(doc, "\nreminders: default", "\nreminders: none", 1)); got.Reminders == nil || len(got.Reminders) != 0 {
		t.Fatalf("none: %v", got.Reminders)
	}

	allDay := strings.NewReplacer("all_day: false", "all_day: true", "start: 2026-10-27 14:00", "start: 2026-10-27",
		"end: 2026-10-27 15:00", "end: 2026-10-27", "\nreminders: default", "\nreminders: [\"09:00\", \"-1d 18:00\"]").Replace(doc)
	got = decode(t, allDay)
	if len(got.Reminders) != 2 || got.Reminders[0] != 9*time.Hour || got.Reminders[1] != -6*time.Hour {
		t.Fatalf("all-day: %v", got.Reminders)
	}
	if _, err := DecodeEvent([]byte(strings.Replace(doc, "\nreminders: default", "\nreminders: [09:00]", 1)), e, lookup); err == nil {
		t.Fatal("clock time on a timed event should fail")
	}
}

func TestEventValidation(t *testing.T) {
	e := &model.Event{CalendarID: "c1", Title: "x", Start: time.Now(), End: time.Now()}
	doc := string(EncodeEvent(e, lookup))
	cases := map[string]string{
		"read-only calendar": strings.Replace(doc, "calendar: Work", "calendar: Holidays", 1),
		"no title":           strings.Replace(doc, "title: x", "title: ''", 1),
		"no frontmatter":     "hello",
	}
	for name, d := range cases {
		if _, err := DecodeEvent([]byte(d), e, lookup); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
	if _, err := DecodeEvent([]byte("  \n"), e, lookup); !errors.Is(err, ErrEmpty) {
		t.Errorf("empty doc: got %v", err)
	}
}

func TestTaskRoundTrip(t *testing.T) {
	tk := &model.Task{ID: "t1", ProjectID: "p2", SectionID: "s1", ParentID: "t0", Content: "Write docs",
		Priority: model.PriorityFromDisplay(2), Labels: []string{"docs"}, Due: &model.Due{Date: "2026-11-01"}, Description: "body"}
	doc := EncodeTask(tk, lookup)
	got, err := DecodeTask(doc, tk, lookup)
	if err != nil {
		t.Fatal(err)
	}
	if got.Content != tk.Content || got.SectionID != "s1" || got.ParentID != "t0" || got.Priority != tk.Priority ||
		got.Due.Date != "2026-11-01" || got.Description != "body" || len(got.Labels) != 1 {
		t.Fatalf("round trip mismatch:\n%s\n%+v", doc, got)
	}
	edited := strings.Replace(string(doc), "due: \"2026-11-01\"", "due: every monday", 1)
	edited = strings.Replace(edited, "priority: p2", "priority: p1", 1)
	got, err = DecodeTask([]byte(edited), tk, lookup)
	if err != nil {
		t.Fatal(err)
	}
	if got.Due == nil || got.Due.String != "every monday" || !got.Due.IsRecurring || got.Priority.Display() != 1 {
		t.Fatalf("edit not applied:\n%s\n%+v %+v", edited, got, got.Due)
	}
}
