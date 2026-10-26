package syncer

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

type slowProvider struct{ running, overlap *atomic.Int32 }

func (p slowProvider) Name() string { return "slow" }

func (p slowProvider) Sync(context.Context) error {
	if p.running.Add(1) > 1 {
		p.overlap.Add(1)
	}
	time.Sleep(20 * time.Millisecond)
	p.running.Add(-1)
	return nil
}

// Two Syncers (as in the TUI and a background `caldo sync`) sharing a lock
// file never run at the same time.
func TestLockFileSerializesSyncers(t *testing.T) {
	var running, overlap atomic.Int32
	lock := filepath.Join(t.TempDir(), "sync.lock")
	done := make(chan struct{})
	for range 2 {
		s := New(slowProvider{&running, &overlap})
		s.LockFile = lock
		go func() {
			for range 3 {
				if _, err := s.Sync(context.Background()); err != nil {
					t.Error(err)
				}
			}
			done <- struct{}{}
		}()
	}
	<-done
	<-done
	if n := overlap.Load(); n != 0 {
		t.Fatalf("syncs overlapped %d times", n)
	}
}
