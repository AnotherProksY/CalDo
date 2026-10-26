package store

import (
	"path/filepath"
	"testing"
	"time"

	"caldo/internal/model"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func ops(t *testing.T, s *Store, p model.Provider) []model.Op {
	t.Helper()
	es, err := s.Pending(p)
	if err != nil {
		t.Fatal(err)
	}
	var out []model.Op
	for _, e := range es {
		out = append(out, e.Op)
	}
	return out
}

func TestLocalEntitiesAreNotQueued(t *testing.T) {
	s := openTest(t)
	if err := s.SaveTask(&model.Task{ProjectID: LocalInboxID, Content: "x"}); err != nil {
		t.Fatal(err)
	}
	if n := s.PendingCount(); n != 0 {
		t.Fatalf("local task queued %d entries", n)
	}
}

func TestOutboxCoalescing(t *testing.T) {
	s := openTest(t)
	p, err := s.PutRemoteProject(model.Project{Provider: model.ProviderTodoist, RemoteID: "r1", Name: "Inbox"})
	if err != nil {
		t.Fatal(err)
	}

	// create + edits + delete before any push leaves nothing behind.
	tk := &model.Task{ProjectID: p.ID, Content: "a"}
	s.SaveTask(tk)
	tk.Content = "b"
	s.SaveTask(tk)
	if got := ops(t, s, model.ProviderTodoist); len(got) != 1 || got[0] != model.OpCreate {
		t.Fatalf("after create+edit: %v", got)
	}
	if err := s.DeleteTask(tk.ID); err != nil {
		t.Fatal(err)
	}
	if n := s.PendingCount(); n != 0 {
		t.Fatalf("create+delete should cancel out, %d left", n)
	}
	if _, err := s.Task(tk.ID); err == nil {
		t.Fatal("unpushed deleted task should be gone")
	}

	// synced task: complete then reopen cancels; edit + move are queued once each.
	synced := &model.Task{ProjectID: p.ID, RemoteID: "srv", Content: "c"}
	s.PutRemoteTask(synced)
	s.SetTaskChecked(synced.ID, true)
	s.SetTaskChecked(synced.ID, false)
	if n := s.PendingCount(); n != 0 {
		t.Fatalf("complete+reopen should cancel out, got %v", ops(t, s, model.ProviderTodoist))
	}
	synced.Content = "d"
	s.SaveTask(synced)
	synced.Content = "e"
	s.SaveTask(synced)
	if got := ops(t, s, model.ProviderTodoist); len(got) != 1 || got[0] != model.OpUpdate {
		t.Fatalf("edits: %v", got)
	}
	s.DeleteTask(synced.ID)
	if got := ops(t, s, model.ProviderTodoist); len(got) != 1 || got[0] != model.OpDelete {
		t.Fatalf("delete should replace pending edits: %v", got)
	}
	ts, _ := s.Tasks(true)
	if len(ts) != 0 {
		t.Fatal("soft-deleted task should be hidden")
	}
}

func TestEventsForRangeHidesDisabledCalendars(t *testing.T) {
	s := openTest(t)
	now := time.Now()
	s.SaveEvent(&model.Event{CalendarID: LocalCalendarID, Title: "x", Start: now, End: now.Add(time.Hour)})
	evs, _ := s.EventsForRange(now.Add(-time.Hour), now.Add(2*time.Hour))
	if len(evs) != 1 {
		t.Fatalf("want 1 event, got %d", len(evs))
	}
	s.SetCalendarEnabled(LocalCalendarID, false)
	evs, _ = s.EventsForRange(now.Add(-time.Hour), now.Add(2*time.Hour))
	if len(evs) != 0 {
		t.Fatal("disabled calendar should be hidden")
	}
}

func TestRemindersAreLocal(t *testing.T) {
	s := openTest(t)
	cal, err := s.UpsertRemoteCalendar(model.Calendar{Provider: model.ProviderGoogle, RemoteID: "c", Name: "Work", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 11, 2, 10, 0, 0, 0, time.Local)
	e := &model.Event{CalendarID: cal.ID, RemoteID: "ev1", ETag: "1", Title: "Sync", Start: start, End: start.Add(time.Hour)}
	if err := s.PutRemoteEvent(e); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Event(e.ID)
	if got.Reminders != nil {
		t.Fatalf("new event should use defaults, got %v", got.Reminders)
	}

	// Changing only reminders queues nothing.
	got.Reminders = []time.Duration{-15 * time.Minute, -time.Hour}
	if err := s.SaveEvent(got); err != nil {
		t.Fatal(err)
	}
	if n := s.PendingCount(); n != 0 {
		t.Fatalf("reminder-only edit queued %d entries", n)
	}

	// A pull rewriting the event keeps them.
	pulled := *e
	pulled.Title, pulled.ETag = "Sync (moved)", "2"
	if err := s.PutRemoteEvent(&pulled); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Event(e.ID)
	if len(got.Reminders) != 2 || got.Reminders[1] != -time.Hour || got.Title != "Sync (moved)" {
		t.Fatalf("after pull: %+v", got)
	}

	// Explicit "none" survives as an empty, non-nil slice; nil restores defaults.
	if err := s.SetEventReminders(e.ID, []time.Duration{}); err != nil {
		t.Fatal(err)
	}
	if got, _ = s.Event(e.ID); got.Reminders == nil || len(got.Reminders) != 0 {
		t.Fatalf("none: %v", got.Reminders)
	}
	if err := s.SetEventReminders(e.ID, nil); err != nil {
		t.Fatal(err)
	}
	if got, _ = s.Event(e.ID); got.Reminders != nil {
		t.Fatalf("defaults: %v", got.Reminders)
	}
}
