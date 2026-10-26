package config

import "time"

func Default() *Config {
	c := &Config{
		General: General{Layout: "split", SplitRatio: 0.55},
		Calendar: CalendarConfig{
			WeekStart:       "monday",
			DefaultView:     "month",
			AgendaDays:      14,
			DefaultDuration: duration{time.Hour},
			TimeFormat:      "15:04",
		},
		Tasks:   TasksConfig{GroupBy: "project"},
		Sync:    SyncConfig{Interval: duration{5 * time.Minute}},
		Google:  GoogleConfig{PastDays: 365},
		Todoist: TodoistConfig{BaseURL: "https://api.todoist.com/api/v1"},
		Reminders: RemindersConfig{
			Timed:   []string{"10m"},
			AllDay:  []string{"09:00"},
			Urgency: "critical",
		},
		Theme: Theme{
			Accent:    "#7aa2f7",
			Muted:     "#565f89",
			Border:    "#3b4261",
			Today:     "#e0af68",
			Success:   "#9ece6a",
			Pending:   "#bb9af7",
			Warning:   "#e0af68",
			Danger:    "#f7768e",
			Selection: "#283457",
		},
		Keys: DefaultKeys(),
	}
	if err := c.parseReminders(); err != nil {
		panic(err) // the built-in defaults are always valid
	}
	return c
}

// Action names used as keys in the [keys] config table.
const (
	ActQuit        = "quit"
	ActToggleFocus = "toggle_focus"
	ActToggleSplit = "toggle_split"
	ActViewMonth   = "view_month"
	ActViewWeek    = "view_week"
	ActViewAgenda  = "view_agenda"
	ActLeft        = "left"
	ActDown        = "down"
	ActUp          = "up"
	ActRight       = "right"
	ActSubFocus    = "sub_focus"
	ActTop         = "top"
	ActBottom      = "bottom"
	ActToday       = "today"
	ActPrevPeriod  = "prev_period"
	ActNextPeriod  = "next_period"
	ActAdd         = "add"
	ActAddFull     = "add_in_editor"
	ActEdit        = "edit"
	ActOpen        = "open"
	ActComplete    = "complete"
	ActDelete      = "delete"
	ActPostpone    = "postpone"
	ActPriority    = "priority"
	ActSync        = "sync"
	ActSearch      = "search"
	ActFilters     = "filters"
	ActGroupBy     = "group_by"
	ActShowDone    = "toggle_completed"
	ActConflicts   = "conflicts"
	ActHelp        = "help"
)

func DefaultKeys() map[string][]string {
	return map[string][]string{
		ActQuit:        {"q", "ctrl+c"},
		ActToggleFocus: {"space"},
		ActToggleSplit: {"\\"},
		ActViewMonth:   {"1"},
		ActViewWeek:    {"2"},
		ActViewAgenda:  {"3"},
		ActLeft:        {"h", "left"},
		ActDown:        {"j", "down"},
		ActUp:          {"k", "up"},
		ActRight:       {"l", "right"},
		ActSubFocus:    {"tab"},
		ActTop:         {"g", "home"},
		ActBottom:      {"G", "end"},
		ActToday:       {"t"},
		ActPrevPeriod:  {"[", "pgup"},
		ActNextPeriod:  {"]", "pgdown"},
		ActAdd:         {"a"},
		ActAddFull:     {"A"},
		ActEdit:        {"e"},
		ActOpen:        {"enter"},
		ActComplete:    {"x", "c"},
		ActDelete:      {"d"},
		ActPostpone:    {"m"},
		ActPriority:    {"p"},
		ActSync:        {"r"},
		ActSearch:      {"/"},
		ActFilters:     {"f"},
		ActGroupBy:     {"s"},
		ActShowDone:    {"v"},
		ActConflicts:   {"C"},
		ActHelp:        {"?"},
	}
}
