# CalDo

A terminal calendar and task manager with two-way sync to **Google Calendar** and **Todoist**.

- Split screen: calendar on the left, tasks on the right. `Space` switches panes and `\` toggles between split and full screen.
- You move a cursor over days and individual events or tasks, as in calcurse.
- On today, the cursor starts on the event that is happening now or comes next.
- Events that have already ended are greyed out (`calendar.dim_past`), so upcoming ones stand out.
- Tasks with a due date also show up in the calendar, colored by project. `e`, `x`, `d`, `m` and `p` act on the task when one is selected there.
- Month grid with a day list, plus week and agenda views. Recurring events are supported, with "only this / this and following / all" edits.
- Three task views: **Today** (overdue and due today), **Upcoming** (dated tasks, one group per day) and **All**. Each view remembers its own grouping.
- The task list looks like taskbook: groups with `[done/total]`, `✔ ☐` icons, priorities and a completion summary.
- Press `e` to edit any event or task in `$EDITOR` (vim/nvim) as a YAML frontmatter + Markdown document.
- Desktop notifications via `notify-send`. Event reminders have separate defaults for timed and all-day events and can be overridden per event. Tasks notify at their due time.
- Mouse support: click to focus a pane and select days, events or tasks; double-click opens details; right-click opens a context menu; middle-click completes a task, edits an event or adds an event on a day; the wheel scrolls lists or flips months over the grid.
- Local-first: everything works offline. Changes are queued and pushed when you're back online. If an item changes in both places, the server version wins, and your version is kept under `C` (conflicts) so you can restore it.

## Build

```sh
make build        # → bin/caldo
make demo         # try it with sample data in a throwaway database
```

## Connect services

The first time you start `caldo`, it asks for your Todoist API token and then opens Google sign-in in your browser. Either step can be skipped and done later with the commands below.

**Todoist:** run `caldo auth todoist` and paste your API token from *Settings → Integrations → Developer*.

**Google Calendar:** run `caldo auth google`. A browser window opens for consent. Release binaries include CalDo's OAuth client, so nothing else is needed.

To use your own OAuth client instead (or with a source build that has none):
1. In [Google Cloud Console](https://console.cloud.google.com/), create a project and enable the **Google Calendar API**.
2. Configure the OAuth consent screen (External) and set its publishing status to **In production**. In *Testing*, refresh tokens expire after 7 days.
3. Create credentials: **OAuth client ID → Desktop app**, then download the JSON.
4. Either set `client_secret_file` under `[google]` in `~/.config/caldo/config.toml` (see `config.example.toml`), or build the client in: `GOOGLE_CLIENT_ID=... GOOGLE_CLIENT_SECRET=... make build`.

By default every calendar in your Google account is synced. Use `calendars = [...]` to sync only the listed ones, or `exclude_calendars = [...]` to skip a few, for example the "Todoist" calendar created by Todoist's own Google integration, since CalDo already shows those tasks.

Tokens are stored in the system keyring. If no keyring is available, they go in `~/.local/share/caldo/secrets.json` with mode 0600.

`caldo sync` runs one sync cycle from the shell. The TUI syncs on start, every `sync.interval`, and when you press `r`.

## Reminders and background sync

Default reminders live under `[reminders]` in `config.toml` (see `config.example.toml`): `timed = ["10m"]` fires 10 minutes before an event, and `all_day = ["09:00"]` fires at 09:00 on the day (`"-1d 18:00"` means the day before at 18:00). To override them for one event, press `e` and set `reminders:` in the document to `default`, `none`, or a list such as `[10m, 1h]`. Reminders are stored only in CalDo; they are not synced to Google Calendar.

Tasks notify at their due time (`tasks = ["0m"]`). Tasks due on a date without a time don't notify unless you set `tasks_all_day`, for example `["09:00"]`.

`caldo notify` shows the reminders that are due and exits. Run it every minute from a systemd timer. `caldo sync` can also run in the background, and a lock file keeps it from overlapping with the TUI's own sync. The units in `contrib/systemd` expect the binary at `~/.local/bin/caldo`:

```sh
ln -s ~/Dev/CalDo/bin/caldo ~/.local/bin/caldo
cp contrib/systemd/caldo-* ~/.config/systemd/user/
systemctl --user daemon-reload
systemctl --user enable --now caldo-notify.timer caldo-sync.timer
```

The first `caldo notify` run is silent. It only records the reminders that are already due, so enabling the timer does not replay everything from earlier today.

## Keys

Press `?` in the app for the full list. The basics:

| Key | Action |
|---|---|
| `space` | switch pane |
| `\` | split / full screen |
| `h j k l` / arrows | move |
| `tab` | month grid ⇄ day events |
| `1` `2` `3` | calendar: month / week / agenda · tasks: today / upcoming / all |
| `[` `]`, `t` | previous / next period, today |
| `a` / `A` | quick add / add in `$EDITOR` |
| `e`, `enter`, `d` | edit, details, delete |
| `x` | complete task |
| `p`, `m` | cycle priority, reschedule / move |
| `s`, `v` | change the task view's grouping (project/date/day/label), show completed |
| `/`, `f` | text filter, choose calendars & projects |
| `r`, `C` | sync now, conflicts |

Quick add syntax for tasks: `Buy milk #Home @errands p1 tomorrow`. For events: `14:00-15:30 Design review`. A title with no time creates an all-day event.
