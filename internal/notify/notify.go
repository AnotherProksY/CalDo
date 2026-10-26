// Package notify decides which event reminders are due and records the ones
// already shown, so `caldo notify` can run every minute from a timer.
package notify

import (
	"fmt"
	"strings"
	"time"

	"caldo/internal/model"
	"caldo/internal/recur"
	"caldo/internal/remind"
	"caldo/internal/store"
)

// Stale reminders older than this are dropped instead of shown, e.g. after
// the machine was off for a day.
const maxDelay = 24 * time.Hour

const stateInit = "notify:initialized"

type Notification struct {
	Key   string
	Title string
	Body  string
}

// Due lists reminders that have gone off by now for events that are not over.
func Due(occs []model.Occurrence, defs remind.Defaults, now time.Time) []Notification {
	var out []Notification
	for _, o := range occs {
		if !o.End.After(now) {
			continue
		}
		for _, off := range defs.For(o) {
			fire := remind.FireTime(o.Start, off, o.Event.AllDay)
			if fire.After(now) || now.Sub(fire) >= maxDelay {
				continue
			}
			out = append(out, Notification{
				Key:   fmt.Sprintf("%s|%d|%d", o.Event.ID, o.Start.Unix(), int(off/time.Minute)),
				Title: o.Event.Title,
				Body:  body(o, now),
			})
		}
	}
	return out
}

// DueTasks lists reminders for open tasks whose time has come. The key holds
// the due date, so a recurring task notifies again once Todoist moves it.
func DueTasks(tasks []*model.Task, projects map[string]string, defs remind.Defaults, now time.Time) []Notification {
	var out []Notification
	for _, t := range tasks {
		if t.Checked {
			continue
		}
		at, hasTime, ok := t.Due.Time()
		if !ok {
			continue
		}
		offs := defs.Tasks
		if !hasTime {
			offs = defs.TasksAllDay
			at = time.Date(at.Year(), at.Month(), at.Day(), 0, 0, 0, 0, time.Local)
		}
		for _, off := range offs {
			fire := remind.FireTime(at, off, !hasTime)
			if fire.After(now) || now.Sub(fire) >= maxDelay {
				continue
			}
			when := "Due " + dayText(at, now)
			if hasTime {
				when = at.Format("15:04") + " · " + relativeDue(at.Sub(now))
				if !sameDay(at, now) {
					when = dayText(at, now) + " " + when
				}
			}
			if p := projects[t.ProjectID]; p != "" {
				when += " · " + p
			}
			out = append(out, Notification{
				Key:   fmt.Sprintf("task|%s|%s|%d", t.ID, t.Due.Date, int(off/time.Minute)),
				Title: "☐ " + t.Content,
				Body:  when,
			})
		}
	}
	return out
}

func relativeDue(d time.Duration) string {
	switch {
	case d > 0:
		return "in " + human(d+time.Minute-1)
	case d > -time.Minute:
		return "due now"
	default:
		return "overdue " + human(-d)
	}
}

// Run shows every due reminder not shown before and returns how many it sent.
func Run(st *store.Store, defs remind.Defaults, now time.Time, send func(Notification) error) (int, error) {
	to := now.Add(remind.MaxLead + 48*time.Hour)
	events, err := st.EventsForRange(now, to)
	if err != nil {
		return 0, err
	}
	// The very first run only records what is already due, so enabling the
	// timer does not replay everything that went off earlier today.
	initialized, err := st.GetState(stateInit)
	if err != nil {
		return 0, err
	}
	if initialized == "" {
		send = func(Notification) error { return nil }
	}
	tasks, err := st.Tasks(false)
	if err != nil {
		return 0, err
	}
	projects, err := st.Projects()
	if err != nil {
		return 0, err
	}
	names := map[string]string{}
	for _, p := range projects {
		names[p.ID] = p.Name
	}
	due := append(Due(recur.Occurrences(events, now, to), defs, now), DueTasks(tasks, names, defs, now)...)
	sent := 0
	for _, n := range due {
		done, err := st.WasNotified(n.Key)
		if err != nil {
			return sent, err
		}
		if done {
			continue
		}
		if err := send(n); err != nil {
			return sent, err
		}
		if err := st.MarkNotified(n.Key, now); err != nil {
			return sent, err
		}
		sent++
	}
	if initialized == "" {
		if err := st.SetState(stateInit, "1"); err != nil {
			return 0, err
		}
		sent = 0
	}
	return sent, st.PruneNotified(now.Add(-3 * maxDelay))
}

func body(o model.Occurrence, now time.Time) string {
	var when string
	if o.Event.AllDay {
		when = "All day · " + dayText(o.Start, now)
		if last := o.End.AddDate(0, 0, -1); !last.Equal(o.Start) {
			when += " – " + last.Format("Mon 2 Jan")
		}
	} else {
		when = o.Start.Format("15:04") + "–" + o.End.Format("15:04")
		if !sameDay(o.Start, now) {
			when = dayText(o.Start, now) + " " + when
		}
		when += " · " + relative(o.Start.Sub(now))
	}
	lines := []string{when}
	if o.Event.Location != "" {
		lines = append(lines, o.Event.Location)
	}
	return strings.Join(lines, "\n")
}

func relative(d time.Duration) string {
	switch {
	case d > 0:
		return "in " + human(d+time.Minute-1)
	case d > -time.Minute:
		return "now"
	default:
		return "started " + human(-d) + " ago"
	}
}

func human(d time.Duration) string {
	d = d.Truncate(time.Minute)
	days, h, m := int(d/(24*time.Hour)), int(d%(24*time.Hour)/time.Hour), int(d%time.Hour/time.Minute)
	var parts []string
	if days > 0 {
		parts = append(parts, fmt.Sprintf("%d d", days))
	}
	if h > 0 {
		parts = append(parts, fmt.Sprintf("%d h", h))
	}
	if m > 0 && days == 0 {
		parts = append(parts, fmt.Sprintf("%d min", m))
	}
	if len(parts) == 0 {
		return "1 min"
	}
	return strings.Join(parts, " ")
}

func dayText(t, now time.Time) string {
	switch {
	case sameDay(t, now):
		return "today"
	case sameDay(t, now.AddDate(0, 0, 1)):
		return "tomorrow"
	}
	return t.Format("Mon 2 Jan")
}

func sameDay(a, b time.Time) bool {
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	return ay == by && am == bm && ad == bd
}
