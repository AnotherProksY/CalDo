package store

import (
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"

	"caldo/internal/model"
)

// --- calendars -----------------------------------------------------------

const calendarCols = `id, provider, remote_id, name, color, enabled, readonly, sort`

func scanCalendar(sc interface{ Scan(...any) error }) (model.Calendar, error) {
	var c model.Calendar
	var enabled, ro int
	err := sc.Scan(&c.ID, &c.Provider, &c.RemoteID, &c.Name, &c.Color, &enabled, &ro, &c.Sort)
	c.Enabled, c.ReadOnly = enabled == 1, ro == 1
	return c, err
}

func (s *Store) Calendars() ([]model.Calendar, error) {
	rows, err := s.db.Query(`SELECT ` + calendarCols + ` FROM calendars ORDER BY sort, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Calendar
	for rows.Next() {
		c, err := scanCalendar(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) Calendar(id string) (model.Calendar, error) {
	return scanCalendar(s.db.QueryRow(`SELECT `+calendarCols+` FROM calendars WHERE id = ?`, id))
}

// UpsertRemoteCalendar inserts or refreshes a provider calendar, keeping the
// user's enabled flag for calendars already known.
func (s *Store) UpsertRemoteCalendar(c model.Calendar) (model.Calendar, error) {
	var id string
	err := s.db.QueryRow(`SELECT id FROM calendars WHERE provider = ? AND remote_id = ?`, c.Provider, c.RemoteID).Scan(&id)
	switch {
	case err == sql.ErrNoRows:
		c.ID = NewID()
		_, err = s.db.Exec(`INSERT INTO calendars(`+calendarCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			c.ID, c.Provider, c.RemoteID, c.Name, c.Color, boolInt(c.Enabled), boolInt(c.ReadOnly), c.Sort)
		return c, err
	case err != nil:
		return c, err
	}
	c.ID = id
	_, err = s.db.Exec(`UPDATE calendars SET name = ?, color = ?, readonly = ?, sort = ? WHERE id = ?`,
		c.Name, c.Color, boolInt(c.ReadOnly), c.Sort, id)
	if err != nil {
		return c, err
	}
	return s.Calendar(id)
}

func (s *Store) SetCalendarEnabled(id string, enabled bool) error {
	_, err := s.db.Exec(`UPDATE calendars SET enabled = ? WHERE id = ?`, boolInt(enabled), id)
	return err
}

// DeleteCalendar drops a calendar locally with its events and their queued
// pushes (used when a calendar is removed remotely or excluded from sync).
func (s *Store) DeleteCalendar(id string) error {
	return s.tx(func(tx *sql.Tx) error {
		if _, err := tx.Exec(`DELETE FROM outbox WHERE entity_type = 'event' AND entity_id IN
			(SELECT id FROM events WHERE calendar_id = ?)`, id); err != nil {
			return err
		}
		_, err := tx.Exec(`DELETE FROM calendars WHERE id = ?`, id)
		return err
	})
}

func calendarProvider(q interface {
	QueryRow(string, ...any) *sql.Row
}, calID string) (model.Provider, error) {
	var p model.Provider
	err := q.QueryRow(`SELECT provider FROM calendars WHERE id = ?`, calID).Scan(&p)
	if err != nil {
		return "", fmt.Errorf("calendar %s: %w", calID, err)
	}
	return p, nil
}

// --- events --------------------------------------------------------------

const eventCols = `id, calendar_id, remote_id, etag, title, description, location, start, end, all_day,
	timezone, recurrence, recurring_event_id, original_start, status, dirty, deleted, updated_at`

// eventSelect reads events with their local reminders.
const eventSelect = `SELECT ` + eventCols + `, r.reminders FROM events
	LEFT JOIN event_reminders r ON r.event_id = events.id`

func scanEvent(sc interface{ Scan(...any) error }) (*model.Event, error) {
	var e model.Event
	var start, end, rec, orig, updated string
	var allDay, dirty, deleted int
	var rems sql.NullString
	err := sc.Scan(&e.ID, &e.CalendarID, &e.RemoteID, &e.ETag, &e.Title, &e.Description, &e.Location,
		&start, &end, &allDay, &e.Timezone, &rec, &e.RecurringEventID, &orig, &e.Status, &dirty, &deleted, &updated, &rems)
	if err != nil {
		return nil, err
	}
	if rems.Valid {
		e.Reminders = decReminders(rems.String)
	}
	e.AllDay, e.Dirty, e.Deleted = allDay == 1, dirty == 1, deleted == 1
	e.Start, e.End = decTime(start), decTime(end)
	if rec != "" {
		e.Recurrence = strings.Split(rec, "\n")
	}
	if orig != "" {
		t := decTime(orig)
		e.OriginalStart = &t
	}
	e.UpdatedAt = decTime(updated)
	return &e, nil
}

func (s *Store) queryEvents(where string, args ...any) ([]*model.Event, error) {
	rows, err := s.db.Query(eventSelect+` WHERE `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*model.Event
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) Event(id string) (*model.Event, error) {
	return scanEvent(s.db.QueryRow(eventSelect+` WHERE id = ?`, id))
}

// EventByRemoteID returns nil, nil when not found.
func (s *Store) EventByRemoteID(calID, remoteID string) (*model.Event, error) {
	e, err := scanEvent(s.db.QueryRow(eventSelect+` WHERE calendar_id = ? AND remote_id = ?`, calID, remoteID))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return e, err
}

// EventsForRange returns raw rows relevant to [from, to) in enabled calendars:
// single events overlapping the range, all recurring masters, and all exceptions.
// Callers expand recurrences with package recur.
func (s *Store) EventsForRange(from, to time.Time) ([]*model.Event, error) {
	// Compare as text is unreliable across offsets, so filter singles in Go.
	all, err := s.queryEvents(`deleted = 0 AND calendar_id IN (SELECT id FROM calendars WHERE enabled = 1)`)
	if err != nil {
		return nil, err
	}
	out := all[:0]
	for _, e := range all {
		if e.IsRecurring() || e.IsException() || (e.Start.Before(to) && e.End.After(from)) || (e.Start.Equal(from) && e.End.Equal(from)) {
			out = append(out, e)
		}
	}
	return out, nil
}

func (s *Store) EventsInCalendar(calID string) ([]*model.Event, error) {
	return s.queryEvents(`calendar_id = ?`, calID)
}

func (s *Store) Exceptions(masterID string) ([]*model.Event, error) {
	return s.queryEvents(`recurring_event_id = ?`, masterID)
}

func writeEvent(tx *sql.Tx, e *model.Event) error {
	var orig string
	if e.OriginalStart != nil {
		orig = encEventTime(*e.OriginalStart, e.AllDay)
	}
	if e.Status == "" {
		e.Status = "confirmed"
	}
	_, err := tx.Exec(`INSERT INTO events(`+eventCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET calendar_id=excluded.calendar_id, remote_id=excluded.remote_id,
		etag=excluded.etag, title=excluded.title, description=excluded.description, location=excluded.location,
		start=excluded.start, end=excluded.end, all_day=excluded.all_day, timezone=excluded.timezone,
		recurrence=excluded.recurrence, recurring_event_id=excluded.recurring_event_id,
		original_start=excluded.original_start, status=excluded.status, dirty=excluded.dirty,
		deleted=excluded.deleted, updated_at=excluded.updated_at`,
		e.ID, e.CalendarID, e.RemoteID, e.ETag, e.Title, e.Description, e.Location,
		encEventTime(e.Start, e.AllDay), encEventTime(e.End, e.AllDay), boolInt(e.AllDay), e.Timezone,
		strings.Join(e.Recurrence, "\n"), e.RecurringEventID, orig, e.Status,
		boolInt(e.Dirty), boolInt(e.Deleted), encTime(e.UpdatedAt))
	return err
}

func writeReminders(tx *sql.Tx, e *model.Event) error {
	if e.Reminders == nil {
		_, err := tx.Exec(`DELETE FROM event_reminders WHERE event_id = ?`, e.ID)
		return err
	}
	_, err := tx.Exec(`INSERT INTO event_reminders(event_id, reminders) VALUES (?, ?)
		ON CONFLICT(event_id) DO UPDATE SET reminders = excluded.reminders`, e.ID, encReminders(e.Reminders))
	return err
}

func encReminders(rs []time.Duration) string {
	parts := make([]string, len(rs))
	for i, r := range rs {
		parts[i] = strconv.Itoa(int(r / time.Minute))
	}
	return strings.Join(parts, ",")
}

func decReminders(s string) []time.Duration {
	out := []time.Duration{}
	for _, p := range strings.Split(s, ",") {
		if n, err := strconv.Atoi(strings.TrimSpace(p)); err == nil {
			out = append(out, time.Duration(n)*time.Minute)
		}
	}
	return out
}

// SetEventReminders changes only an event's local reminders (nil = defaults).
func (s *Store) SetEventReminders(id string, rs []time.Duration) error {
	return s.tx(func(tx *sql.Tx) error { return writeReminders(tx, &model.Event{ID: id, Reminders: rs}) })
}

// SaveEvent stores a local create or edit and queues it for push.
func (s *Store) SaveEvent(e *model.Event) error {
	return s.tx(func(tx *sql.Tx) error {
		p, err := calendarProvider(tx, e.CalendarID)
		if err != nil {
			return err
		}
		op := model.OpUpdate
		if e.ID == "" {
			e.ID = NewID()
			op = model.OpCreate
		} else {
			old, err := scanEvent(tx.QueryRow(eventSelect+` WHERE id = ?`, e.ID))
			if err == sql.ErrNoRows {
				op = model.OpCreate
			} else if err != nil {
				return err
			} else if old.CalendarID != e.CalendarID {
				// Moving between calendars: delete from old, create in new.
				return s.moveEventCalendar(tx, e, old.CalendarID, p)
			} else if old.SameContent(e) {
				// Only local fields (reminders) changed: nothing to push.
				return writeReminders(tx, e)
			}
		}
		e.Dirty = p != model.ProviderLocal
		e.UpdatedAt = time.Now()
		if err := writeEvent(tx, e); err != nil {
			return err
		}
		if err := writeReminders(tx, e); err != nil {
			return err
		}
		_, err = enqueue(tx, p, model.EntityEvent, e.ID, op)
		return err
	})
}

func (s *Store) moveEventCalendar(tx *sql.Tx, e *model.Event, oldCal string, newP model.Provider) error {
	oldP, err := calendarProvider(tx, oldCal)
	if err != nil {
		return err
	}
	old := *e
	old.CalendarID = oldCal
	if err := s.deleteEventTx(tx, e.ID, oldP); err != nil {
		return err
	}
	e.ID, e.RemoteID, e.ETag = NewID(), "", ""
	e.Dirty = newP != model.ProviderLocal
	e.UpdatedAt = time.Now()
	if err := writeEvent(tx, e); err != nil {
		return err
	}
	if err := writeReminders(tx, e); err != nil {
		return err
	}
	_, err = enqueue(tx, newP, model.EntityEvent, e.ID, model.OpCreate)
	return err
}

// DeleteEvent removes an event locally and queues the remote delete. Deleting
// a recurring master also removes its exceptions.
func (s *Store) DeleteEvent(id string) error {
	return s.tx(func(tx *sql.Tx) error {
		var calID string
		if err := tx.QueryRow(`SELECT calendar_id FROM events WHERE id = ?`, id).Scan(&calID); err != nil {
			return err
		}
		p, err := calendarProvider(tx, calID)
		if err != nil {
			return err
		}
		return s.deleteEventTx(tx, id, p)
	})
}

func (s *Store) deleteEventTx(tx *sql.Tx, id string, p model.Provider) error {
	// Exceptions vanish with their master on the server, so drop them locally only.
	if _, err := tx.Exec(`DELETE FROM outbox WHERE entity_type = 'event' AND entity_id IN
		(SELECT id FROM events WHERE recurring_event_id = ?)`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM events WHERE recurring_event_id = ?`, id); err != nil {
		return err
	}
	var remoteID string
	if err := tx.QueryRow(`SELECT remote_id FROM events WHERE id = ?`, id).Scan(&remoteID); err != nil {
		return err
	}
	purge, err := enqueue(tx, p, model.EntityEvent, id, model.OpDelete)
	if err != nil {
		return err
	}
	if p == model.ProviderLocal || purge || remoteID == "" {
		if _, err := tx.Exec(`DELETE FROM outbox WHERE entity_type = 'event' AND entity_id = ?`, id); err != nil {
			return err
		}
		_, err = tx.Exec(`DELETE FROM events WHERE id = ?`, id)
		return err
	}
	_, err = tx.Exec(`UPDATE events SET deleted = 1, dirty = 1, updated_at = ? WHERE id = ?`, now(), id)
	return err
}

// --- syncer-side writes --------------------------------------------------

// PutRemoteEvent writes server state verbatim (not dirty, nothing queued).
// If e.ID is empty a new local id is assigned.
func (s *Store) PutRemoteEvent(e *model.Event) error {
	if e.ID == "" {
		e.ID = NewID()
	}
	e.Dirty, e.Deleted = false, false
	if e.UpdatedAt.IsZero() {
		e.UpdatedAt = time.Now()
	}
	return s.tx(func(tx *sql.Tx) error { return writeEvent(tx, e) })
}

// MarkEventPushed records the server identity after a successful push.
func (s *Store) MarkEventPushed(id, remoteID, etag string) error {
	_, err := s.db.Exec(`UPDATE events SET remote_id = ?, etag = ?,
		dirty = (SELECT COUNT(*) > 0 FROM outbox WHERE entity_type = 'event' AND entity_id = ?) WHERE id = ?`,
		remoteID, etag, id, id)
	return err
}

// PurgeEvent hard-deletes an event and its exceptions without queuing anything.
func (s *Store) PurgeEvent(id string) error {
	return s.tx(func(tx *sql.Tx) error {
		if _, err := tx.Exec(`DELETE FROM events WHERE recurring_event_id = ?`, id); err != nil {
			return err
		}
		_, err := tx.Exec(`DELETE FROM events WHERE id = ?`, id)
		return err
	})
}
