package store

import (
	"database/sql"

	"caldo/internal/model"
)

// enqueue records a pending push, coalescing with entries already queued for the
// same entity. Because pushes read the entity's current state, one create or update
// entry is enough no matter how many edits happened. It reports purge=true when a
// delete cancelled a not-yet-pushed create, meaning the row can be dropped outright.
func enqueue(tx *sql.Tx, p model.Provider, et model.EntityType, id string, op model.Op) (purge bool, err error) {
	if p == model.ProviderLocal {
		return false, nil
	}
	pending := map[model.Op]bool{}
	rows, err := tx.Query(`SELECT op FROM outbox WHERE entity_type = ? AND entity_id = ?`, et, id)
	if err != nil {
		return false, err
	}
	for rows.Next() {
		var o model.Op
		if err := rows.Scan(&o); err != nil {
			rows.Close()
			return false, err
		}
		pending[o] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return false, err
	}

	del := func(ops ...model.Op) error {
		for _, o := range ops {
			if _, err := tx.Exec(`DELETE FROM outbox WHERE entity_type = ? AND entity_id = ? AND op = ?`, et, id, o); err != nil {
				return err
			}
		}
		return nil
	}

	switch op {
	case model.OpUpdate, model.OpMove:
		if pending[model.OpCreate] || pending[op] {
			return false, nil
		}
	case model.OpComplete, model.OpUncomplete:
		opposite := model.OpUncomplete
		if op == model.OpUncomplete {
			opposite = model.OpComplete
		}
		if pending[opposite] {
			// Toggled back before pushing: both cancel out.
			return false, del(opposite)
		}
		if pending[op] {
			return false, nil
		}
	case model.OpDelete:
		if pending[model.OpCreate] {
			_, err := tx.Exec(`DELETE FROM outbox WHERE entity_type = ? AND entity_id = ?`, et, id)
			return true, err
		}
		if _, err := tx.Exec(`DELETE FROM outbox WHERE entity_type = ? AND entity_id = ?`, et, id); err != nil {
			return false, err
		}
	}

	var seq int64
	if err := tx.QueryRow(`SELECT COALESCE(MAX(seq), 0) + 1 FROM outbox`).Scan(&seq); err != nil {
		return false, err
	}
	_, err = tx.Exec(`INSERT INTO outbox(id, seq, provider, entity_type, entity_id, op, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, NewID(), seq, p, et, id, op, now())
	return false, err
}

// Pending returns queued pushes for a provider in the order they were made.
func (s *Store) Pending(p model.Provider) ([]model.OutboxEntry, error) {
	rows, err := s.db.Query(`SELECT id, provider, entity_type, entity_id, op, created_at, attempts, last_error
		FROM outbox WHERE provider = ? ORDER BY seq`, p)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.OutboxEntry
	for rows.Next() {
		var e model.OutboxEntry
		var created string
		if err := rows.Scan(&e.ID, &e.Provider, &e.EntityType, &e.EntityID, &e.Op, &created, &e.Attempts, &e.LastError); err != nil {
			return nil, err
		}
		e.CreatedAt = decTime(created)
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) PendingCount() int {
	var n int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM outbox`).Scan(&n)
	return n
}

// HasPending reports whether an entity has unpushed local changes.
func (s *Store) HasPending(et model.EntityType, id string) bool {
	var n int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM outbox WHERE entity_type = ? AND entity_id = ?`, et, id).Scan(&n)
	return n > 0
}

func (s *Store) AckOutbox(id string) error {
	_, err := s.db.Exec(`DELETE FROM outbox WHERE id = ?`, id)
	return err
}

func (s *Store) FailOutbox(id string, cause error) error {
	_, err := s.db.Exec(`UPDATE outbox SET attempts = attempts + 1, last_error = ? WHERE id = ?`, cause.Error(), id)
	return err
}

// DropPending removes every queued push for an entity (used when the server wins a conflict).
func (s *Store) DropPending(et model.EntityType, id string) error {
	_, err := s.db.Exec(`DELETE FROM outbox WHERE entity_type = ? AND entity_id = ?`, et, id)
	return err
}
