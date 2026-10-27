package ui

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"caldo/internal/editor"
	"caldo/internal/model"
	"caldo/internal/quick"
	"caldo/internal/recur"
	"caldo/internal/remind"
	"caldo/internal/ui/tasks"
	"caldo/internal/ui/theme"
)

// --- $EDITOR round trip --------------------------------------------------

type editorDoneMsg struct {
	path  string
	orig  []byte
	err   error
	apply func([]byte) error
}

func (m *Model) openEditor(doc, orig []byte, apply func([]byte) error) tea.Cmd {
	path, err := editor.WriteTemp(doc)
	if err != nil {
		m.setError(err)
		return nil
	}
	c := editor.Command(m.cfg.General.Editor, path)
	return tea.ExecProcess(c, func(err error) tea.Msg {
		return editorDoneMsg{path: path, orig: orig, err: err, apply: apply}
	})
}

func (m *Model) finishEditor(msg editorDoneMsg) tea.Cmd {
	defer os.Remove(msg.path)
	if msg.err != nil {
		m.setError(fmt.Errorf("editor: %w", msg.err))
		return nil
	}
	b, err := os.ReadFile(msg.path)
	if err != nil {
		m.setError(err)
		return nil
	}
	if bytes.Equal(b, msg.orig) {
		m.setMessage("No changes")
		return nil
	}
	err = msg.apply(b)
	switch {
	case errors.Is(err, editor.ErrEmpty):
		m.setMessage("Cancelled (empty document)")
		return nil
	case err != nil:
		// Keep the user's text: offer to reopen it rather than losing the edit.
		m.dlg = &confirmDialog{
			msg: "Error: " + err.Error() + " — reopen editor?",
			onYes: func() tea.Cmd {
				return m.openEditor(b, msg.orig, msg.apply)
			},
		}
		return nil
	}
	m.setMessage("Saved")
	return changed
}

func (m *Model) lookup() editor.Lookup {
	var l editor.Lookup
	l.Calendars, _ = m.st.Calendars()
	l.Projects, _ = m.st.Projects()
	l.Sections, _ = m.st.Sections()
	l.Tasks, _ = m.st.Tasks(true)
	l.Reminders = m.cfg.DefaultReminders()
	return l
}

// --- events --------------------------------------------------------------

type scope int

const (
	scopeThis scope = iota
	scopeFollowing
	scopeAll
)

var scopeOptions = []string{"1. Only this occurrence", "2. This and following", "3. All occurrences"}

func (m *Model) withScope(o model.Occurrence, title string, fn func(scope) tea.Cmd) tea.Cmd {
	if !o.IsRecurring() {
		return fn(scopeAll)
	}
	m.dlg = &choiceDialog{title: title, options: scopeOptions, onPick: func(i int) tea.Cmd { return fn(scope(i)) }}
	return nil
}

func (m *Model) writable(calendarID string) bool {
	c, err := m.st.Calendar(calendarID)
	if err != nil {
		m.setError(err)
		return false
	}
	if c.ReadOnly {
		m.setError(fmt.Errorf("calendar %q is read-only", c.Name))
		return false
	}
	return true
}

func (m *Model) defaultCalendar() string {
	cals, _ := m.st.Calendars()
	return quick.DefaultCalendar(cals)
}

// occurrenceView is the event as the user sees this occurrence.
func occurrenceView(o model.Occurrence, sc scope) model.Event {
	e := *o.Event
	e.Start, e.End = o.Start, o.End
	if o.IsRecurring() {
		if sc == scopeThis {
			e.Recurrence = nil
		} else {
			e.Recurrence = o.Master.Recurrence
		}
	}
	return e
}

func (m *Model) editEvent(o model.Occurrence) tea.Cmd {
	if !m.writable(o.Event.CalendarID) {
		return nil
	}
	return m.withScope(o, "Edit recurring event", func(sc scope) tea.Cmd {
		base := occurrenceView(o, sc)
		doc := editor.EncodeEvent(&base, m.lookup())
		return m.openEditor(doc, doc, func(b []byte) error {
			edited, err := editor.DecodeEvent(b, &base, m.lookup())
			if err != nil {
				return err
			}
			return m.applyEventChange(o, edited, sc)
		})
	})
}

