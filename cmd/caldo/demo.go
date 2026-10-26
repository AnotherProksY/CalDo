package main

import (
	"time"

	"caldo/internal/editor"
	"caldo/internal/model"
	"caldo/internal/store"
)

// seedDemo fills the local calendar/project with sample data for trying the UI.
func seedDemo(st *store.Store) error {
	n := time.Now()
	day := func(offset int) time.Time {
		return time.Date(n.Year(), n.Month(), n.Day()+offset, 0, 0, 0, 0, time.Local)
	}
	at := func(offset, h, m int) time.Time {
		return day(offset).Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute)
	}

	events := []*model.Event{
		{Title: "Standup", Start: at(0, 10, 0), End: at(0, 10, 15), Recurrence: []string{"RRULE:FREQ=WEEKLY;BYDAY=MO,TU,WE,TH,FR"}},
		{Title: "Lunch with Alex", Start: at(0, 13, 0), End: at(0, 14, 0), Location: "Café"},
		{Title: "Design review", Start: at(1, 15, 0), End: at(1, 16, 30), Description: "Review the new calendar layout."},
		{Title: "Conference", Start: day(3), End: day(5), AllDay: true},
		{Title: "Gym", Start: at(-2, 19, 0), End: at(-2, 20, 0), Recurrence: []string{"RRULE:FREQ=WEEKLY;INTERVAL=1;COUNT=10"}},
		{Title: "Dentist", Start: at(7, 9, 30), End: at(7, 10, 0)},
	}
	for _, e := range events {
		e.CalendarID = store.LocalCalendarID
		if err := st.SaveEvent(e); err != nil {
			return err
		}
	}

	tasks := []*model.Task{
		{Content: "Buy milk", Priority: model.PriorityFromDisplay(1), Due: editor.ParseDue("today", nil), Labels: []string{"errands"}},
		{Content: "Write CalDo README", Priority: model.PriorityFromDisplay(2), Due: editor.ParseDue("tomorrow", nil)},
		{Content: "Pay rent", Due: &model.Due{Date: day(-1).Format("2006-01-02")}},
		{Content: "Water plants", Due: &model.Due{Date: day(2).Format("2006-01-02"), String: "every 3 days", IsRecurring: true}},
		{Content: "Read a book", Priority: model.PriorityFromDisplay(3), Labels: []string{"home"}},
		{Content: "Call mom", Checked: true},
	}
	for i, t := range tasks {
		t.ProjectID = store.LocalInboxID
		t.Sort = i
		if err := st.SaveTask(t); err != nil {
			return err
		}
	}
	sub := &model.Task{Content: "Outline sections", ProjectID: store.LocalInboxID, ParentID: tasks[1].ID, Sort: 10}
	return st.SaveTask(sub)
}
