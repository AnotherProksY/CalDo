// Package auth stores provider credentials (system keyring, file fallback)
// and runs the Google OAuth loopback flow.
package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/zalando/go-keyring"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	calendar "google.golang.org/api/calendar/v3"
)

const service = "caldo"

const (
	KeyGoogleToken  = "google-token"
	KeyTodoistToken = "todoist-token"
)

var ErrNotFound = errors.New("credential not found")

// Secrets reads and writes credentials, preferring the system keyring and
// falling back to a 0600 JSON file when no keyring service is available.
type Secrets struct {
	File string
	mu   sync.Mutex
}

func (s *Secrets) Get(key string) (string, error) {
	v, err := keyring.Get(service, key)
	if err == nil {
		return v, nil
	}
	m, ferr := s.readFile()
	if ferr != nil {
		return "", ferr
	}
	if v, ok := m[key]; ok {
		return v, nil
	}
	return "", ErrNotFound
}

// Set stores a secret. usedFile reports that the keyring was unavailable.
func (s *Secrets) Set(key, value string) (usedFile bool, err error) {
	if err := keyring.Set(service, key, value); err == nil {
		// Drop any stale plaintext copy.
		s.deleteFromFile(key)
		return false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.readFile()
	if err != nil {
		return true, err
	}
	m[key] = value
	return true, s.writeFile(m)
}

func (s *Secrets) Delete(key string) {
	keyring.Delete(service, key)
	s.deleteFromFile(key)
}

func (s *Secrets) deleteFromFile(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if m, err := s.readFile(); err == nil {
		if _, ok := m[key]; ok {
			delete(m, key)
			_ = s.writeFile(m) // best effort: the keyring copy is authoritative
		}
	}
}

func (s *Secrets) readFile() (map[string]string, error) {
	m := map[string]string{}
	b, err := os.ReadFile(s.File)
	if os.IsNotExist(err) {
		return m, nil
	}
	if err != nil {
		return nil, err
	}
	return m, json.Unmarshal(b, &m)
}

func (s *Secrets) writeFile(m map[string]string) error {
	if err := os.MkdirAll(filepath.Dir(s.File), 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(m, "", "  ")
	return os.WriteFile(s.File, b, 0o600)
}

// --- Google --------------------------------------------------------------

// GoogleConfig loads the user's Desktop OAuth client.
func GoogleConfig(clientSecretFile string) (*oauth2.Config, error) {
	if clientSecretFile == "" {
		return nil, errors.New("set [google] client_secret_file in config.toml to your Desktop OAuth client JSON " +
			"(Google Cloud Console → APIs & Services → Credentials)")
	}
	b, err := os.ReadFile(clientSecretFile)
	if err != nil {
		return nil, err
	}
	return google.ConfigFromJSON(b, calendar.CalendarScope)
}

// GoogleLogin runs the loopback + PKCE flow and returns a token with a refresh token.
func GoogleLogin(ctx context.Context, cfg *oauth2.Config, out func(string)) (*oauth2.Token, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	defer ln.Close()
	cfg.RedirectURL = fmt.Sprintf("http://%s/", ln.Addr())

	stateBytes := make([]byte, 16)
	rand.Read(stateBytes)
	state := hex.EncodeToString(stateBytes)
	verifier := oauth2.GenerateVerifier()
	url := cfg.AuthCodeURL(state, oauth2.AccessTypeOffline, oauth2.ApprovalForce, oauth2.S256ChallengeOption(verifier))

	type result struct {
		code string
		err  error
	}
	ch := make(chan result, 1)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		switch {
		case q.Get("state") != state:
			http.Error(w, "state mismatch", http.StatusBadRequest)
			return
		case q.Get("error") != "":
			_, _ = fmt.Fprintln(w, "Authorization failed; you can close this tab.")
			ch <- result{err: fmt.Errorf("authorization failed: %s", q.Get("error"))}
		default:
			_, _ = fmt.Fprintln(w, "CalDo is connected to Google Calendar. You can close this tab.")
			ch <- result{code: q.Get("code")}
		}
	})}
	go func() { _ = srv.Serve(ln) }() // returns ErrServerClosed on shutdown
	defer func() { _ = srv.Shutdown(context.Background()) }()

	out("Opening your browser to authorize CalDo. If it does not open, visit:\n\n  " + url + "\n")
	openBrowser(url)

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(5 * time.Minute):
		return nil, errors.New("timed out waiting for authorization")
	case r := <-ch:
		if r.err != nil {
			return nil, r.err
		}
		return cfg.Exchange(ctx, r.code, oauth2.VerifierOption(verifier))
	}
}

func openBrowser(url string) {
	for _, c := range []string{"xdg-open", "open"} {
		if p, err := exec.LookPath(c); err == nil {
			_ = exec.Command(p, url).Start() // the URL is also printed
			return
		}
	}
}

// GoogleTokenSource returns a token source that persists refreshed tokens.
func GoogleTokenSource(ctx context.Context, cfg *oauth2.Config, s *Secrets) (oauth2.TokenSource, error) {
	raw, err := s.Get(KeyGoogleToken)
	if err != nil {
		return nil, err
	}
	var tok oauth2.Token
	if err := json.Unmarshal([]byte(raw), &tok); err != nil {
		return nil, fmt.Errorf("stored Google token: %w", err)
	}
	return &savingSource{base: cfg.TokenSource(ctx, &tok), last: tok.AccessToken, secrets: s}, nil
}

func SaveGoogleToken(s *Secrets, tok *oauth2.Token) (bool, error) {
	b, _ := json.Marshal(tok)
	return s.Set(KeyGoogleToken, string(b))
}

type savingSource struct {
	base    oauth2.TokenSource
	mu      sync.Mutex
	last    string
	secrets *Secrets
}

func (s *savingSource) Token() (*oauth2.Token, error) {
	t, err := s.base.Token()
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if t.AccessToken != s.last {
		s.last = t.AccessToken
		// A failed save only means refreshing again next run.
		_, _ = SaveGoogleToken(s.secrets, t)
	}
	return t, nil
}
