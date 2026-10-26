CREATE TABLE IF NOT EXISTS calendars (
    id        TEXT PRIMARY KEY,
    provider  TEXT NOT NULL,
    remote_id TEXT NOT NULL DEFAULT '',
    name      TEXT NOT NULL,
    color     TEXT NOT NULL DEFAULT '',
    enabled   INTEGER NOT NULL DEFAULT 1,
    readonly  INTEGER NOT NULL DEFAULT 0,
    sort      INTEGER NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX IF NOT EXISTS calendars_remote ON calendars(provider, remote_id) WHERE remote_id <> '';

CREATE TABLE IF NOT EXISTS events (
    id                 TEXT PRIMARY KEY,
    calendar_id        TEXT NOT NULL REFERENCES calendars(id) ON DELETE CASCADE,
    remote_id          TEXT NOT NULL DEFAULT '',
    etag               TEXT NOT NULL DEFAULT '',
    title              TEXT NOT NULL DEFAULT '',
    description        TEXT NOT NULL DEFAULT '',
    location           TEXT NOT NULL DEFAULT '',
    start              TEXT NOT NULL,
    end                TEXT NOT NULL,
    all_day            INTEGER NOT NULL DEFAULT 0,
    timezone           TEXT NOT NULL DEFAULT '',
    recurrence         TEXT NOT NULL DEFAULT '',
    recurring_event_id TEXT NOT NULL DEFAULT '',
    original_start     TEXT NOT NULL DEFAULT '',
    status             TEXT NOT NULL DEFAULT 'confirmed',
    dirty              INTEGER NOT NULL DEFAULT 0,
    deleted            INTEGER NOT NULL DEFAULT 0,
    updated_at         TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS events_calendar ON events(calendar_id);
CREATE INDEX IF NOT EXISTS events_remote ON events(calendar_id, remote_id);
CREATE INDEX IF NOT EXISTS events_master ON events(recurring_event_id);

CREATE TABLE IF NOT EXISTS projects (
    id        TEXT PRIMARY KEY,
    provider  TEXT NOT NULL,
    remote_id TEXT NOT NULL DEFAULT '',
    name      TEXT NOT NULL,
    color     TEXT NOT NULL DEFAULT '',
    parent_id TEXT NOT NULL DEFAULT '',
    sort      INTEGER NOT NULL DEFAULT 0,
    is_inbox  INTEGER NOT NULL DEFAULT 0,
    enabled   INTEGER NOT NULL DEFAULT 1
);

CREATE TABLE IF NOT EXISTS sections (
    id         TEXT PRIMARY KEY,
    project_id TEXT NOT NULL,
    remote_id  TEXT NOT NULL DEFAULT '',
    name       TEXT NOT NULL,
    sort       INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS labels (
    id        TEXT PRIMARY KEY,
    remote_id TEXT NOT NULL DEFAULT '',
    name      TEXT NOT NULL,
    color     TEXT NOT NULL DEFAULT '',
    sort      INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS tasks (
    id             TEXT PRIMARY KEY,
    project_id     TEXT NOT NULL,
    section_id     TEXT NOT NULL DEFAULT '',
    parent_id      TEXT NOT NULL DEFAULT '',
    remote_id      TEXT NOT NULL DEFAULT '',
    content        TEXT NOT NULL,
    description    TEXT NOT NULL DEFAULT '',
    priority       INTEGER NOT NULL DEFAULT 1,
    labels         TEXT NOT NULL DEFAULT '[]',
    due_date       TEXT NOT NULL DEFAULT '',
    due_string     TEXT NOT NULL DEFAULT '',
    due_recurring  INTEGER NOT NULL DEFAULT 0,
    due_timezone   TEXT NOT NULL DEFAULT '',
    deadline       TEXT NOT NULL DEFAULT '',
    checked        INTEGER NOT NULL DEFAULT 0,
    sort           INTEGER NOT NULL DEFAULT 0,
    completed_at   TEXT NOT NULL DEFAULT '',
    remote_version TEXT NOT NULL DEFAULT '',
    dirty          INTEGER NOT NULL DEFAULT 0,
    deleted        INTEGER NOT NULL DEFAULT 0,
    updated_at     TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS tasks_project ON tasks(project_id);
CREATE INDEX IF NOT EXISTS tasks_remote ON tasks(remote_id);

-- Pending local changes to push. Entries reference entity state, which is read at push time.
CREATE TABLE IF NOT EXISTS outbox (
    id          TEXT PRIMARY KEY,
    seq         INTEGER NOT NULL,
    provider    TEXT NOT NULL,
    entity_type TEXT NOT NULL,
    entity_id   TEXT NOT NULL,
    op          TEXT NOT NULL,
    created_at  TEXT NOT NULL,
    attempts    INTEGER NOT NULL DEFAULT 0,
    last_error  TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS outbox_entity ON outbox(entity_type, entity_id);

CREATE TABLE IF NOT EXISTS sync_state (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS conflicts (
    id          TEXT PRIMARY KEY,
    entity_type TEXT NOT NULL,
    entity_id   TEXT NOT NULL,
    title       TEXT NOT NULL,
    local_json  TEXT NOT NULL,
    created_at  TEXT NOT NULL
);

-- CalDo-only reminders, kept apart from events so pulls never overwrite them.
-- No row: configured defaults. Empty string: no reminders. Otherwise
-- comma-separated offsets from the start in minutes (negative = before).
CREATE TABLE IF NOT EXISTS event_reminders (
    event_id  TEXT PRIMARY KEY REFERENCES events(id) ON DELETE CASCADE,
    reminders TEXT NOT NULL
);

-- Reminders already shown by `caldo notify`.
CREATE TABLE IF NOT EXISTS notified (
    key TEXT PRIMARY KEY,
    at  TEXT NOT NULL
);
