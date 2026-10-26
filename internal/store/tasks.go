package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"caldo/internal/model"
)

// --- projects / sections / labels ---------------------------------------

const projectCols = `id, provider, remote_id, name, color, parent_id, sort, is_inbox, enabled`

func scanProject(sc interface{ Scan(...any) error }) (model.Project, error) {
	var p model.Project
	var inbox, enabled int
	err := sc.Scan(&p.ID, &p.Provider, &p.RemoteID, &p.Name, &p.Color, &p.ParentID, &p.Sort, &inbox, &enabled)
	p.IsInbox, p.Enabled = inbox == 1, enabled == 1
	return p, err
}

// Projects are ordered inbox first, then by sort order.
func (s *Store) Projects() ([]model.Project, error) {
	rows, err := s.db.Query(`SELECT ` + projectCols + ` FROM projects ORDER BY is_inbox DESC, sort, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Project
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) Project(id string) (model.Project, error) {
	return scanProject(s.db.QueryRow(`SELECT `+projectCols+` FROM projects WHERE id = ?`, id))
}

func (s *Store) ProjectByRemoteID(p model.Provider, remoteID string) (model.Project, bool, error) {
	pr, err := scanProject(s.db.QueryRow(`SELECT `+projectCols+` FROM projects WHERE provider = ? AND remote_id = ?`, p, remoteID))
	if err == sql.ErrNoRows {
		return pr, false, nil
	}
	return pr, err == nil, err
}

// PutRemoteProject upserts a provider project by remote id, preserving the enabled flag.
func (s *Store) PutRemoteProject(p model.Project) (model.Project, error) {
	existing, ok, err := s.ProjectByRemoteID(p.Provider, p.RemoteID)
	if err != nil {
		return p, err
	}
	if ok {
		p.ID, p.Enabled = existing.ID, existing.Enabled
		_, err = s.db.Exec(`UPDATE projects SET name=?, color=?, parent_id=?, sort=?, is_inbox=? WHERE id=?`,
			p.Name, p.Color, p.ParentID, p.Sort, boolInt(p.IsInbox), p.ID)
		return p, err
	}
	p.ID, p.Enabled = NewID(), true
	_, err = s.db.Exec(`INSERT INTO projects(`+projectCols+`) VALUES (?,?,?,?,?,?,?,?,?)`,
		p.ID, p.Provider, p.RemoteID, p.Name, p.Color, p.ParentID, p.Sort, boolInt(p.IsInbox), 1)
	return p, err
}

// DeleteProject removes a project and its tasks and sections locally.
func (s *Store) DeleteProject(id string) error {
	return s.tx(func(tx *sql.Tx) error {
		for _, q := range []string{
			`DELETE FROM tasks WHERE project_id = ?`,
			`DELETE FROM sections WHERE project_id = ?`,
			`DELETE FROM projects WHERE id = ?`,
		} {
			if _, err := tx.Exec(q, id); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) SetProjectEnabled(id string, enabled bool) error {
	_, err := s.db.Exec(`UPDATE projects SET enabled = ? WHERE id = ?`, boolInt(enabled), id)
	return err
}

func (s *Store) Sections() ([]model.Section, error) {
	rows, err := s.db.Query(`SELECT id, project_id, remote_id, name, sort FROM sections ORDER BY sort, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Section
	for rows.Next() {
		var sc model.Section
		if err := rows.Scan(&sc.ID, &sc.ProjectID, &sc.RemoteID, &sc.Name, &sc.Sort); err != nil {
			return nil, err
		}
		out = append(out, sc)
	}
	return out, rows.Err()
}

func (s *Store) PutRemoteSection(sc model.Section) (model.Section, error) {
	var id string
	err := s.db.QueryRow(`SELECT id FROM sections WHERE remote_id = ?`, sc.RemoteID).Scan(&id)
	if err == sql.ErrNoRows {
		sc.ID = NewID()
		_, err = s.db.Exec(`INSERT INTO sections(id, project_id, remote_id, name, sort) VALUES (?,?,?,?,?)`,
			sc.ID, sc.ProjectID, sc.RemoteID, sc.Name, sc.Sort)
		return sc, err
	} else if err != nil {
		return sc, err
	}
	sc.ID = id
	_, err = s.db.Exec(`UPDATE sections SET project_id=?, name=?, sort=? WHERE id=?`, sc.ProjectID, sc.Name, sc.Sort, id)
	return sc, err
}

func (s *Store) DeleteSectionByRemoteID(remoteID string) error {
	_, err := s.db.Exec(`DELETE FROM sections WHERE remote_id = ?`, remoteID)
	return err
}

func (s *Store) SectionIDByRemote(remoteID string) string {
	var id string
	_ = s.db.QueryRow(`SELECT id FROM sections WHERE remote_id = ?`, remoteID).Scan(&id)
	return id
}

func (s *Store) Labels() ([]model.Label, error) {
	rows, err := s.db.Query(`SELECT id, remote_id, name, color, sort FROM labels ORDER BY sort, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Label
	for rows.Next() {
		var l model.Label
		if err := rows.Scan(&l.ID, &l.RemoteID, &l.Name, &l.Color, &l.Sort); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func (s *Store) PutRemoteLabel(l model.Label) error {
	var id string
	err := s.db.QueryRow(`SELECT id FROM labels WHERE remote_id = ?`, l.RemoteID).Scan(&id)
	if err == sql.ErrNoRows {
		_, err = s.db.Exec(`INSERT INTO labels(id, remote_id, name, color, sort) VALUES (?,?,?,?,?)`,
			NewID(), l.RemoteID, l.Name, l.Color, l.Sort)
		return err
	} else if err != nil {
		return err
	}
	_, err = s.db.Exec(`UPDATE labels SET name=?, color=?, sort=? WHERE id=?`, l.Name, l.Color, l.Sort, id)
	return err
}

func (s *Store) DeleteLabelByRemoteID(remoteID string) error {
	_, err := s.db.Exec(`DELETE FROM labels WHERE remote_id = ?`, remoteID)
	return err
}

// --- tasks ---------------------------------------------------------------

const taskCols = `id, project_id, section_id, parent_id, remote_id, content, description, priority, labels,
	due_date, due_string, due_recurring, due_timezone, deadline, checked, sort, completed_at,
	remote_version, dirty, deleted, updated_at`

func scanTask(sc interface{ Scan(...any) error }) (*model.Task, error) {
	var t model.Task
	var labels, dueDate, dueString, dueTZ, completed, updated string
	var dueRec, checked, dirty, deleted int
	err := sc.Scan(&t.ID, &t.ProjectID, &t.SectionID, &t.ParentID, &t.RemoteID, &t.Content, &t.Description,
		&t.Priority, &labels, &dueDate, &dueString, &dueRec, &dueTZ, &t.Deadline, &checked, &t.Sort,
		&completed, &t.RemoteVersion, &dirty, &deleted, &updated)
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(labels), &t.Labels)
	if dueDate != "" || dueString != "" {
		t.Due = &model.Due{Date: dueDate, String: dueString, IsRecurring: dueRec == 1, Timezone: dueTZ}
	}
	t.Checked, t.Dirty, t.Deleted = checked == 1, dirty == 1, deleted == 1
	if completed != "" {
		c := decTime(completed)
		t.CompletedAt = &c
	}
	t.UpdatedAt = decTime(updated)
	return &t, nil
}

func (s *Store) queryTasks(where string, args ...any) ([]*model.Task, error) {
	rows, err := s.db.Query(`SELECT `+taskCols+` FROM tasks WHERE `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*model.Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// Tasks returns visible tasks of enabled projects.
func (s *Store) Tasks(includeCompleted bool) ([]*model.Task, error) {
	where := `deleted = 0 AND project_id IN (SELECT id FROM projects WHERE enabled = 1)`
	if !includeCompleted {
		where += ` AND checked = 0`
	}
	return s.queryTasks(where + ` ORDER BY sort, content`)
}

func (s *Store) Task(id string) (*model.Task, error) {
	return scanTask(s.db.QueryRow(`SELECT `+taskCols+` FROM tasks WHERE id = ?`, id))
}

// TaskByRemoteID returns nil, nil when not found.
func (s *Store) TaskByRemoteID(remoteID string) (*model.Task, error) {
	t, err := scanTask(s.db.QueryRow(`SELECT `+taskCols+` FROM tasks WHERE remote_id = ?`, remoteID))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return t, err
}

func (s *Store) TaskIDByRemote(remoteID string) string {
	var id string
	_ = s.db.QueryRow(`SELECT id FROM tasks WHERE remote_id = ?`, remoteID).Scan(&id)
	return id
}

func writeTask(tx *sql.Tx, t *model.Task) error {
	labels, _ := json.Marshal(t.Labels)
	if t.Labels == nil {
		labels = []byte("[]")
	}
	var due model.Due
	if t.Due != nil {
		due = *t.Due
	}
	var completed string
	if t.CompletedAt != nil {
		completed = encTime(*t.CompletedAt)
	}
	if t.Priority < 1 || t.Priority > 4 {
		t.Priority = 1
	}
	_, err := tx.Exec(`INSERT INTO tasks(`+taskCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET project_id=excluded.project_id, section_id=excluded.section_id,
		parent_id=excluded.parent_id, remote_id=excluded.remote_id, content=excluded.content,
		description=excluded.description, priority=excluded.priority, labels=excluded.labels,
		due_date=excluded.due_date, due_string=excluded.due_string, due_recurring=excluded.due_recurring,
		due_timezone=excluded.due_timezone, deadline=excluded.deadline, checked=excluded.checked,
		sort=excluded.sort, completed_at=excluded.completed_at, remote_version=excluded.remote_version,
		dirty=excluded.dirty, deleted=excluded.deleted, updated_at=excluded.updated_at`,
		t.ID, t.ProjectID, t.SectionID, t.ParentID, t.RemoteID, t.Content, t.Description, t.Priority,
		string(labels), due.Date, due.String, boolInt(due.IsRecurring), due.Timezone, t.Deadline,
		boolInt(t.Checked), t.Sort, completed, t.RemoteVersion, boolInt(t.Dirty), boolInt(t.Deleted),
		encTime(t.UpdatedAt))
	return err
}

func projectProvider(q interface {
	QueryRow(string, ...any) *sql.Row
}, projectID string) (model.Provider, error) {
	var p model.Provider
	if err := q.QueryRow(`SELECT provider FROM projects WHERE id = ?`, projectID).Scan(&p); err != nil {
		return "", fmt.Errorf("project %s: %w", projectID, err)
	}
	return p, nil
}

// SaveTask stores a local create or edit and queues the needed pushes.
func (s *Store) SaveTask(t *model.Task) error {
	return s.tx(func(tx *sql.Tx) error {
		p, err := projectProvider(tx, t.ProjectID)
		if err != nil {
			return err
		}
		var old *model.Task
		if t.ID == "" {
			t.ID = NewID()
		} else {
			old, err = scanTask(tx.QueryRow(`SELECT `+taskCols+` FROM tasks WHERE id = ?`, t.ID))
			if err == sql.ErrNoRows {
				old = nil
			} else if err != nil {
				return err
			}
		}
		if old != nil {
			oldP, err := projectProvider(tx, old.ProjectID)
			if err != nil {
				return err
			}
			if oldP != p {
				return fmt.Errorf("moving tasks between %s and %s is not supported", oldP, p)
			}
		}
		if t.Checked && t.CompletedAt == nil {
			n := time.Now()
			t.CompletedAt = &n
		}
		t.Dirty = p != model.ProviderLocal
		t.UpdatedAt = time.Now()
		if err := writeTask(tx, t); err != nil {
			return err
		}
		if old == nil {
			_, err = enqueue(tx, p, model.EntityTask, t.ID, model.OpCreate)
			if err == nil && t.Checked {
				_, err = enqueue(tx, p, model.EntityTask, t.ID, model.OpComplete)
			}
			return err
		}
		if _, err := enqueue(tx, p, model.EntityTask, t.ID, model.OpUpdate); err != nil {
			return err
		}
		if old.ProjectID != t.ProjectID || old.SectionID != t.SectionID || old.ParentID != t.ParentID {
			if _, err := enqueue(tx, p, model.EntityTask, t.ID, model.OpMove); err != nil {
				return err
			}
		}
		if old.Checked != t.Checked {
			op := model.OpUncomplete
			if t.Checked {
				op = model.OpComplete
			}
			_, err = enqueue(tx, p, model.EntityTask, t.ID, op)
		}
		return err
	})
}

// SetTaskChecked completes or reopens a task. Completing a parent completes its subtasks locally,
// matching Todoist behaviour.
func (s *Store) SetTaskChecked(id string, checked bool) error {
	t, err := s.Task(id)
	if err != nil {
		return err
	}
	return s.tx(func(tx *sql.Tx) error {
		p, err := projectProvider(tx, t.ProjectID)
		if err != nil {
			return err
		}
		var completed string
		if checked {
			completed = now()
		}
		dirty := boolInt(p != model.ProviderLocal)
		if _, err := tx.Exec(`UPDATE tasks SET checked = ?, completed_at = ?, dirty = ?, updated_at = ? WHERE id = ?`,
			boolInt(checked), completed, dirty, now(), id); err != nil {
			return err
		}
		if checked {
			if _, err := tx.Exec(`UPDATE tasks SET checked = 1, completed_at = ?, updated_at = ? WHERE parent_id = ?`,
				completed, now(), id); err != nil {
				return err
			}
		}
		op := model.OpUncomplete
		if checked {
			op = model.OpComplete
		}
		_, err = enqueue(tx, p, model.EntityTask, id, op)
		return err
	})
}

// DeleteTask removes a task (and its subtasks) and queues the remote delete.
func (s *Store) DeleteTask(id string) error {
	return s.tx(func(tx *sql.Tx) error {
		var projectID, remoteID string
		if err := tx.QueryRow(`SELECT project_id, remote_id FROM tasks WHERE id = ?`, id).Scan(&projectID, &remoteID); err != nil {
			return err
		}
		p, err := projectProvider(tx, projectID)
		if err != nil {
			return err
		}
		// Subtasks go with the parent on the server; drop their pending work locally.
		if _, err := tx.Exec(`DELETE FROM outbox WHERE entity_type = 'task' AND entity_id IN
			(SELECT id FROM tasks WHERE parent_id = ?)`, id); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM tasks WHERE parent_id = ?`, id); err != nil {
			return err
		}
		purge, err := enqueue(tx, p, model.EntityTask, id, model.OpDelete)
		if err != nil {
			return err
		}
		if p == model.ProviderLocal || purge || remoteID == "" {
			if _, err := tx.Exec(`DELETE FROM outbox WHERE entity_type = 'task' AND entity_id = ?`, id); err != nil {
				return err
			}
			_, err = tx.Exec(`DELETE FROM tasks WHERE id = ?`, id)
			return err
		}
		_, err = tx.Exec(`UPDATE tasks SET deleted = 1, dirty = 1, updated_at = ? WHERE id = ?`, now(), id)
		return err
	})
}

// --- syncer-side writes --------------------------------------------------

// PutRemoteTask writes server state verbatim (not dirty, nothing queued).
func (s *Store) PutRemoteTask(t *model.Task) error {
	if t.ID == "" {
		t.ID = NewID()
	}
	t.Dirty, t.Deleted = false, false
	if t.UpdatedAt.IsZero() {
		t.UpdatedAt = time.Now()
	}
	return s.tx(func(tx *sql.Tx) error { return writeTask(tx, t) })
}

func (s *Store) MarkTaskPushed(id, remoteID string) error {
	_, err := s.db.Exec(`UPDATE tasks SET remote_id = ?,
		dirty = (SELECT COUNT(*) > 0 FROM outbox WHERE entity_type = 'task' AND entity_id = ?) WHERE id = ?`,
		remoteID, id, id)
	return err
}

func (s *Store) PurgeTask(id string) error {
	_, err := s.db.Exec(`DELETE FROM tasks WHERE id = ? OR parent_id = ?`, id, id)
	return err
}
