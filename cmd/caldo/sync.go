package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/term"
	"google.golang.org/api/option"

	"caldo/internal/auth"
	"caldo/internal/config"
	"caldo/internal/provider/google"
	"caldo/internal/provider/todoist"
	"caldo/internal/store"
	"caldo/internal/syncer"
	"caldo/internal/ui"
)

// buildSyncer wires every provider that has credentials. Missing credentials
// just leave that provider out; CalDo still works locally.
func buildSyncer(cfg *config.Config, st *store.Store, paths config.Paths) *syncer.Syncer {
	secrets := &auth.Secrets{File: paths.SecretsFile}
	var providers []syncer.Provider
	hc := &http.Client{Timeout: 60 * time.Second}

	if tok, err := secrets.Get(auth.KeyTodoistToken); err == nil && tok != "" {
		providers = append(providers, todoist.New(cfg.Todoist.BaseURL, tok, hc, st))
	}
	if cfg.Google.ClientSecretFile != "" {
		if oc, err := auth.GoogleConfig(cfg.Google.ClientSecretFile); err == nil {
			ctx := context.WithValue(context.Background(), oauth2.HTTPClient, hc)
			if ts, err := auth.GoogleTokenSource(ctx, oc, secrets); err == nil {
				client := oauth2.NewClient(ctx, ts)
				client.Timeout = 60 * time.Second
				if g, err := google.New(ctx, st, cfg.Google, option.WithHTTPClient(client)); err == nil {
					providers = append(providers, g)
				}
			}
		}
	}
	return syncer.New(providers...)
}

func runTUISyncer(cfg *config.Config, st *store.Store, paths config.Paths) ui.Syncer {
	s := buildSyncer(cfg, st, paths)
	if !s.Enabled() {
		return nil
	}
	return s
}

func runAuth(provider string, cfg *config.Config, paths config.Paths) error {
	secrets := &auth.Secrets{File: paths.SecretsFile}
	var usedFile bool
	switch provider {
	case "todoist":
		fmt.Print("Todoist API token (Settings → Integrations → Developer): ")
		var tok string
		if term.IsTerminal(int(os.Stdin.Fd())) {
			b, err := term.ReadPassword(int(os.Stdin.Fd()))
			fmt.Println()
			if err != nil {
				return err
			}
			tok = string(b)
		} else {
			line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
			tok = line
		}
		tok = strings.TrimSpace(tok)
		if tok == "" {
			return errors.New("empty token")
		}
		var err error
		if usedFile, err = secrets.Set(auth.KeyTodoistToken, tok); err != nil {
			return err
		}
		fmt.Println("Todoist connected.")
	case "google":
		oc, err := auth.GoogleConfig(cfg.Google.ClientSecretFile)
		if err != nil {
			return err
		}
		tok, err := auth.GoogleLogin(context.Background(), oc, func(s string) { fmt.Println(s) })
		if err != nil {
			return err
		}
		if usedFile, err = auth.SaveGoogleToken(secrets, tok); err != nil {
			return err
		}
		fmt.Println("Google Calendar connected.")
	default:
		return fmt.Errorf("unknown provider %q (use google or todoist)", provider)
	}
	if usedFile {
		fmt.Printf("Note: no system keyring available; the credential is stored in %s (mode 0600).\n", paths.SecretsFile)
	}
	return nil
}

func runSyncOnce(cfg *config.Config, st *store.Store, paths config.Paths) error {
	s := buildSyncer(cfg, st, paths)
	if !s.Enabled() {
		return errors.New("no providers configured — run `caldo auth google` or `caldo auth todoist`")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	offline, err := s.Sync(ctx)
	if offline {
		fmt.Printf("Offline: %d change(s) still queued.\n", st.PendingCount())
	}
	if err != nil {
		return err
	}
	fmt.Printf("Synced. %d pending, %d conflict(s).\n", st.PendingCount(), st.ConflictCount())
	return nil
}
