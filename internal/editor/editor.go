// Package editor round-trips events and tasks through a YAML-frontmatter +
// Markdown document so they can be edited in $EDITOR.
package editor

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"caldo/internal/model"
)

// Lookup resolves the human-readable names used in documents.
type Lookup struct {
	Calendars []model.Calendar
	Projects  []model.Project
	Sections  []model.Section
	Tasks     []*model.Task
}

var ErrEmpty = errors.New("document is empty")

const (
	dateLayout     = "2006-01-02"
	dateTimeLayout = "2006-01-02 15:04"
)

// --- events --------------------------------------------------------------

type eventDoc struct {
	Type       string   `yaml:"type"`
	Title      string   `yaml:"title"`
	Calendar   string   `yaml:"calendar"`
	Start      string   `yaml:"start"`
	End        string   `yaml:"end"`
	AllDay     bool     `yaml:"all_day"`
	Location   string   `yaml:"location"`
	Timezone   string   `yaml:"timezone,omitempty"`
	Recurrence []string `yaml:"recurrence,omitempty"`
}

func EncodeEvent(e *model.Event, l Lookup) []byte {
	d := eventDoc{
		Type:       "event",
		Title:      e.Title,
		Calendar:   calendarName(l, e.CalendarID),
		AllDay:     e.AllDay,
		Location:   e.Location,
		Timezone:   e.Timezone,
		Recurrence: e.Recurrence,
	}
	if e.AllDay {
		d.Start = e.Start.Format(dateLayout)
		// Documents show an inclusive end date; storage keeps it exclusive.
		d.End = e.End.AddDate(0, 0, -1).Format(dateLayout)
	} else {
		d.Start = e.Start.Format(dateTimeLayout)
		d.End = e.End.Format(dateTimeLayout)
	}
	var hdr strings.Builder
	hdr.WriteString("# calendars: ")
	var names []string
	for _, c := range l.Calendars {
		if !c.ReadOnly {
			names = append(names, c.Name)
		}
	}
	hdr.WriteString(strings.Join(names, ", "))
	hdr.WriteString("\n# start/end: YYYY-MM-DD HH:MM (timed) or YYYY-MM-DD (all_day, end inclusive)\n")
	hdr.WriteString("# recurrence: RFC 5545 lines, e.g. - RRULE:FREQ=WEEKLY;BYDAY=MO,WE\n")
	return render(hdr.String(), d, e.Description)
}

// DecodeEvent applies a document onto a copy of base and returns it.
func DecodeEvent(b []byte, base *model.Event, l Lookup) (*model.Event, error) {
	var d eventDoc
	body, err := parse(b, &d)
	if err != nil {
		return nil, err
	}
	e := *base
	e.Title = strings.TrimSpace(d.Title)
	if e.Title == "" {
		return nil, errors.New("title is required")
	}
	if d.Calendar != "" {
		id, ok := calendarID(l, d.Calendar)
		if !ok {
			return nil, fmt.Errorf("unknown calendar %q", d.Calendar)
		}
		e.CalendarID = id
	}
	e.AllDay = d.AllDay
	e.Location = strings.TrimSpace(d.Location)
	e.Timezone = strings.TrimSpace(d.Timezone)
	e.Description = body
	e.Recurrence = nil
	for _, r := range d.Recurrence {
		if r = strings.TrimSpace(r); r != "" {
			e.Recurrence = append(e.Recurrence, r)
		}
	}
	if e.AllDay {
		st, err := time.ParseInLocation(dateLayout, strings.TrimSpace(d.Start), time.Local)
		if err != nil {
			return nil, fmt.Errorf("start: expected YYYY-MM-DD for all-day events")
		}
		en := st
		if strings.TrimSpace(d.End) != "" {
			if en, err = time.ParseInLocation(dateLayout, strings.TrimSpace(d.End), time.Local); err != nil {
				return nil, fmt.Errorf("end: expected YYYY-MM-DD for all-day events")
			}
		}
		if en.Before(st) {
			return nil, errors.New("end is before start")
		}
		e.Start, e.End = st, en.AddDate(0, 0, 1)
	} else {
		st, err := parseDateTime(d.Start)
		if err != nil {
			return nil, fmt.Errorf("start: %w", err)
		}
		en, err := parseDateTime(d.End)
		if err != nil {
			return nil, fmt.Errorf("end: %w", err)
		}
		if en.Before(st) {
			return nil, errors.New("end is before start")
		}
		e.Start, e.End = st, en
	}
	return &e, nil
}