// applyEventChange saves an edited occurrence according to the chosen scope.
func (m *Model) applyEventChange(o model.Occurrence, edited *model.Event, sc scope) error {
	if !o.IsRecurring() {
		return m.st.SaveEvent(edited)
	}
	master := *o.Master
	if base := occurrenceView(o, sc); base.SameContent(edited) {
		// Reminders are local and per series; changing only them must not
		// split the series or create a server-side exception.
		target := master.ID
		if sc == scopeThis && o.Event.IsException() {
			target = o.Event.ID
		}
		return m.st.SetEventReminders(target, edited.Reminders)
	}
	switch sc {
	case scopeThis:
		ex := *edited
		ex.Recurrence = nil
		ex.CalendarID = master.CalendarID
		if !o.Event.IsException() {
			inst := o.InstanceStart
			ex.ID, ex.RemoteID, ex.ETag = "", "", ""
			ex.RecurringEventID, ex.OriginalStart = master.ID, &inst
		}
		return m.st.SaveEvent(&ex)

	case scopeFollowing:
		if !o.InstanceStart.After(master.Start) {
			break // first occurrence: same as editing all
		}
		old := master
		old.Recurrence = recur.UntilBefore(master.Recurrence, o.InstanceStart, master.AllDay)
		if err := m.st.SaveEvent(&old); err != nil {
			return err
		}
		tail := *edited
		tail.ID, tail.RemoteID, tail.ETag = "", "", ""
		tail.RecurringEventID, tail.OriginalStart, tail.Status = "", nil, ""
		if slices.Equal(edited.Recurrence, master.Recurrence) {
			tail.Recurrence = recur.TailRecurrence(&master, o.InstanceStart)
		}
		if err := m.st.SaveEvent(&tail); err != nil {
			return err
		}
		return m.dropExceptionsFrom(master.ID, o.InstanceStart)
	}

	// All occurrences: shift the master by how much this occurrence moved.
	all := master
	all.Title, all.Description, all.Location = edited.Title, edited.Description, edited.Location
	all.CalendarID, all.Timezone, all.Recurrence = edited.CalendarID, edited.Timezone, edited.Recurrence
	all.Reminders = edited.Reminders
	delta := edited.Start.Sub(o.Start)
	dur := edited.End.Sub(edited.Start)
	all.AllDay = edited.AllDay
	all.Start = master.Start.Add(delta)
	if all.AllDay {
		all.Start = time.Date(all.Start.Year(), all.Start.Month(), all.Start.Day(), 0, 0, 0, 0, time.Local)
	}
	all.End = all.Start.Add(dur)
	return m.st.SaveEvent(&all)
}

// dropExceptionsFrom removes exceptions at or after a split point; the server
// drops them when the master's rule is shortened.
func (m *Model) dropExceptionsFrom(masterID string, from time.Time) error {
	exs, err := m.st.Exceptions(masterID)
	if err != nil {
		return err
	}
	for _, ex := range exs {
		if ex.OriginalStart != nil && !ex.OriginalStart.Before(from) {
			if err := m.st.DropPending(model.EntityEvent, ex.ID); err != nil {
				return err
			}
			if err := m.st.PurgeEvent(ex.ID); err != nil {
				return err
			}
		}
	}
	return nil
}

func (m *Model) deleteEvent(o model.Occurrence) tea.Cmd {
	if !m.writable(o.Event.CalendarID) {
		return nil
	}
	run := func(sc scope) tea.Cmd {
		if err := m.applyEventDelete(o, sc); err != nil {
			m.setError(err)
			return nil
		}
		m.setMessage("Deleted")
		return changed
	}
	if o.IsRecurring() {
		return m.withScope(o, "Delete recurring event", run)
	}
	m.dlg = &confirmDialog{msg: fmt.Sprintf("Delete %q?", o.Event.Title), onYes: func() tea.Cmd { return run(scopeAll) }}
	return nil
}

