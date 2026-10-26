// Package store is CalDo's local-first SQLite database. The UI reads and writes
// only through it; local mutations of remote-backed entities are recorded in the
// outbox for the syncer to push.
package store

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"

	"caldo/internal/model"
)

//go:embed schema.sql
var schema string

type Store struct {
	db *sql.DB
}

func Open(path string) (*Store, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// A single connection serialises writers from the UI and the syncer goroutine.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	s := &Store{db: db}
	if err := s.ensureLocalDefaults(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func NewID() string { return uuid.NewString() }

// Local calendar/project ids always exist so CalDo is usable without any provider.
const (
	LocalCalendarID = "local-calendar"
	LocalInboxID    = "local-inbox"
)

func (s *Store) ensureLocalDefaults() error {
	if _, err := s.db.Exec(`INSERT OR IGNORE INTO calendars(id, provider, name, color, sort)
		VALUES (?, 'local', 'Local', '#7dcfff', 1000)`, LocalCalendarID); err != nil {
		return err
	}
	_, err := s.db.Exec(`INSERT OR IGNORE INTO projects(id, provider, name, color, sort, is_inbox)
		VALUES (?, 'local', 'Local', '#7dcfff', 1000, 0)`, LocalInboxID)
	return err
}

func (s *Store) tx(fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

// --- time encoding -------------------------------------------------------

const dateLayout = "2006-01-02"

func encTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339)
}

func decTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.Local()
	}
	if t, err := time.ParseInLocation(dateLayout, s, time.Local); err == nil {
		return t
	}
	return time.Time{}
}

func encEventTime(t time.Time, allDay bool) string {
	if allDay {
		return t.Format(dateLayout)
	}
	return encTime(t)
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func now() string { return encTime(time.Now()) }

// DataVersion changes whenever another connection (another process) commits
// to the database.
func (s *Store) DataVersion() (int64, error) {
	var v int64
	err := s.db.QueryRow(`PRAGMA data_version`).Scan(&v)
	return v, err
}

// --- notifications -------------------------------------------------------

func (s *Store) WasNotified(key string) (bool, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM notified WHERE key = ?`, key).Scan(&n)
	return n > 0, err
}

func (s *Store) MarkNotified(key string, at time.Time) error {
	_, err := s.db.Exec(`INSERT OR IGNORE INTO notified(key, at) VALUES (?, ?)`, key, encTime(at))
	return err
}

// PruneNotified forgets notifications recorded before t.
func (s *Store) PruneNotified(t time.Time) error {
	_, err := s.db.Exec(`DELETE FROM notified WHERE at < ?`, encTime(t))
	return err
}

// --- sync state ----------------------------------------------------------

func (s *Store) GetState(key string) (string, error) {
	var v string
	err := s.db.QueryRow(`SELECT value FROM sync_state WHERE key = ?`, key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return v, err
}

func (s *Store) SetState(key, value string) error {
	_, err := s.db.Exec(`INSERT INTO sync_state(key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

func (s *Store) DeleteState(prefix string) error {
	_, err := s.db.Exec(`DELETE FROM sync_state WHERE key LIKE ?`, prefix+"%")
	return err
}

// --- conflicts -----------------------------------------------------------

func (s *Store) AddConflict(c model.Conflict) error {
	if c.ID == "" {
		c.ID = NewID()
	}
	_, err := s.db.Exec(`INSERT INTO conflicts(id, entity_type, entity_id, title, local_json, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`, c.ID, c.EntityType, c.EntityID, c.Title, c.LocalJSON, now())
	return err
}

func (s *Store) Conflicts() ([]model.Conflict, error) {
	rows, err := s.db.Query(`SELECT id, entity_type, entity_id, title, local_json, created_at
		FROM conflicts ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Conflict
	for rows.Next() {
		var c model.Conflict
		var created string
		if err := rows.Scan(&c.ID, &c.EntityType, &c.EntityID, &c.Title, &c.LocalJSON, &created); err != nil {
			return nil, err
		}
		c.CreatedAt = decTime(created)
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) DeleteConflict(id string) error {
	_, err := s.db.Exec(`DELETE FROM conflicts WHERE id = ?`, id)
	return err
}

func (s *Store) ConflictCount() int {
	var n int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM conflicts`).Scan(&n)
	return n
}