func parseDateTime(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	for _, layout := range []string{dateTimeLayout, "2006-01-02T15:04", "2006-01-02 15:04:05"} {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("expected YYYY-MM-DD HH:MM, got %q", s)
}

// --- tasks ---------------------------------------------------------------

type taskDoc struct {
	Type     string   `yaml:"type"`
	Title    string   `yaml:"title"`
	Project  string   `yaml:"project"`
	Section  string   `yaml:"section"`
	Parent   string   `yaml:"parent"`
	Priority string   `yaml:"priority"`
	Labels   []string `yaml:"labels"`
	Due      string   `yaml:"due"`
	Deadline string   `yaml:"deadline"`
	Done     bool     `yaml:"done"`
}

func EncodeTask(t *model.Task, l Lookup) []byte {
	d := taskDoc{
		Type:     "task",
		Title:    t.Content,
		Project:  projectName(l, t.ProjectID),
		Section:  sectionName(l, t.SectionID),
		Parent:   taskTitle(l, t.ParentID),
		Priority: fmt.Sprintf("p%d", t.Priority.Display()),
		Labels:   t.Labels,
		Deadline: t.Deadline,
		Done:     t.Checked,
	}
	if d.Labels == nil {
		d.Labels = []string{}
	}
	if t.Due != nil {
		d.Due = DueText(t.Due)
	}
	var hdr strings.Builder
	var names []string
	for _, p := range l.Projects {
		names = append(names, p.Name)
	}
	hdr.WriteString("# projects: " + strings.Join(names, ", ") + "\n")
	hdr.WriteString("# priority: p1 (highest) .. p4; due: YYYY-MM-DD [HH:MM] or natural language (\"every monday\")\n")
	hdr.WriteString("# parent: title of the parent task in the same project (empty = top level)\n")
	return render(hdr.String(), d, t.Description)
}

// DueText renders a due date the way a user would type it back.
func DueText(d *model.Due) string {
	if d.IsRecurring && d.String != "" {
		return d.String
	}
	if t, hasTime, ok := d.Time(); ok {
		if hasTime {
			return t.Format(dateTimeLayout)
		}
		return t.Format(dateLayout)
	}
	return d.String
}

func DecodeTask(b []byte, base *model.Task, l Lookup) (*model.Task, error) {
	var d taskDoc
	body, err := parse(b, &d)
	if err != nil {
		return nil, err
	}
	t := *base
	t.Content = strings.TrimSpace(d.Title)
	if t.Content == "" {
		return nil, errors.New("title is required")
	}
	if d.Project != "" {
		id, ok := projectID(l, d.Project)
		if !ok {
			return nil, fmt.Errorf("unknown project %q", d.Project)
		}
		t.ProjectID = id
	}
	t.SectionID = ""
	if s := strings.TrimSpace(d.Section); s != "" {
		id, ok := sectionID(l, t.ProjectID, s)
		if !ok {
			return nil, fmt.Errorf("unknown section %q in project", s)
		}
		t.SectionID = id
	}
	t.ParentID = ""
	if p := strings.TrimSpace(d.Parent); p != "" {
		id, ok := parentID(l, t.ProjectID, p, t.ID)
		if !ok {
			return nil, fmt.Errorf("unknown parent task %q in project", p)
		}
		t.ParentID = id
	}
	pr, err := ParsePriority(d.Priority)
	if err != nil {
		return nil, err
	}
	t.Priority = pr
	t.Labels = nil
	for _, lb := range d.Labels {
		if lb = strings.TrimPrefix(strings.TrimSpace(lb), "@"); lb != "" {
			t.Labels = append(t.Labels, lb)
		}
	}
	t.Due = ParseDue(d.Due, t.Due)
	t.Deadline = strings.TrimSpace(d.Deadline)
	if t.Deadline != "" {
		if _, err := time.Parse(dateLayout, t.Deadline); err != nil {
			return nil, fmt.Errorf("deadline: expected YYYY-MM-DD")
		}
	}
	if t.Checked != d.Done {
		t.Checked = d.Done
		if !d.Done {
			t.CompletedAt = nil
		}
	}
	t.Description = body
	return &t, nil
}

func ParsePriority(s string) (model.Priority, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return 1, nil
	}
	var n int
	if _, err := fmt.Sscanf(strings.TrimPrefix(s, "p"), "%d", &n); err != nil || n < 1 || n > 4 {
		return 0, fmt.Errorf("priority: expected p1..p4, got %q", s)
	}
	return model.PriorityFromDisplay(n), nil
}

// ParseDue interprets user input. Exact dates are stored directly; anything else
// is kept as a natural-language string for Todoist to resolve, with a few common
// words resolved locally so local tasks still get a date.
func ParseDue(s string, prev *model.Due) *model.Due {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	if prev != nil && DueText(prev) == s {
		return prev
	}
	if t, err := time.ParseInLocation(dateLayout, s, time.Local); err == nil {
		return &model.Due{Date: t.Format(dateLayout)}
	}
	if t, err := parseDateTime(s); err == nil {
		return &model.Due{Date: t.Format("2006-01-02T15:04:05")}
	}
	d := &model.Due{String: s}
	if t, ok := resolveWord(s, time.Now()); ok {
		d.Date = t.Format(dateLayout)
	}
	d.IsRecurring = strings.HasPrefix(strings.ToLower(s), "every") || strings.HasPrefix(strings.ToLower(s), "каждый")
	return d
}

