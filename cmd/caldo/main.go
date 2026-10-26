// Command caldo is a terminal calendar + task manager with two-way sync to
// Google Calendar and Todoist.
package main

import (
	"flag"
	"fmt"
	"os"

	tea "charm.land/bubbletea/v2"

	"caldo/internal/config"
	"caldo/internal/store"
	"caldo/internal/ui"
)

const usage = `Usage: caldo [flags] [command]

Commands:
  (none)          start the TUI (the first run asks to connect Todoist and Google)
  auth google     connect Google Calendar (opens a browser)
  auth todoist    connect Todoist (paste your API token)
  sync            run one sync cycle and exit
  notify          show desktop notifications for due reminders and exit
  service install|uninstall
                  run notify every minute and sync every 15 minutes in the
                  background (systemd user timers / launchd agents)
  demo            fill the database with sample data (use with --db)

Flags:
`

func main() {
	paths, err := config.DefaultPaths()
	if err != nil {
		fatal(err)
	}
	cfgPath := flag.String("config", paths.ConfigFile, "config file")
	dbPath := flag.String("db", paths.DBFile, "database file")
	flag.Usage = func() {
		fmt.Fprint(os.Stderr, usage)
		flag.PrintDefaults()
	}
	flag.Parse()
	paths.ConfigFile, paths.DBFile = *cfgPath, *dbPath

	cfg, err := config.Load(paths.ConfigFile)
	if err != nil {
		fatal(err)
	}
	st, err := store.Open(paths.DBFile)
	if err != nil {
		fatal(err)
	}
	defer st.Close()

	args := flag.Args()
	cmd := ""
	if len(args) > 0 {
		cmd = args[0]
	}
	switch cmd {
	case "":
		if err := maybeRunSetup(cfg, paths); err != nil {
			fatal(err)
		}
		runTUI(cfg, st, paths)
	case "demo":
		if err := seedDemo(st); err != nil {
			fatal(err)
		}
		fmt.Println("Sample data added to", paths.DBFile)
	case "auth":
		if len(args) < 2 {
			fatal(fmt.Errorf("usage: caldo auth google|todoist"))
		}
		if err := runAuth(args[1], cfg, paths); err != nil {
			fatal(err)
		}
	case "sync":
		if err := runSyncOnce(cfg, st, paths); err != nil {
			fatal(err)
		}
	case "notify":
		if err := runNotify(cfg, st); err != nil {
			fatal(err)
		}
	case "service":
		if err := runService(args[1:]); err != nil {
			fatal(err)
		}
	default:
		flag.Usage()
		os.Exit(2)
	}
}

func runTUI(cfg *config.Config, st *store.Store, paths config.Paths) {
	p := tea.NewProgram(ui.New(cfg, st, runTUISyncer(cfg, st, paths)))
	if _, err := p.Run(); err != nil {
		fatal(err)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "caldo:", err)
	os.Exit(1)
}