func (m *Model) applyEventDelete(o model.Occurrence, sc scope) error {
	if !o.IsRecurring() {
		return m.st.DeleteEvent(o.Event.ID)
	}
	master := *o.Master
	switch sc {
	case scopeThis:
		// A cancelled exception hides the slot, which is how Google represents it too.
		ex := occurrenceView(o, scopeThis)
		ex.Status = model.StatusCancelled
		ex.CalendarID = master.CalendarID
		if !o.Event.IsException() {
			inst := o.InstanceStart
			ex.ID, ex.RemoteID, ex.ETag = "", "", ""
			ex.RecurringEventID, ex.OriginalStart = master.ID, &inst
		}
		return m.st.SaveEvent(&ex)
	case scopeFollowing:
		if o.InstanceStart.After(master.Start) {
			master.Recurrence = recur.UntilBefore(master.Recurrence, o.InstanceStart, master.AllDay)
			if err := m.st.SaveEvent(&master); err != nil {
				return err
			}
			return m.dropExceptionsFrom(master.ID, o.InstanceStart)
		}
	}
	return m.st.DeleteEvent(master.ID)
}

// quickAddEvent: "Title" makes an all-day event, "14:00 Title" or "14:00-15:30 Title" a timed one.
func (m *Model) quickAddEvent() tea.Cmd {
	day := m.cal.SelectedDay()
	m.dlg = newInput("New event · "+day.Format("Mon 2 Jan"), "[HH:MM[-HH:MM]] title [#Calendar] · empty title cancels · A opens $EDITOR", "", func(s string) tea.Cmd {
		if s == "" {
			return nil
		}
		cals, _ := m.st.Calendars()
		e, err := quick.Event(s, day, false, m.cfg.DefaultEventDuration(), cals, m.defaultCalendar())
		if err != nil {
			return status(err)
		}
		if err := m.st.SaveEvent(e); err != nil {
			return status(err)
		}
		m.cal.SelectEvent(e.ID, e.Start)
		m.setMessage("Event added")
		return changed
	})
	return nil
}

func (m *Model) newEventInEditor() tea.Cmd {
	day := m.cal.SelectedDay()
	n := time.Now()
	start := time.Date(day.Year(), day.Month(), day.Day(), n.Hour()+1, 0, 0, 0, time.Local)
	base := &model.Event{CalendarID: m.defaultCalendar(), Start: start, End: start.Add(m.cfg.DefaultEventDuration())}
	doc := editor.EncodeEvent(base, m.lookup())
	return m.openEditor(doc, doc, func(b []byte) error {
		e, err := editor.DecodeEvent(b, base, m.lookup())
		if err != nil {
			return err
		}
		if !m.writable(e.CalendarID) {
			return errors.New("calendar is read-only")
		}
		if err := m.st.SaveEvent(e); err != nil {
			return err
		}
		m.cal.SelectEvent(e.ID, e.Start)
		return nil
	})
}

// moveEvent asks for a new start and keeps the duration.
func (m *Model) moveEvent(o model.Occurrence) tea.Cmd {
	if !m.writable(o.Event.CalendarID) {
		return nil
	}
	layout := "2006-01-02 15:04"
	if o.Event.AllDay {
		layout = "2006-01-02"
	}
	return m.withScope(o, "Move recurring event", func(sc scope) tea.Cmd {
		m.dlg = newInput("Move to", "YYYY-MM-DD"+map[bool]string{false: " HH:MM", true: ""}[o.Event.AllDay], o.Start.Format(layout), func(s string) tea.Cmd {
			st, err := time.ParseInLocation(layout, s, time.Local)
			if err != nil {
				return status(fmt.Errorf("expected %s", layout))
			}
			e := occurrenceView(o, sc)
			dur := e.End.Sub(e.Start)
			e.Start, e.End = st, st.Add(dur)
			if err := m.applyEventChange(o, &e, sc); err != nil {
				return status(err)
			}
			m.cal.SelectEvent(e.ID, st)
			m.setMessage("Moved")
			return changed
		})
		return nil
	})
}

