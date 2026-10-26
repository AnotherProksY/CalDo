// Package syncer runs provider sync cycles and classifies failures.
package syncer

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"sync"
	"syscall"
)

// Provider syncs one remote service with the local store.
type Provider interface {
	Name() string
	Sync(ctx context.Context) error
}

type Syncer struct {
	mu        sync.Mutex
	providers []Provider
	// LockFile, when set, is flock'ed around each cycle so the TUI and a
	// background `caldo sync` never push the same outbox entries twice.
	LockFile string
}

func New(providers ...Provider) *Syncer {
	return &Syncer{providers: providers}
}

func (s *Syncer) Enabled() bool { return len(s.providers) > 0 }

// Sync runs every provider in turn. offline is true when any provider could not
// reach its server; queued changes stay in the outbox for the next attempt.
func (s *Syncer) Sync(ctx context.Context) (offline bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.LockFile != "" {
		unlock, err := lockFile(s.LockFile)
		if err != nil {
			return false, fmt.Errorf("sync lock: %w", err)
		}
		defer unlock()
	}
	var errs []error
	for _, p := range s.providers {
		if perr := p.Sync(ctx); perr != nil {
			if IsOffline(perr) {
				offline = true
			}
			errs = append(errs, fmt.Errorf("%s: %w", p.Name(), perr))
		}
	}
	return offline, errors.Join(errs...)
}

// lockFile blocks until it holds an exclusive lock on path.
func lockFile(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}

// IsOffline reports network-level failures: DNS, refused connections, timeouts.
func IsOffline(err error) bool {
	var ue *url.Error
	var ne net.Error
	var de *net.DNSError
	var oe *net.OpError
	return errors.As(err, &ue) || errors.As(err, &ne) || errors.As(err, &de) || errors.As(err, &oe) ||
		errors.Is(err, context.DeadlineExceeded)
}
