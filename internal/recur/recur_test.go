package recur

import (
	"testing"
	"time"

	"caldo/internal/model"
)

func at(y int, m time.Month, d, h, min int) time.Time {
	return time.Date(y, m, d, h, min, 0, 0, time.Local)
}

func titles(occ []model.Occurrence) []string {
	var out []string
	for _, o := range occ {
		out = append(out, o.Start.Format("01-02 15:04")+" "+o.Event.Title)
	}
	return out
}

func TestWeeklyWithExdateAndExceptions(t *testing.T) {
	master := &model.Event{
		ID: "m", Title: "Sync", Start: at(2026, 10, 5, 10, 0), End: at(2026, 10, 5, 11, 0),
		Recurrence: []string{"RRULE:FREQ=WEEKLY;BYDAY=MO", "EXDATE:" + at(2026, 10, 12, 10, 0).UTC().Format("20060102T150405Z")},
	}
	moved := at(2026, 10, 19, 10, 0)
	cancelled := at(2026, 10, 26, 10, 0)
	evs := []*model.Event{
		master,
		{ID: "x1", Title: "Sync (moved)", RecurringEventID: "m", OriginalStart: &moved, Start: at(2026, 10, 20, 15, 0), End: at(2026, 10, 20, 16, 0)},
		{ID: "x2", Title: "Sync", RecurringEventID: "m", OriginalStart: &cancelled, Status: model.StatusCancelled, Start: cancelled, End: cancelled},
	}
	got := titles(Occurrences(evs, at(2026, 10, 1, 0, 0), at(2026, 11, 3, 0, 0)))
	want := []string{"10-05 10:00 Sync", "10-20 15:00 Sync (moved)", "11-02 10:00 Sync"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d] got %q, want %q", i, got[i], want[i])
		}
	}
}

func TestAllDayAndCount(t *testing.T) {
	m := &model.Event{ID: "a", Title: "Trip", AllDay: true, Start: at(2026, 3, 1, 0, 0), End: at(2026, 3, 3, 0, 0),
		Recurrence: []string{"RRULE:FREQ=MONTHLY;COUNT=2"}}
	occ := Occurrences([]*model.Event{m}, at(2026, 1, 1, 0, 0), at(2027, 1, 1, 0, 0))
	if len(occ) != 2 {
		t.Fatalf("want 2 occurrences, got %v", titles(occ))
	}
	if !occ[1].Start.Equal(at(2026, 4, 1, 0, 0)) || !occ[1].End.Equal(at(2026, 4, 3, 0, 0)) {
		t.Errorf("second occurrence %v–%v", occ[1].Start, occ[1].End)
	}
}

func TestDSTKeepsWallClock(t *testing.T) {
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Skip("no tzdata")
	}
	start := time.Date(2026, 10, 20, 9, 0, 0, 0, berlin)
	m := &model.Event{ID: "d", Title: "Daily", Timezone: "Europe/Berlin", Start: start, End: start.Add(time.Hour),
		Recurrence: []string{"RRULE:FREQ=DAILY"}}
	starts, err := Expand(m, start, start.AddDate(0, 0, 14))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range starts {
		if h := s.In(berlin).Hour(); h != 9 {
			t.Fatalf("occurrence %v is at %d:00 Berlin time, want 9:00", s, h)
		}
	}
}

func TestSplitHelpers(t *testing.T) {
	m := &model.Event{ID: "s", Start: at(2026, 1, 1, 9, 0), End: at(2026, 1, 1, 10, 0),
		Recurrence: []string{"RRULE:FREQ=DAILY;COUNT=10"}}
	split := at(2026, 1, 4, 9, 0)
	head := UntilBefore(m.Recurrence, split, false)
	if head[0] != "RRULE:FREQ=DAILY;UNTIL="+split.Add(-time.Second).UTC().Format("20060102T150405Z") {
		t.Errorf("head rule %q", head[0])
	}
	tail := TailRecurrence(m, split)
	if tail[0] != "RRULE:FREQ=DAILY;COUNT=7" {
		t.Errorf("tail rule %q, want COUNT=7", tail[0])
	}
	m.Recurrence = head
	if n := len(Occurrences([]*model.Event{m}, at(2026, 1, 1, 0, 0), at(2026, 2, 1, 0, 0))); n != 3 {
		t.Errorf("head series has %d occurrences, want 3", n)
	}
}
