package agenda

import (
	"testing"
	"time"

	"caldo/internal/model"
)

func TestNext(t *testing.T) {
	day := Day(time.Now())
	occ := func(title string, s, e time.Duration, allDay bool) model.Occurrence {
		return model.Occurrence{Event: &model.Event{Title: title, AllDay: allDay}, Start: day.Add(s), End: day.Add(e)}
	}
	occs := []model.Occurrence{
		occ("Holiday", 0, 24*time.Hour, true),
		occ("Standup", 9*time.Hour, 9*time.Hour+15*time.Minute, false),
		occ("Call", 12*time.Hour, 12*time.Hour, false), // a task due at a time
		occ("Lunch", 13*time.Hour, 14*time.Hour, false),
	}
	for _, c := range []struct {
		at   time.Duration
		want string
	}{{8 * time.Hour, "Standup"}, {9*time.Hour + 5*time.Minute, "Standup"}, {11 * time.Hour, "Call"}, {12*time.Hour + time.Minute, "Lunch"}} {
		o, ok := Next(occs, day.Add(c.at))
		if !ok || o.Event.Title != c.want {
			t.Errorf("at %v: got %q, want %q", c.at, o.Event.Title, c.want)
		}
	}
	if _, ok := Next(occs, day.Add(15*time.Hour)); ok {
		t.Error("nothing should be next after the last event")
	}
}