func (m *Model) eventDetail(o model.Occurrence) dialog {
	th := m.th
	e := o.Event
	cal, _ := m.st.Calendar(e.CalendarID)
	when := o.Start.Format("Mon 2 Jan 2006 15:04") + " – " + o.End.Format("15:04")
	if e.AllDay {
		last := o.End.AddDate(0, 0, -1)
		when = o.Start.Format("Mon 2 Jan 2006")
		if !last.Equal(o.Start) {
			when += " – " + last.Format("Mon 2 Jan 2006")
		}
	} else if !sameDate(o.Start, o.End) {
		when = o.Start.Format("Mon 2 Jan 15:04") + " – " + o.End.Format("Mon 2 Jan 15:04")
	}
	field := func(k, v string) string { return th.Faint.Render(fmt.Sprintf("%-10s", k)) + v }
	lines := []string{
		th.Bold.Render(e.Title), "",
		field("When", when),
		field("Calendar", th.Fg(cal.Color).Render("● ")+cal.Name),
	}
	if e.Location != "" {
		lines = append(lines, field("Where", theme.OneLine(e.Location)))
	}
	if o.IsRecurring() {
		lines = append(lines, field("Repeats", strings.Join(o.Master.Recurrence, " ")))
	}
	lines = append(lines, field("Remind", m.reminderText(o)))
	if e.Dirty {
		lines = append(lines, field("Sync", "pending push"))
	}
	if e.Description != "" {
		lines = append(lines, "")
		lines = append(lines, wrap(e.Description, 66)...)
	}
	return &textDialog{title: "Event", lines: lines, width: 72}
}

func (m *Model) reminderText(o model.Occurrence) string {
	rs := m.cfg.DefaultReminders().For(o)
	if len(rs) == 0 {
		return "none"
	}
	parts := remind.FormatList(rs, o.Event.AllDay)
	if !o.Event.AllDay {
		for i := range parts {
			parts[i] += " before"
		}
	}
	text := strings.Join(parts, ", ")
	if o.Event.Reminders == nil && (o.Master == nil || o.Master.Reminders == nil) {
		text += m.th.Faint.Render(" (default)")
	}
	return text
}

// --- tasks ---------------------------------------------------------------

func (m *Model) defaultProject() (string, string) {
	if p, s := m.tasks.CurrentProject(); p != "" {
		return p, s
	}
	ps, _ := m.st.Projects()
	return quick.DefaultProject(ps), ""
}

// parseQuickTask understands "Title #Project @label p1 tomorrow".
func (m *Model) parseQuickTask(s string) (*model.Task, error) {
	projectID, sectionID := m.defaultProject()
	projects, _ := m.st.Projects()
	return quick.Task(s, projects, projectID, sectionID)
}

func (m *Model) quickAddTask() tea.Cmd {
	m.dlg = newInput("New task", "title #Project @label p1..p4 tomorrow/friday/2026-11-01/every monday · A opens $EDITOR", "", func(s string) tea.Cmd {
		if s == "" {
			return nil
		}
		t, err := m.parseQuickTask(s)
		if err != nil {
			return status(err)
		}
		t.Sort = int(time.Now().Unix())
		if t.Due == nil && m.tasks.CurrentView() == tasks.Today {
			// Like Todoist: tasks added in Today are due on the day it shows, or they'd vanish.
			t.Due = editor.ParseDue(m.tasks.Day().Format(time.DateOnly), nil)
		}
		if err := m.st.SaveTask(t); err != nil {
			return status(err)
		}
		m.reload()
		m.tasks.Select(t.ID)
		m.setMessage("Task added")
		return nil
	})
	return nil
}

func (m *Model) newTaskInEditor() tea.Cmd {
	p, s := m.defaultProject()
	base := &model.Task{ProjectID: p, SectionID: s, Priority: 1, Sort: int(time.Now().Unix())}
	doc := editor.EncodeTask(base, m.lookup())
	return m.openEditor(doc, doc, func(b []byte) error {
		t, err := editor.DecodeTask(b, base, m.lookup())
		if err != nil {
			return err
		}
		return m.st.SaveTask(t)
	})
}

func (m *Model) editTask(t *model.Task) tea.Cmd {
	base := *t
	doc := editor.EncodeTask(&base, m.lookup())
	return m.openEditor(doc, doc, func(b []byte) error {
		edited, err := editor.DecodeTask(b, &base, m.lookup())
		if err != nil {
			return err
		}
		return m.st.SaveTask(edited)
	})
}

func (m *Model) toggleTask(t *model.Task) tea.Cmd {
	if err := m.st.SetTaskChecked(t.ID, !t.Checked); err != nil {
		m.setError(err)
		return nil
	}
	if t.Checked {
		m.setMessage("Reopened “" + t.Content + "”")
	} else {
		m.setMessage("✔ Completed “" + t.Content + "”")
	}
	return changed
}

