package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"caldo/internal/agenda"
	"caldo/internal/config"
	"caldo/internal/editor"
	"caldo/internal/model"
	"caldo/internal/quick"
	"caldo/internal/store"
)

// --- caldo add ---------------------------------------------------------------

func runAdd(args []string, cfg *config.Config, st *store.Store, paths config.Paths) error {
	if len(args) == 0 || (args[0] != "task" && args[0] != "event") {
		return errors.New("usage: caldo add task|event [--no-sync] <text>  (text \"-\" reads a line from stdin)")
	}
	kind := args[0]
	fs := flag.NewFlagSet("add "+kind, flag.ContinueOnError)
	noSync := fs.Bool("no-sync", false, "only save locally; the next sync pushes it")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	text, err := quickText(fs.Args(), os.Stdin)
	if err != nil {
		return err
	}

	var what string
	if kind == "task" {
		projects, err := st.Projects()
		if err != nil {
			return err
		}
		t, err := quick.Task(text, projects, quick.DefaultProject(projects), "")
		if err != nil {
			return err
		}
		t.Sort = int(time.Now().Unix())
		if err := st.SaveTask(t); err != nil {
			return err
		}
		what = fmt.Sprintf("task %q in %s", t.Content, projectName(projects, t.ProjectID))
		if t.Due != nil {
			what += ", due " + editor.DueText(t.Due)
		}
	} else {
		cals, err := st.Calendars()
		if err != nil {
			return err
		}
		e, err := quick.Event(text, time.Now(), true, cfg.DefaultEventDuration(), cals, quick.DefaultCalendar(cals))
		if err != nil {
			return err
		}
		if err := st.SaveEvent(e); err != nil {
			return err
		}
		what = fmt.Sprintf("event %q on %s in %s", e.Title, whenText(model.Occurrence{Event: e, Start: e.Start, End: e.End}),
			calendarName(cals, e.CalendarID))
	}
	fmt.Println("Added", what)
	if *noSync {
		return nil
	}
	return pushNow(cfg, st, paths)
}

// quickText joins the arguments, or reads one line from stdin for "-" (handy
// with rofi/fuzzel/dmenu prompts).
func quickText(args []string, stdin io.Reader) (string, error) {
	text := strings.TrimSpace(strings.Join(args, " "))
	if text == "-" {
		line, err := bufio.NewReader(stdin).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", err
		}
		text = strings.TrimSpace(line)
	}
	if text == "" {
		return "", errors.New("nothing to add")
	}
	return text, nil
}

