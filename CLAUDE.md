# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

CalDo is a Go TUI (Bubble Tea v2) calendar + task manager with two-way sync to Google Calendar and Todoist. See README.md for user-facing features and setup.

## Toolchain

Go is not installed on the host (Fedora Atomic). Every Go command runs inside the `caldo-dev` distrobox:

```sh
distrobox enter caldo-dev -- sh -c 'cd /var/home/kfazilov/Dev/CalDo && <command>'
```

Commands (inside the box):

- `make build` — builds `bin/caldo`
- `make test` / `go test ./...`
- `go test ./internal/recur -run TestSplitHelpers -v` — run a single test
- `CALDO_SHOW=1 go test ./internal/ui -run TestSplitLayout -v` — print rendered screens to check layout
- `make lint` — golangci-lint v2, config in `.golangci.yml` (errcheck is strict outside tests)
- `make demo` — runs the app against `/tmp/caldo-demo.db` filled with sample data
- `bin/caldo --db <file> --config <file>` — point at a throwaway DB/config; never test against the real `~/.local/share/caldo/caldo.db`

## Architecture

**Local-first data flow.** The UI only reads and writes SQLite through `internal/store`. Mutating methods (`SaveEvent`, `SaveTask`, `SetTaskChecked`, `DeleteEvent`, `DeleteTask`) update the row and, for remote-backed entities, append to the `outbox` table in the same transaction. Syncer-side writes use separate methods (`PutRemote*`, `Mark*Pushed`, `Purge*`), which never enqueue anything. Keep these two paths separate.

**Outbox is state-based.** Entries record only `(entity, op)`. Providers read the entity's *current* row at push time. `store/outbox.go:enqueue` coalesces entries: create absorbs later updates, delete-after-create purges the row entirely, and complete/uncomplete cancel each other out. Deletes of synced entities are soft (`deleted=1`) until the push succeeds.

**Provider identity.** An entity's provider comes from its calendar or project (`calendars.provider`, `projects.provider`). The built-in `local-calendar` / `local-inbox` rows (`store.LocalCalendarID`, `store.LocalInboxID`) are provider `local` and never enqueue anything.

**Conflict policy: the server wins.** When a pull brings a newer server version of an entity that has pending local changes, the provider stores the local version as JSON in `conflicts`, drops its outbox entries, and applies the server version. The UI's `C` dialog lets the user restore that JSON via `$EDITOR`.
- Google (`internal/provider/google`): push first with `If-Match: etag`, where 412 means conflict; then pull per calendar with `syncToken`, where 410 means clear the token and run a full resync.
- Todoist (`internal/provider/todoist`): has no If-Match, so a cycle is pull (detect conflicts via `updated_at` vs `tasks.remote_version`) → push commands → pull. The command `uuid` is the outbox entry id, which makes retries idempotent. `temp_id` is the local task id, so commands in the same batch can reference unpushed parents.

**Recurring events.** Masters (with `recurrence` lines) and exceptions (with `recurring_event_id` + `original_start`) are stored as-is, never pre-expanded. `recur.Occurrences` expands them for display. rrule-go cannot parse `VALUE=DATE`, so `recur.normalizeLine` rewrites date-only values as floating datetimes. Scope edits ("this / following / all") live in `ui/actions.go:applyEventChange` and `applyEventDelete`. "This" creates or updates an exception, "following" sets `UNTIL` on the old master and creates a new master (`recur.UntilBefore`, `recur.TailRecurrence`), and "all" shifts the master. Cancelled exceptions hide single occurrences, matching how Google represents them.

**UI structure.** `internal/ui/app.go` is the root model. It owns focus (calendar or tasks pane), split vs full layout, the status bar, and one modal `dialog` at a time (`ui/dialog.go`). The panes (`ui/calendar`, `ui/tasks`) handle only navigation and rendering. They return content lines, and the root wraps them in `theme.Box`. All actions (add, edit, delete, editor round trips) live in `ui/actions.go`. A dialog callback can open the next dialog by assigning `m.dlg`, because the root only clears a dialog that is still the current one. `$EDITOR` runs via `tea.ExecProcess`, and parse errors offer to reopen the editor with the user's text intact.

