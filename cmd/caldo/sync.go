package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"time"

	"golang.org/x/oauth2"
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
	s := syncer.New(providers...)
	s.LockFile = filepath.Join(filepath.Dir(paths.DBFile), "sync.lock")
	return s
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
		fmt.Println(todoistTokenHelp)
		var err error
		if _, usedFile, err = connectTodoist(cfg, secrets, false); err != nil {
			return err
		}
	case "google":
		var err error
		if usedFile, err = connectGoogle(cfg, secrets); err != nil {
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
