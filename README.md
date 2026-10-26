# CalDo

A terminal calendar and task manager with two-way sync to **Google Calendar** and **Todoist**.

- Split screen: calendar on the left, tasks on the right. `Space` switches panes and `\` toggles between split and full screen.
- You move a cursor over days and individual events or tasks, as in calcurse.
- Month grid with a day list, plus week and agenda views. Recurring events are supported, with "only this / this and following / all" edits.
- The task list looks like taskbook: groups with `[done/total]`, `✔ ☐` icons, priorities and a completion summary.
- Press `e` to edit any event or task in `$EDITOR` (vim/nvim) as a YAML frontmatter + Markdown document.
- Desktop notifications for event reminders via `notify-send`, with separate defaults for timed and all-day events, overridable per event.
- Local-first: everything works offline. Changes are queued and pushed when you're back online. If an item changes in both places, the server version wins, and your version is kept under `C` (conflicts) so you can restore it.

## Build

```sh
make build        # → bin/caldo
make demo         # try it with sample data in a throwaway database
```

## Connect services

**Todoist:** run `caldo auth todoist` and paste your API token from *Settings → Integrations → Developer*.

**Google Calendar:**
1. In [Google Cloud Console](https://console.cloud.google.com/), create a project and enable the **Google Calendar API**.
2. Configure the OAuth consent screen (External, add yourself as a test user).
3. Create credentials: **OAuth client ID → Desktop app**, then download the JSON.
4. Set `client_secret_file` under `[google]` in `~/.config/caldo/config.toml`. See `config.example.toml`.
5. Run `caldo auth google`. A browser window opens for consent.

Tokens are stored in the system keyring. If no keyring is available, they go in `~/.local/share/caldo/secrets.json` with mode 0600.

`caldo sync` runs one sync cycle from the shell. The TUI syncs on start, every `sync.interval`, and when you press `r`.

## Reminders and background sync

Default reminders live under `[reminders]` in `config.toml` (see `config.example.toml`): `timed = ["10m"]` fires 10 minutes before an event, and `all_day = ["09:00"]` fires at 09:00 on the day (`"-1d 18:00"` means the day before at 18:00). To override them for one event, press `e` and set `reminders:` in the document to `default`, `none`, or a list such as `[10m, 1h]`. Reminders are stored only in CalDo; they are not synced to Google Calendar.

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
| `1` `2` `3` | month / week / agenda |
| `[` `]`, `t` | previous / next period, today |
| `a` / `A` | quick add / add in `$EDITOR` |
| `e`, `enter`, `d` | edit, details, delete |
| `x` | complete task |
| `p`, `m` | cycle priority, reschedule / move |
| `s`, `v` | group tasks by project/date/label, show completed |
| `/`, `f` | text filter, choose calendars & projects |
| `r`, `C` | sync now, conflicts |

Quick add syntax for tasks: `Buy milk #Home @errands p1 tomorrow`. For events: `14:00-15:30 Design review`. A title with no time creates an all-day event.