**Mouse** (`ui/mouse.go`). `View` records the frame geometry: `m.layout` holds the pane x/width and `m.dlgRect` the dialog box. Panes record hit regions while rendering. `calendar.Model.hits`, rebuilt each `Render`, maps content cells to days or list indexes, and `tasks.Model.lineRows` maps lines to rows. Clicks go through the same `m.action(act)` dispatcher as keys. Context menus are `choiceDialog{menu: true}`, which are anchored (`anchoredDialog`) and clickable (`clickDialog`). New list rendering must register hits, or clicks on it will do nothing.

**Task views.** `tasks.Model` has a `view` (Today, Upcoming, All). `viewTasks()` filters the data, and the `buildBy*` builders group whatever slice they get. `groupBy` is an array indexed by view, so each view keeps its own grouping. The view keys (`1`/`2`/`3`, actions `view_month`/`view_week`/`view_agenda`) act on the focused pane.

**Key bindings** are action names (`config.Act*`) mapped to key strings in `config.DefaultKeys()` and overridable from `[keys]` in config.toml. To add a key, add the action constant and default binding, handle it in `ui/app.go`, and list it in `helpSections` (`ui/keys.go`).

**Tasks in the calendar.** With `calendar.show_tasks` on, `ui.Model.Occurrences` appends open dated tasks as `model.Occurrence{Task: t}`, with a synthetic `Event` (id `task:<id>`, `CalendarID` = project id for the color) and `recur.Sort` merging them in. Any code that acts on a calendar selection must check `o.Task` first. `calendarKey` routes those selections to task actions (`calendarTaskKey`), never to event or store event methods.

**Reminders and notifications.** `Event.Reminders` holds offsets from the start (negative = before). nil means the `[reminders]` defaults, or for an exception the series' reminders (`remind.Defaults.For`). They are local-only, stored in the `event_reminders` table, which `PutRemoteEvent` never touches. `SaveEvent` skips the outbox when `Event.SameContent` says only local fields changed, and `applyEventChange` applies a reminders-only edit of a recurring occurrence to the series instead of creating an exception. `caldo notify` (`internal/notify`) runs from a systemd timer every minute. It fires reminders whose time has passed (by less than 24h) for events that are not over, and dedupes them through the `notified` table. Its first run is silent.

**Sync scheduling.** `syncer.Syncer` runs providers sequentially inside a `tea.Cmd`. The UI reschedules with `tea.Tick`: every `sync.interval`, or at most 1 minute while offline (detected by `syncer.IsOffline`). The store uses a single DB connection (`SetMaxOpenConns(1)`), which serializes UI and sync writers. A background `caldo sync` (systemd timer) may run alongside the TUI. `Syncer.LockFile` (`sync.lock` next to the DB, flock) keeps the two from syncing at once, and the TUI polls `PRAGMA data_version` every 15s to reload after another process writes.

**Cell widths.** All width math goes through `theme.StringWidth`/`theme.Truncate`/`theme.Fit`, never `ansi.StringWidth` or lipgloss widths directly. `theme.WidthMethod` mirrors the Bubble Tea renderer: wcwidth by default (tmux), and grapheme clusters after the terminal reports mode 2027. `theme.Fit` also strips U+FE0F (the emoji presentation selector). tmux 3.4+ counts it as widening the preceding character, while foot and wcwidth do not, so leaving it in shifts the rest of the line in some terminal stacks.

## Testing notes

- Providers are tested against in-memory fake servers (`httptest`). The Google client points at one through `option.WithEndpoint`. When the sync protocol changes, extend the fakes rather than mocking the store.
- UI tests drive `Model.Update` with `tea.KeyPressMsg` and assert on `ansi.Strip(View().Content)`. Every screen line must be exactly the terminal width.
