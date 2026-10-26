// Package remind parses reminder offsets and works out when they fire.
//
// A reminder is an offset from the event start. Timed events use durations
// before the start ("10m", "1h30m", "1d"); all-day events use a clock time on
// the day, optionally shifted by whole days ("09:00", "-1d 18:00").
package remind

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"caldo/internal/model"
)

const day = 24 * time.Hour

// MaxLead bounds how far ahead of an event a reminder may fire.
const MaxLead = 4 * 7 * day

var (
	durRe   = regexp.MustCompile(`^(?:(\d+)w)?(?:(\d+)d)?(?:(\d+)h)?(?:(\d+)m)?$`)
	clockRe = regexp.MustCompile(`^(?:-(\d+)d\s+)?(\d{1,2}):(\d{2})$`)
)

// Parse reads one reminder for a timed or all-day event.
func Parse(s string, allDay bool) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if allDay {
		m := clockRe.FindStringSubmatch(s)
		if m == nil {
			return 0, fmt.Errorf("all-day reminder %q: expected HH:MM or -Nd HH:MM", s)
		}
		days, _ := strconv.Atoi(m[1])
		h, _ := strconv.Atoi(m[2])
		mi, _ := strconv.Atoi(m[3])
		if h > 23 || mi > 59 {
			return 0, fmt.Errorf("all-day reminder %q: bad time", s)
		}
		off := time.Duration(h)*time.Hour + time.Duration(mi)*time.Minute - time.Duration(days)*day
		if -off > MaxLead {
			return 0, fmt.Errorf("reminder %q is more than 4 weeks ahead", s)
		}
		return off, nil
	}
	m := durRe.FindStringSubmatch(s)
	if s == "" || m == nil {
		return 0, fmt.Errorf("reminder %q: expected a duration like 10m, 1h30m, 1d", s)
	}
	var d time.Duration
	for i, unit := range []time.Duration{7 * day, day, time.Hour, time.Minute} {
		n, _ := strconv.Atoi(m[i+1])
		d += time.Duration(n) * unit
	}
	if d > MaxLead {
		return 0, fmt.Errorf("reminder %q is more than 4 weeks ahead", s)
	}
	return -d, nil
}

// ParseList parses every entry, returning a non-nil slice.
func ParseList(ss []string, allDay bool) ([]time.Duration, error) {
	out := []time.Duration{}
	for _, s := range ss {
		d, err := Parse(s, allDay)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, nil
}

// Format renders an offset the way Parse reads it back.
func Format(off time.Duration, allDay bool) string {
	if allDay {
		days := 0
		for off < 0 {
			off += day
			days++
		}
		hm := fmt.Sprintf("%02d:%02d", int(off/time.Hour), int(off%time.Hour/time.Minute))
		if days > 0 {
			return fmt.Sprintf("-%dd %s", days, hm)
		}
		return hm
	}
	d := -off
	if d <= 0 {
		return "0m"
	}
	var b strings.Builder
	for _, u := range []struct {
		unit time.Duration
		sfx  string
	}{{7 * day, "w"}, {day, "d"}, {time.Hour, "h"}, {time.Minute, "m"}} {
		if n := d / u.unit; n > 0 {
			fmt.Fprintf(&b, "%d%s", n, u.sfx)
			d -= n * u.unit
		}
	}
	return b.String()
}

// FormatList renders offsets as strings.
func FormatList(offs []time.Duration, allDay bool) []string {
	out := make([]string, len(offs))
	for i, o := range offs {
		out[i] = Format(o, allDay)
	}
	return out
}

// Defaults are the configured reminders for events that do not set their own.
type Defaults struct {
	Timed  []time.Duration
	AllDay []time.Duration
	// Tasks apply to tasks due at a time, TasksAllDay to date-only tasks.
	Tasks       []time.Duration
	TasksAllDay []time.Duration
}

// For returns the reminders that apply to an occurrence.
func (d Defaults) For(o model.Occurrence) []time.Duration {
	if o.Event.Reminders != nil {
		return o.Event.Reminders
	}
	if o.Master != nil && o.Master.Reminders != nil {
		return o.Master.Reminders
	}
	if o.Event.AllDay {
		return d.AllDay
	}
	return d.Timed
}

// FireTime is when a reminder with offset off goes off for an occurrence
// starting at start. All-day offsets are calendar days plus a clock time, so
// they stay on the wall clock across DST changes.
func FireTime(start time.Time, off time.Duration, allDay bool) time.Time {
	if !allDay {
		return start.Add(off)
	}
	days := 0
	for off < 0 {
		off += day
		days++
	}
	d := time.Date(start.Year(), start.Month(), start.Day()-days, 0, 0, 0, 0, start.Location())
	return d.Add(off)
}
