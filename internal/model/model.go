// Package model defines the domain types shared by storage, sync and UI.
package model

import (
	"slices"
	"time"
)

// Provider identifies where an entity lives remotely.
type Provider string

const (
	ProviderLocal   Provider = "local"
	ProviderGoogle  Provider = "google"
	ProviderTodoist Provider = "todoist"
)

type Calendar struct {
	ID       string
	Provider Provider
	RemoteID string
	Name     string
	Color    string
	Enabled  bool
	ReadOnly bool
	Sort     int
}

// Event is either a single event, a recurring master (Recurrence non-empty)
// or an exception of a master (RecurringEventID + OriginalStart set).
type Event struct {
	ID          string
	CalendarID  string
	RemoteID    string
	ETag        string
	Title       string
	Description string
	Location    string
	// Start/End are in local time. For all-day events they are midnights and End is exclusive.
	Start  time.Time
	End    time.Time
	AllDay bool
	// Timezone is the IANA zone the event was defined in; recurrence expands in it.
	Timezone string
	// Recurrence holds RFC 5545 lines (RRULE/EXDATE/RDATE), one per entry.
	Recurrence       []string
	RecurringEventID string
	OriginalStart    *time.Time
	Status           string // "confirmed", "tentative", "cancelled"
	// Reminders are offsets from Start (negative = before). They are local to
	// CalDo and never synced. nil means the configured defaults (for an
	// exception: the series' reminders); an empty slice means none.
	Reminders []time.Duration
	Dirty     bool
	Deleted   bool
	UpdatedAt time.Time
}

const StatusCancelled = "cancelled"

// SameContent reports whether a and b agree on everything the server stores,
// i.e. a change between them needs no push.
func (e *Event) SameContent(o *Event) bool {
	sameOrig := (e.OriginalStart == nil) == (o.OriginalStart == nil) &&
		(e.OriginalStart == nil || e.OriginalStart.Equal(*o.OriginalStart))
	return e.CalendarID == o.CalendarID && e.Title == o.Title && e.Description == o.Description &&
		e.Location == o.Location && e.Start.Equal(o.Start) && e.End.Equal(o.End) &&
		e.AllDay == o.AllDay && e.Timezone == o.Timezone && slices.Equal(e.Recurrence, o.Recurrence) &&
		e.RecurringEventID == o.RecurringEventID && sameOrig && e.Status == o.Status && e.Deleted == o.Deleted
}

func (e *Event) IsRecurring() bool { return len(e.Recurrence) > 0 }

func (e *Event) IsException() bool { return e.RecurringEventID != "" }

// Occurrence is one displayed instance of an event on the timeline.
type Occurrence struct {
	Event *Event
	Start time.Time
	End   time.Time
	// Master is set when this occurrence was expanded from a recurring series
	// (Event == Master then) or is an exception belonging to one.
	Master *Event
	// InstanceStart is the series slot this occurrence fills (original start).
	InstanceStart time.Time
}

func (o Occurrence) IsRecurring() bool { return o.Master != nil }

type Project struct {
	ID       string
	Provider Provider
	RemoteID string
	Name     string
	Color    string
	ParentID string
	Sort     int
	IsInbox  bool
	Enabled  bool
}

type Section struct {
	ID        string
	ProjectID string
	RemoteID  string
	Name      string
	Sort      int
}

type Label struct {
	ID       string
	RemoteID string
	Name     string
	Color    string
	Sort     int
}

// Priority follows the Todoist API scale: 4 is the highest (shown as p1), 1 is normal (p4).
type Priority int

func (p Priority) Display() int { return 5 - int(p) }

func PriorityFromDisplay(d int) Priority {
	if d < 1 || d > 4 {
		return 1
	}
	return Priority(5 - d)
}

type Due struct {
	// Date is "2006-01-02" or a local datetime "2006-01-02T15:04:05".
	Date        string
	String      string
	IsRecurring bool
	Timezone    string
}

// Time parses Date into local time; ok is false for empty or invalid dates.
func (d *Due) Time() (t time.Time, hasTime bool, ok bool) {
	if d == nil || d.Date == "" {
		return time.Time{}, false, false
	}
	if t, err := time.ParseInLocation("2006-01-02", d.Date, time.Local); err == nil {
		return t, false, true
	}
	for _, layout := range []string{"2006-01-02T15:04:05", time.RFC3339, "2006-01-02T15:04:05Z"} {
		if t, err := time.Parse(layout, d.Date); err == nil {
			if layout == "2006-01-02T15:04:05" {
				t, _ = time.ParseInLocation(layout, d.Date, time.Local)
			}
			return t.Local(), true, true
		}
	}
	return time.Time{}, false, false
}

type Task struct {
	ID          string
	ProjectID   string
	SectionID   string
	ParentID    string
	RemoteID    string
	Content     string
	Description string
	Priority    Priority
	Labels      []string
	Due         *Due
	Deadline    string
	Checked     bool
	Sort        int
	CompletedAt *time.Time
	// RemoteVersion is the server's updated_at seen at last sync; used for conflict detection.
	RemoteVersion string
	Dirty         bool
	Deleted       bool
	UpdatedAt     time.Time
}

// Outbox operations.
type Op string

const (
	OpCreate     Op = "create"
	OpUpdate     Op = "update"
	OpDelete     Op = "delete"
	OpComplete   Op = "complete"
	OpUncomplete Op = "uncomplete"
	OpMove       Op = "move"
)

type EntityType string

const (
	EntityEvent EntityType = "event"
	EntityTask  EntityType = "task"
)

type OutboxEntry struct {
	ID         string
	Provider   Provider
	EntityType EntityType
	EntityID   string
	Op         Op
	CreatedAt  time.Time
	Attempts   int
	LastError  string
}

type Conflict struct {
	ID         string
	EntityType EntityType
	EntityID   string
	Title      string
	// LocalJSON is the discarded local version, restorable by the user.
	LocalJSON string
	CreatedAt time.Time
}
