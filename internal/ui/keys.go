package ui

import (
	"strings"

	"caldo/internal/config"
)

type keymap struct {
	byKey    map[string]string
	byAction map[string][]string
}

func newKeymap(bindings map[string][]string) keymap {
	km := keymap{byKey: map[string]string{}, byAction: bindings}
	for action, keys := range bindings {
		for _, k := range keys {
			km.byKey[k] = action
		}
	}
	return km
}

func (k keymap) action(key string) string {
	if key == " " {
		key = "space"
	}
	return k.byKey[key]
}

func (k keymap) keys(action string) string {
	return strings.Join(k.byAction[action], "/")
}

// helpSections drives the ? overlay; order here is the order shown.
var helpSections = []struct {
	title   string
	actions [][2]string
}{
	{"Global", [][2]string{
		{config.ActToggleFocus, "switch pane (calendar ⇄ tasks)"},
		{config.ActToggleSplit, "toggle split / full screen"},
		{config.ActSync, "sync now"},
		{config.ActSearch, "filter by text (esc clears)"},
		{config.ActFilters, "choose calendars & projects"},
		{config.ActConflicts, "sync conflicts"},
		{config.ActHelp, "help"},
		{config.ActQuit, "quit"},
	}},
	{"Navigation", [][2]string{
		{config.ActLeft, "previous day / group"},
		{config.ActRight, "next day / group"},
		{config.ActUp, "up (week in grid, item in list)"},
		{config.ActDown, "down"},
		{config.ActSubFocus, "month grid ⇄ day events"},
		{config.ActPrevPeriod, "previous month / week"},
		{config.ActNextPeriod, "next month / week"},
		{config.ActToday, "jump to today"},
		{config.ActTop, "first item"},
		{config.ActBottom, "last item"},
	}},
	{"Calendar views", [][2]string{
		{config.ActViewMonth, "calendar: month · tasks: today"},
		{config.ActViewDays, "calendar: 3 days · tasks: upcoming"},
		{config.ActViewWeek, "calendar: week · tasks: all"},
		{config.ActViewAgenda, "calendar: agenda"},
	}},
	{"Items", [][2]string{
		{config.ActAdd, "quick add"},
		{config.ActAddFull, "add in $EDITOR"},
		{config.ActEdit, "edit in $EDITOR"},
		{config.ActOpen, "details"},
		{config.ActComplete, "complete / reopen task"},
		{config.ActDelete, "delete"},
		{config.ActPostpone, "move event / reschedule task"},
		{config.ActPriority, "cycle task priority"},
		{config.ActGroupBy, "group by project/date/day/label (per view)"},
		{config.ActShowDone, "show / hide completed tasks"},
	}},
}
