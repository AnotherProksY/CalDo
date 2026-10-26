package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/term"

	"caldo/internal/auth"
	"caldo/internal/config"
	"caldo/internal/provider/todoist"
)

const todoistTokenHelp = `  1. Open https://app.todoist.com/app/settings/integrations/developer
     (Todoist → Settings → Integrations → Developer).
  2. Copy the "API token" and paste it below.`

var stdin = bufio.NewReader(os.Stdin)

// setupMarker records that first-run setup ran, so skipped steps are not
// asked again. `caldo auth` connects a service later.
func setupMarker(paths config.Paths) string {
	return filepath.Join(paths.DataDir, "setup-done")
}

// maybeRunSetup runs the first-run setup before the TUI starts: Todoist
// token first, then the Google browser consent.
func maybeRunSetup(cfg *config.Config, paths config.Paths) error {
	marker := setupMarker(paths)
	if _, err := os.Stat(marker); err == nil || !term.IsTerminal(int(os.Stdin.Fd())) {
		return nil
	}
	secrets := &auth.Secrets{File: paths.SecretsFile}
	_, terr := secrets.Get(auth.KeyTodoistToken)
	_, gerr := secrets.Get(auth.KeyGoogleToken)
	if terr != nil || gerr != nil {
		if err := runSetup(cfg, paths, secrets, terr == nil, gerr == nil); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(paths.DataDir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(marker, nil, 0o600)
}

func runSetup(cfg *config.Config, paths config.Paths, secrets *auth.Secrets, hasTodoist, hasGoogle bool) error {
	fmt.Print("Welcome to CalDo! Let's connect your accounts.\n" +
		"Everything works offline too: press Enter to skip a step and connect it later with `caldo auth`.\n\n")
	usedFile := false

	fmt.Println("1/2  Todoist")
	if hasTodoist {
		fmt.Print("  Already connected.\n\n")
	} else {
		fmt.Println(todoistTokenHelp)
		ok, f, err := connectTodoist(cfg, secrets, true)
		if err != nil {
			return err
		}
		usedFile = usedFile || f
		if !ok {
			fmt.Println("  Skipped.")
		}
		fmt.Println()
	}

	fmt.Println("2/2  Google Calendar")
	switch {
	case hasGoogle:
		fmt.Print("  Already connected.\n\n")
	default:
		if _, err := auth.GoogleConfig(cfg.Google.ClientSecretFile); err != nil {
			fmt.Printf("  Skipped: %v\n\n", err)
			break
		}
		fmt.Print("  Press Enter to sign in with Google in your browser, or type s to skip: ")
		line, err := stdin.ReadString('\n')
		if err != nil {
			return err
		}
		if strings.EqualFold(strings.TrimSpace(line), "s") {
			fmt.Print("  Skipped.\n\n")
			break
		}
		f, err := connectGoogle(cfg, secrets)
		if err != nil {
			fmt.Printf("  Google sign-in failed: %v\n  Try again later with `caldo auth google`.\n\n", err)
			break
		}
		usedFile = usedFile || f
		fmt.Print("  Google Calendar connected.\n\n")
	}

	if usedFile {
		fmt.Printf("Note: no system keyring available; credentials are stored in %s (mode 0600).\n", paths.SecretsFile)
	}
	return nil
}

// connectTodoist asks for an API token until Todoist accepts it. With
// optional, an empty answer skips the step (ok is false).
func connectTodoist(cfg *config.Config, secrets *auth.Secrets, optional bool) (ok, usedFile bool, err error) {
	for {
		prompt := "  API token: "
		if optional {
			prompt = "  API token (Enter to skip): "
		}
		tok, err := readSecret(prompt)
		if err != nil {
			return false, false, err
		}
		if tok == "" {
			if optional {
				return false, false, nil
			}
			return false, false, errors.New("empty token")
		}

		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		c := todoist.New(cfg.Todoist.BaseURL, tok, &http.Client{}, nil)
		name, err := c.User(ctx)
		cancel()
		switch {
		case todoist.Rejected(err):
			fmt.Println("  Todoist rejected this token. Check that you copied all of it and try again.")
			continue
		case err != nil:
			fmt.Printf("  Could not check the token (%v); saving it anyway.\n", err)
		default:
			fmt.Printf("  Connected as %s.\n", name)
		}
		usedFile, err = secrets.Set(auth.KeyTodoistToken, tok)
		return err == nil, usedFile, err
	}
}

func connectGoogle(cfg *config.Config, secrets *auth.Secrets) (usedFile bool, err error) {
	oc, err := auth.GoogleConfig(cfg.Google.ClientSecretFile)
	if err != nil {
		return false, err
	}
	tok, err := auth.GoogleLogin(context.Background(), oc, func(s string) { fmt.Println(s) })
	if err != nil {
		return false, err
	}
	return auth.SaveGoogleToken(secrets, tok)
}

// readSecret reads a line without echo when stdin is a terminal.
func readSecret(prompt string) (string, error) {
	fmt.Print(prompt)
	if term.IsTerminal(int(os.Stdin.Fd())) {
		b, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Println()
		return strings.TrimSpace(string(b)), err
	}
	line, err := stdin.ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimSpace(line), nil
}