func (m *Model) deleteTask(t *model.Task) tea.Cmd {
	m.dlg = &confirmDialog{msg: fmt.Sprintf("Delete task %q?", t.Content), onYes: func() tea.Cmd {
		if err := m.st.DeleteTask(t.ID); err != nil {
			return status(err)
		}
		m.setMessage("Task deleted")
		return changed
	}}
	return nil
}

func (m *Model) cyclePriority(t *model.Task) tea.Cmd {
	d := t.Priority.Display() - 1
	if d < 1 {
		d = 4
	}
	nt := *t
	nt.Priority = model.PriorityFromDisplay(d)
	if err := m.st.SaveTask(&nt); err != nil {
		m.setError(err)
		return nil
	}
	m.setMessage(fmt.Sprintf("Priority p%d", d))
	return changed
}

func (m *Model) rescheduleTask(t *model.Task) tea.Cmd {
	cur := ""
	if t.Due != nil {
		cur = editor.DueText(t.Due)
	}
	m.dlg = newInput("Due date", "YYYY-MM-DD [HH:MM], today, tomorrow, friday, every monday… · empty removes", cur, func(s string) tea.Cmd {
		nt := *t
		nt.Due = editor.ParseDue(s, t.Due)
		if err := m.st.SaveTask(&nt); err != nil {
			return status(err)
		}
		m.setMessage("Rescheduled")
		return changed
	})
	return nil
}

func (m *Model) taskDetail(t *model.Task) dialog {
	th := m.th
	p, _ := m.st.Project(t.ProjectID)
	field := func(k, v string) string { return th.Faint.Render(fmt.Sprintf("%-10s", k)) + v }
	state := th.PendingIcon.Render("☐ open")
	if t.Checked {
		state = th.DoneIcon.Render("✔ done")
	}
	lines := []string{
		th.Bold.Render(t.Content), "",
		field("Status", state),
		field("Project", th.Fg(p.Color).Render("● ")+p.Name),
		field("Priority", fmt.Sprintf("p%d", t.Priority.Display())),
	}
	if t.Due != nil {
		lines = append(lines, field("Due", editor.DueText(t.Due)))
	}
	if t.Deadline != "" {
		lines = append(lines, field("Deadline", t.Deadline))
	}
	if len(t.Labels) > 0 {
		lines = append(lines, field("Labels", "@"+strings.Join(t.Labels, " @")))
	}
	if t.Dirty {
		lines = append(lines, field("Sync", "pending push"))
	}
	if t.Description != "" {
		lines = append(lines, "")
		lines = append(lines, wrap(t.Description, 66)...)
	}
	return &textDialog{title: "Task", lines: lines, width: 72}
}

// --- overlays ------------------------------------------------------------

func (m *Model) helpDialog() dialog {
	th := m.th
	var lines []string
	for i, sec := range helpSections {
		if i > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, th.Header.Render(sec.title))
		for _, a := range sec.actions {
			lines = append(lines, "  "+th.StatusKey.Render(theme.Fit(m.keys.keys(a[0]), 12))+" "+a[1])
		}
	}
	if m.cfg.General.Mouse {
		lines = append(lines, "", th.Header.Render("Mouse"))
		for _, l := range [][2]string{
			{"click", "focus pane, select day / item"},
			{"double click", "open details"},
			{"right click", "context menu"},
			{"middle click", "task: complete · event: edit · day: add"},
			{"wheel", "scroll lists · over the grid: change month"},
		} {
			lines = append(lines, "  "+th.StatusKey.Render(theme.Fit(l[0], 12))+" "+l[1])
		}
	}
	return &textDialog{title: "Keys", lines: lines, width: 60}
}