// pushNow syncs right away so the item shows up in Google/Todoist; offline it
// stays queued for the next sync, which is not an error.
func pushNow(cfg *config.Config, st *store.Store, paths config.Paths) error {
	s := buildSyncer(cfg, st, paths)
	if !s.Enabled() {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	offline, err := s.Sync(ctx)
	switch {
	case offline:
		fmt.Println("Offline: queued, it will sync later.")
	case err != nil:
		fmt.Fprintln(os.Stderr, "caldo: saved locally, but sync failed:", err)
	}
	return nil
}

// --- caldo agenda / next ------------------------------------------------------

// item is the JSON shape of an event or task for scripts.
type item struct {
	Type      string    `json:"type"` // "event" or "task"
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Start     time.Time `json:"start"`
	End       time.Time `json:"end"`
	AllDay    bool      `json:"all_day"`
	Calendar  string    `json:"calendar,omitempty"`
	Project   string    `json:"project,omitempty"`
	Location  string    `json:"location,omitempty"`
	Recurring bool      `json:"recurring,omitempty"`
	Priority  int       `json:"priority,omitempty"` // tasks: 1 (highest) .. 4
	Overdue   bool      `json:"overdue,omitempty"`
	Ongoing   bool      `json:"ongoing,omitempty"`
	Past      bool      `json:"past,omitempty"`
}

type names struct {
	cals     []model.Calendar
	projects []model.Project
}

func loadNames(st *store.Store) (names, error) {
	var n names
	var err error
	if n.cals, err = st.Calendars(); err != nil {
		return n, err
	}
	n.projects, err = st.Projects()
	return n, err
}

func (n names) toItem(o model.Occurrence, now time.Time) item {
	it := item{Type: "event", ID: o.Event.ID, Title: o.Event.Title, Start: o.Start, End: o.End,
		AllDay: o.Event.AllDay, Location: o.Event.Location, Recurring: o.IsRecurring()}
	if t := o.Task; t != nil {
		it.Type, it.ID, it.Project = "task", t.ID, projectName(n.projects, t.ProjectID)
		it.Priority, it.Recurring = t.Priority.Display(), t.Due.IsRecurring
		it.Overdue = agenda.Day(o.Start).Before(agenda.Day(now)) || (!o.Event.AllDay && o.Start.Before(now))
		return it
	}
	it.Calendar = calendarName(n.cals, o.Event.CalendarID)
	it.Ongoing = !o.Start.After(now) && o.End.After(now)
	it.Past = !o.End.After(now)
	return it
}

func overdueItems(st *store.Store, n names, now time.Time) ([]item, error) {
	ts, err := agenda.Overdue(st, now)
	if err != nil {
		return nil, err
	}
	var out []item
	for _, o := range agenda.TaskOccurrences(ts, time.Time{}, agenda.Day(now)) {
		out = append(out, n.toItem(o, now))
	}
	return out, nil
}

func runAgenda(args []string, st *store.Store) error {
	fs := flag.NewFlagSet("agenda", flag.ContinueOnError)
	days := fs.Int("days", 1, "number of days from today")
	asJSON := fs.Bool("json", false, "print JSON")
	noTasks := fs.Bool("no-tasks", false, "events only")
	if err := fs.Parse(args); err != nil {
		return err
	}
	now := time.Now()
	from := agenda.Day(now)
	occs, err := agenda.Items(st, from, from.AddDate(0, 0, max(1, *days)), !*noTasks)
	if err != nil {
		return err
	}
	n, err := loadNames(st)
	if err != nil {
		return err
	}
	var items []item
	if !*noTasks {
		if items, err = overdueItems(st, n, now); err != nil {
			return err
		}
	}
	for _, o := range occs {
		items = append(items, n.toItem(o, now))
	}
	if *asJSON {
		return printJSON(items)
	}
	lastDay := time.Time{}
	for _, it := range items {
		day := agenda.Day(it.Start)
		if day.Before(from) {
			day = from // overdue tasks and events that began earlier go under today
		}
		if !day.Equal(lastDay) {
			if !lastDay.IsZero() {
				fmt.Println()
			}
			fmt.Println(dayLabel(day, now))
			lastDay = day
		}
		fmt.Println("  " + itemLine(it, now))
	}
	return nil
}

func runNext(args []string, st *store.Store) error {
	fs := flag.NewFlagSet("next", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print JSON")
	noTasks := fs.Bool("no-tasks", false, "events only")
	if err := fs.Parse(args); err != nil {
		return err
	}
	now := time.Now()
	occs, err := agenda.Items(st, agenda.Day(now), agenda.Day(now).AddDate(0, 0, 8), !*noTasks)
	if err != nil {
		return err
	}
	o, ok := agenda.Next(occs, now)
	if !ok {
		os.Exit(1) // nothing in the next week: `caldo next || echo free`
	}
	n, err := loadNames(st)
	if err != nil {
		return err
	}
	it := n.toItem(o, now)
	if *asJSON {
		return printJSON(it)
	}
	fmt.Println(nextText(it, now, 0))
	return nil
}

// --- caldo waybar -------------------------------------------------------------

// pango escapes text for waybar's Pango-markup tooltips.
var pango = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

// waybarOut is a custom module's "return-type": "json" payload.
type waybarOut struct {
	Text    string   `json:"text"`
	Tooltip string   `json:"tooltip"`
	Class   []string `json:"class"`
	Alt     string   `json:"alt"`
}

func runWaybar(args []string, st *store.Store) error {
	fs := flag.NewFlagSet("waybar", flag.ContinueOnError)
	width := fs.Int("width", 30, "max title length in the bar")
	soon := fs.Duration("soon", 15*time.Minute, "how close an event counts as \"soon\"")
	if err := fs.Parse(args); err != nil {
		return err
	}
	now := time.Now()
	today := agenda.Day(now)
	occs, err := agenda.Items(st, today, today.AddDate(0, 0, 2), true)
	if err != nil {
		return err
	}
	n, err := loadNames(st)
	if err != nil {
		return err
	}
	overdue, err := overdueItems(st, n, now)
	if err != nil {
		return err
	}

	var todayEvents, todayTasks, tomorrow []item
	var next *item
	for _, o := range occs {
		it := n.toItem(o, now)
		switch {
		case agenda.Day(o.Start).After(today):
			if it.Type == "event" {
				tomorrow = append(tomorrow, it)
			}
		case it.Type == "task":
			todayTasks = append(todayTasks, it)
		case !it.Past:
			todayEvents = append(todayEvents, it)
		}
	}
	if o, ok := agenda.Next(occs, now); ok && agenda.Day(o.Start).Equal(today) && o.Task == nil {
		it := n.toItem(o, now)
		next = &it
	}

	out := waybarOut{Class: []string{}}
	switch {
	case next == nil:
		out.Text, out.Alt = "No more events", "free"
	case next.Ongoing:
		out.Text, out.Alt = nextText(*next, now, *width), "ongoing"
	case next.Start.Sub(now) <= *soon:
		out.Text, out.Alt = nextText(*next, now, *width), "soon"
	default:
		out.Text, out.Alt = nextText(*next, now, *width), "upcoming"
	}
	out.Class = append(out.Class, out.Alt)
	if open := len(todayTasks) + len(overdue); open > 0 {
		out.Text += fmt.Sprintf(" · ☐ %d", open)
	}
	if len(overdue) > 0 {
		out.Class = append(out.Class, "overdue")
	}

	var tip []string
	section := func(title string, items []item) {
		if len(items) == 0 {
			return
		}
		if len(tip) > 0 {
			tip = append(tip, "")
		}
		tip = append(tip, "<b>"+pango.Replace(title)+"</b>")
		for _, it := range items {
			tip = append(tip, pango.Replace(itemLine(it, now)))
		}
	}
	section(dayLabel(today, now), todayEvents)
	section("Overdue", overdue)
	section("Tasks today", todayTasks)
	section(dayLabel(today.AddDate(0, 0, 1), now), tomorrow)
	if len(tip) == 0 {
		tip = append(tip, "Nothing planned")
	}
	out.Tooltip = strings.Join(tip, "\n")
	return printJSON(out)
}

// --- formatting ---------------------------------------------------------------

func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

func dayLabel(day, now time.Time) string {
	today := agenda.Day(now)
	s := day.Format("Mon 2 Jan")
	switch {
	case day.Equal(today):
		return s + " · today"
	case day.Equal(today.AddDate(0, 0, 1)):
		return s + " · tomorrow"
	}
	return s
}

// itemLine is one agenda row: time column, title, then where it belongs.
func itemLine(it item, now time.Time) string {
	when := "all day"
	switch {
	case it.Type == "task" && it.Overdue && agenda.Day(it.Start).Before(agenda.Day(now)):
		when = "overdue"
	case !it.AllDay && it.Start.Equal(it.End):
		when = it.Start.Format("15:04")
	case !it.AllDay:
		when = it.Start.Format("15:04") + "–" + it.End.Format("15:04")
	}
	title := it.Title
	if it.Type == "task" {
		title = "☐ " + title
		if it.Priority < 4 {
			title += fmt.Sprintf(" p%d", it.Priority)
		}
	}
	if it.Recurring {
		title += " ↻"
	}
	where := it.Calendar
	if it.Type == "task" {
		where = it.Project
	}
	return fmt.Sprintf("%-11s  %s  · %s", when, title, where)
}

// nextText describes the next item relative to now, e.g. "13:00 Lunch · in 20m".
func nextText(it item, now time.Time, width int) string {
	title := it.Title
	if width > 0 && len([]rune(title)) > width {
		title = string([]rune(title)[:width-1]) + "…"
	}
	if it.Ongoing {
		return fmt.Sprintf("%s · until %s", title, it.End.Format("15:04"))
	}
	when := it.Start.Format("15:04")
	if !agenda.Day(it.Start).Equal(agenda.Day(now)) {
		when = dayLabel(agenda.Day(it.Start), now) + " " + when
		if it.AllDay {
			when = dayLabel(agenda.Day(it.Start), now)
		}
		return when + " " + title
	}
	return fmt.Sprintf("%s %s · in %s", when, title, shortDur(it.Start.Sub(now)))
}

func shortDur(d time.Duration) string {
	d = (d + time.Minute - 1).Truncate(time.Minute)
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	h, m := int(d.Hours()), int(d.Minutes())%60
	if m == 0 {
		return fmt.Sprintf("%dh", h)
	}
	return fmt.Sprintf("%dh%02dm", h, m)
}

func whenText(o model.Occurrence) string {
	if o.Event.AllDay {
		return o.Start.Format("Mon 2 Jan") + " (all day)"
	}
	return o.Start.Format("Mon 2 Jan 15:04") + "–" + o.End.Format("15:04")
}

func calendarName(cals []model.Calendar, id string) string {
	for _, c := range cals {
		if c.ID == id {
			return c.Name
		}
	}
	return ""
}

func projectName(projects []model.Project, id string) string {
	for _, p := range projects {
		if p.ID == id {
			return p.Name
		}
	}
	return ""
}
