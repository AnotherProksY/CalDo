package quick

import (
	"testing"
	"time"

	"caldo/internal/model"
)

var cals = []model.Calendar{
	{ID: "work", Name: "Work", Enabled: true},
	{ID: "hol", Name: "Holidays in Kazakhstan", Enabled: true, ReadOnly: true},
}

func TestTask(t *testing.T) {
	projects := []model.Project{{ID: "in", Name: "Inbox", IsInbox: true, Enabled: true}, {ID: "h", Name: "Home Stuff", Enabled: true}}
	tk, err := Task("Buy milk #home_stuff @errands p1 tomorrow", projects, DefaultProject(projects), "")
	if err != nil {
		t.Fatal(err)
	}
	if tk.Content != "Buy milk" || tk.ProjectID != "h" || tk.Priority.Display() != 1 || len(tk.Labels) != 1 || tk.Due == nil {
		t.Fatalf("parsed %+v", tk)
	}
	if _, err := Task("x #Nope", projects, "in", ""); err == nil {
		t.Fatal("unknown project should fail")
	}
}

func TestEvent(t *testing.T) {
	day := time.Date(2026, 11, 2, 0, 0, 0, 0, time.Local)
	e, err := Event("14:00-15:30 Review #work", day, false, time.Hour, cals, DefaultCalendar(cals))
	if err != nil {
		t.Fatal(err)
	}
	if e.Title != "Review" || e.CalendarID != "work" || !e.Start.Equal(day.Add(14*time.Hour)) || !e.End.Equal(day.Add(15*time.Hour+30*time.Minute)) {
		t.Fatalf("timed: %+v", e)
	}
	if e, _ := Event("23:00-01:00 Late", day, false, time.Hour, cals, "work"); !e.End.Equal(day.Add(25 * time.Hour)) {
		t.Fatalf("past midnight: %v", e.End)
	}
	if e, _ := Event("Holiday", day, false, time.Hour, cals, "work"); !e.AllDay || !e.End.Equal(day.AddDate(0, 0, 1)) {
		t.Fatalf("all-day: %+v", e)
	}

	// A leading date is only read when asked to (the CLI), so the TUI keeps
	// titles like "Sunday brunch" on the selected day.
	e, _ = Event("2026-11-05 10:00 Dentist", day, true, time.Hour, cals, "work")
	if e.Title != "Dentist" || !e.Start.Equal(time.Date(2026, 11, 5, 10, 0, 0, 0, time.Local)) {
		t.Fatalf("leading date: %+v", e)
	}
	if e, _ := Event("Sunday brunch", day, false, time.Hour, cals, "work"); e.Title != "Sunday brunch" || !e.Start.Equal(day) {
		t.Fatalf("TUI must not read a leading day: %+v", e)
	}

	if _, err := Event("x #holidays_in_kazakhstan", day, false, time.Hour, cals, "work"); err == nil {
		t.Fatal("read-only calendar should fail")
	}
	if _, err := Event("14:00", day, false, time.Hour, cals, "work"); err == nil {
		t.Fatal("empty title should fail")
	}
}