func resolveWord(s string, now time.Time) (time.Time, bool) {
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	switch strings.ToLower(s) {
	case "today", "tod", "сегодня":
		return today, true
	case "tomorrow", "tom", "завтра":
		return today.AddDate(0, 0, 1), true
	case "послезавтра":
		return today.AddDate(0, 0, 2), true
	}
	days := map[string]time.Weekday{
		"monday": time.Monday, "tuesday": time.Tuesday, "wednesday": time.Wednesday, "thursday": time.Thursday,
		"friday": time.Friday, "saturday": time.Saturday, "sunday": time.Sunday,
		"mon": time.Monday, "tue": time.Tuesday, "wed": time.Wednesday, "thu": time.Thursday,
		"fri": time.Friday, "sat": time.Saturday, "sun": time.Sunday,
	}
	if wd, ok := days[strings.ToLower(s)]; ok {
		n := (int(wd) - int(today.Weekday()) + 7) % 7
		if n == 0 {
			n = 7
		}
		return today.AddDate(0, 0, n), true
	}
	return time.Time{}, false
}

// --- document format -----------------------------------------------------

func render(header string, front any, body string) []byte {
	var buf bytes.Buffer
	buf.WriteString("---\n")
	buf.WriteString(header)
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	_ = enc.Encode(front)
	enc.Close()
	buf.WriteString("---\n")
	buf.WriteString(body)
	if body != "" && !strings.HasSuffix(body, "\n") {
		buf.WriteString("\n")
	}
	return buf.Bytes()
}

func parse(b []byte, front any) (body string, err error) {
	s := strings.ReplaceAll(string(b), "\r\n", "\n")
	if strings.TrimSpace(s) == "" {
		return "", ErrEmpty
	}
	if !strings.HasPrefix(s, "---\n") {
		return "", errors.New("missing opening '---' frontmatter line")
	}
	rest := s[len("---\n"):]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return "", errors.New("missing closing '---' frontmatter line")
	}
	if err := yaml.Unmarshal([]byte(rest[:end]), front); err != nil {
		return "", fmt.Errorf("frontmatter: %w", err)
	}
	body = rest[end+len("\n---"):]
	body = strings.TrimPrefix(body, "\n")
	return strings.TrimRight(body, "\n"), nil
}

// --- name lookups --------------------------------------------------------

func calendarName(l Lookup, id string) string {
	for _, c := range l.Calendars {
		if c.ID == id {
			return c.Name
		}
	}
	return ""
}

func calendarID(l Lookup, name string) (string, bool) {
	for _, c := range l.Calendars {
		if strings.EqualFold(c.Name, name) && !c.ReadOnly {
			return c.ID, true
		}
	}
	return "", false
}

func projectName(l Lookup, id string) string {
	for _, p := range l.Projects {
		if p.ID == id {
			return p.Name
		}
	}
	return ""
}

func projectID(l Lookup, name string) (string, bool) {
	for _, p := range l.Projects {
		if strings.EqualFold(p.Name, name) {
			return p.ID, true
		}
	}
	return "", false
}

func sectionName(l Lookup, id string) string {
	for _, s := range l.Sections {
		if s.ID == id {
			return s.Name
		}
	}
	return ""
}

func sectionID(l Lookup, projectID, name string) (string, bool) {
	for _, s := range l.Sections {
		if s.ProjectID == projectID && strings.EqualFold(s.Name, name) {
			return s.ID, true
		}
	}
	return "", false
}

func taskTitle(l Lookup, id string) string {
	for _, t := range l.Tasks {
		if t.ID == id {
			return t.Content
		}
	}
	return ""
}

func parentID(l Lookup, projectID, title, self string) (string, bool) {
	for _, t := range l.Tasks {
		if t.ProjectID == projectID && t.ID != self && strings.EqualFold(t.Content, title) {
			return t.ID, true
		}
	}
	return "", false
}

// --- launching -----------------------------------------------------------

// WriteTemp writes doc to a new temp file and returns its path.
func WriteTemp(doc []byte) (string, error) {
	f, err := os.CreateTemp("", "caldo-*.md")
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := f.Write(doc); err != nil {
		return "", err
	}
	return f.Name(), nil
}

// Command builds the editor invocation: override, then $VISUAL, $EDITOR, vi.
func Command(override, file string) *exec.Cmd {
	ed := override
	for _, env := range []string{"VISUAL", "EDITOR"} {
		if ed == "" {
			ed = os.Getenv(env)
		}
	}
	if ed == "" {
		ed = "vi"
	}
	// Allow editors configured with arguments, e.g. "code --wait".
	parts := strings.Fields(ed)
	return exec.Command(parts[0], append(parts[1:], file)...)
}
