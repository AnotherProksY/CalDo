// Package quick parses the one-line "quick add" syntax shared by the TUI and
// the `caldo add` command.
package quick

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"caldo/internal/editor"
	"caldo/internal/model"
)

var dueSuffix = regexp.MustCompile(`(?i)\s+(today|tomorrow|tod|tom|сегодня|завтра|послезавтра|next week|` +
	`mon(?:day)?|tue(?:sday)?|wed(?:nesday)?|thu(?:rsday)?|fri(?:day)?|sat(?:urday)?|sun(?:day)?|` +
	`in \d+ (?:days?|weeks?)|every .+|каждый .+|\d{4}-\d{2}-\d{2}(?: \d{1,2}:\d{2})?)$`)

// Task parses "Title #Project @label p1 tomorrow". Project names match
// case-insensitively, with _ standing for spaces. projectID and sectionID
// are used when no #Project is given.
func Task(s string, projects []model.Project, projectID, sectionID string) (*model.Task, error) {
	t := &model.Task{ProjectID: projectID, SectionID: sectionID, Priority: 1}
	var words []string
	for _, w := range strings.Fields(s) {
		switch {
		case strings.HasPrefix(w, "#") && len(w) > 1:
			name := strings.ReplaceAll(w[1:], "_", " ")
			found := false
			for _, p := range projects {
				if strings.EqualFold(p.Name, name) {
					t.ProjectID, t.SectionID, found = p.ID, "", true
				}
			}
			if !found {
				return nil, fmt.Errorf("unknown project %q", name)
			}
		case strings.HasPrefix(w, "@") && len(w) > 1:
			t.Labels = append(t.Labels, w[1:])
		case len(w) == 2 && (w[0] == 'p' || w[0] == 'P') && w[1] >= '1' && w[1] <= '4':
			t.Priority = model.PriorityFromDisplay(int(w[1] - '0'))
		default:
			words = append(words, w)
		}
	}
	content := strings.Join(words, " ")
	if mt := dueSuffix.FindStringSubmatchIndex(content); mt != nil {
		t.Due = editor.ParseDue(content[mt[2]:mt[3]], nil)
		content = strings.TrimSpace(content[:mt[0]])
	}
	if content == "" {
		return nil, errors.New("task title is empty")
	}
	if t.ProjectID == "" {
		return nil, errors.New("no project available")
	}
	t.Content = content
	return t, nil
}

var timePrefix = regexp.MustCompile(`^(\d{1,2}[:.]\d{2})(?:\s*-\s*(\d{1,2}[:.]\d{2}))?\s+(.+)$`)

// timeOnly is a time (range) with no title after it.
var timeOnly = regexp.MustCompile(`^\d{1,2}[:.]\d{2}(?:\s*-\s*\d{1,2}[:.]\d{2})?$`)

// Event parses "[day] [HH:MM[-HH:MM]] Title [#Calendar]". With leadingDay a
// first word that is a date (YYYY-MM-DD, today/tomorrow, a weekday) sets the
// day, otherwise day is used. No time makes an all-day event, a start alone
// lasts dur. calendarID is used when no #Calendar is given; read-only
// calendars are refused.
func Event(s string, day time.Time, leadingDay bool, dur time.Duration, cals []model.Calendar, calendarID string) (*model.Event, error) {
	var words []string
	for _, w := range strings.Fields(s) {
		if strings.HasPrefix(w, "#") && len(w) > 1 {
			name := strings.ReplaceAll(w[1:], "_", " ")
			found := false
			for _, c := range cals {
				if strings.EqualFold(c.Name, name) {
					if c.ReadOnly {
						return nil, fmt.Errorf("calendar %q is read-only", c.Name)
					}
					calendarID, found = c.ID, true
				}
			}
			if !found {
				return nil, fmt.Errorf("unknown calendar %q", name)
			}
			continue
		}
		words = append(words, w)
	}
	if leadingDay && len(words) > 1 {
		if d, ok := editor.ResolveDay(words[0]); ok {
			day, words = d, words[1:]
		}
	}
	text := strings.Join(words, " ")
	if text == "" || timeOnly.MatchString(text) {
		return nil, errors.New("event title is empty")
	}
	if calendarID == "" {
		return nil, errors.New("no writable calendar available")
	}
	day = time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, time.Local)
	e := &model.Event{CalendarID: calendarID, Title: text, Start: day, End: day.AddDate(0, 0, 1), AllDay: true}
	if mt := timePrefix.FindStringSubmatch(text); mt != nil {
		st, err := clockOn(day, mt[1])
		if err != nil {
			return nil, err
		}
		en := st.Add(dur)
		if mt[2] != "" {
			if en, err = clockOn(day, mt[2]); err != nil {
				return nil, err
			}
			if !en.After(st) {
				en = en.AddDate(0, 0, 1) // ends after midnight
			}
		}
		e.Title, e.Start, e.End, e.AllDay = mt[3], st, en, false
	}
	if strings.TrimSpace(e.Title) == "" {
		return nil, errors.New("event title is empty")
	}
	return e, nil
}

func clockOn(day time.Time, hm string) (time.Time, error) {
	t, err := time.Parse("15:04", strings.Replace(hm, ".", ":", 1))
	if err != nil {
		return time.Time{}, fmt.Errorf("bad time %q", hm)
	}
	return time.Date(day.Year(), day.Month(), day.Day(), t.Hour(), t.Minute(), 0, 0, time.Local), nil
}

// DefaultCalendar is the first writable calendar, preferring enabled ones.
func DefaultCalendar(cals []model.Calendar) string {
	for _, c := range cals {
		if c.Enabled && !c.ReadOnly {
			return c.ID
		}
	}
	for _, c := range cals {
		if !c.ReadOnly {
			return c.ID
		}
	}
	return ""
}

// DefaultProject is the enabled inbox, else the first enabled project.
func DefaultProject(projects []model.Project) string {
	for _, p := range projects {
		if p.Enabled && p.IsInbox {
			return p.ID
		}
	}
	for _, p := range projects {
		if p.Enabled {
			return p.ID
		}
	}
	return ""
}