func (m *Model) filtersDialog() dialog {
	d := &checklistDialog{title: "Show calendars & projects", onDone: func() tea.Cmd { return changed }}
	cals, _ := m.st.Calendars()
	d.items = append(d.items, checkItem{label: "Calendars", header: true})
	for _, c := range cals {
		d.items = append(d.items, checkItem{label: c.Name, color: c.Color, checked: c.Enabled,
			toggle: func(on bool) error { return m.st.SetCalendarEnabled(c.ID, on) }})
	}
	ps, _ := m.st.Projects()
	d.items = append(d.items, checkItem{label: "Projects", header: true})
	for _, p := range ps {
		d.items = append(d.items, checkItem{label: p.Name, color: p.Color, checked: p.Enabled,
			toggle: func(on bool) error { return m.st.SetProjectEnabled(p.ID, on) }})
	}
	d.sel = 1
	return d
}

func (m *Model) conflictsDialog() dialog {
	cs, err := m.st.Conflicts()
	if err != nil {
		m.setError(err)
		return nil
	}
	if len(cs) == 0 {
		return &textDialog{title: "Conflicts", lines: []string{"No sync conflicts.", "",
			m.th.Faint.Render("When an item changes both here and on the server, the server"),
			m.th.Faint.Render("version wins and your version is kept here to restore.")}}
	}
	var opts []string
	for _, c := range cs {
		opts = append(opts, fmt.Sprintf("%s  %s · %s", c.CreatedAt.Format("02 Jan 15:04"), c.EntityType, c.Title))
	}
	return &choiceDialog{title: "Conflicts — your discarded versions", options: opts, onPick: func(i int) tea.Cmd {
		c := cs[i]
		m.dlg = &choiceDialog{title: c.Title, options: []string{"1. Restore my version (opens $EDITOR)", "2. Discard my version"},
			onPick: func(j int) tea.Cmd {
				if j == 1 {
					if err := m.st.DeleteConflict(c.ID); err != nil {
						return status(err)
					}
					m.setMessage("Discarded")
					return nil
				}
				return m.restoreConflict(c)
			}}
		return nil
	}}
}

// restoreConflict opens the discarded local version on top of the current server version.
func (m *Model) restoreConflict(c model.Conflict) tea.Cmd {
	// The edit itself is saved; failing to clear the conflict entry only leaves it listed.
	done := func() { _ = m.st.DeleteConflict(c.ID) }
	switch c.EntityType {
	case model.EntityEvent:
		var local model.Event
		if err := json.Unmarshal([]byte(c.LocalJSON), &local); err != nil {
			return status(err)
		}
		base := local
		if cur, err := m.st.Event(c.EntityID); err == nil {
			base.RemoteID, base.ETag = cur.RemoteID, cur.ETag
		} else {
			base.ID, base.RemoteID, base.ETag = "", "", ""
		}
		doc := editor.EncodeEvent(&local, m.lookup())
		return m.openEditor(doc, nil, func(b []byte) error {
			e, err := editor.DecodeEvent(b, &base, m.lookup())
			if err != nil {
				return err
			}
			if err := m.st.SaveEvent(e); err != nil {
				return err
			}
			done()
			return nil
		})
	case model.EntityTask:
		var local model.Task
		if err := json.Unmarshal([]byte(c.LocalJSON), &local); err != nil {
			return status(err)
		}
		base := local
		if cur, err := m.st.Task(c.EntityID); err == nil {
			base.RemoteID, base.RemoteVersion = cur.RemoteID, cur.RemoteVersion
		} else {
			base.ID, base.RemoteID = "", ""
		}
		doc := editor.EncodeTask(&local, m.lookup())
		return m.openEditor(doc, nil, func(b []byte) error {
			t, err := editor.DecodeTask(b, &base, m.lookup())
			if err != nil {
				return err
			}
			if err := m.st.SaveTask(t); err != nil {
				return err
			}
			done()
			return nil
		})
	}
	return nil
}

// --- helpers -------------------------------------------------------------

func status(err error) tea.Cmd {
	return func() tea.Msg { return statusMsg{text: err.Error(), isErr: true} }
}

func sameDate(a, b time.Time) bool {
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	return ay == by && am == bm && ad == bd
}

func wrap(s string, w int) []string {
	var out []string
	for _, para := range strings.Split(s, "\n") {
		line := ""
		for _, word := range strings.Fields(para) {
			if line != "" && len([]rune(line))+1+len([]rune(word)) > w {
				out = append(out, line)
				line = word
				continue
			}
			if line != "" {
				line += " "
			}
			line += word
		}
		out = append(out, line)
	}
	return out
}
