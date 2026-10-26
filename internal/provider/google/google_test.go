package google

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	calendar "google.golang.org/api/calendar/v3"
	"google.golang.org/api/option"

	"caldo/internal/config"
	"caldo/internal/model"
	"caldo/internal/store"
)

// fakeCalendar is a minimal in-memory Google Calendar API for one calendar.
type fakeCalendar struct {
	mu        sync.Mutex
	events    map[string]*calendar.Event
	changed   map[string]bool // ids changed since the last list
	next      int
	expireTok bool
	lists     []string // sync tokens received, "" for full syncs
}

func newFakeCalendar() *fakeCalendar {
	f := &fakeCalendar{events: map[string]*calendar.Event{}, changed: map[string]bool{}}
	start := time.Now().Add(24 * time.Hour).Truncate(time.Hour)
	f.put(&calendar.Event{Id: "single", Summary: "Single", Status: "confirmed",
		Start: &calendar.EventDateTime{DateTime: start.Format(time.RFC3339)},
		End:   &calendar.EventDateTime{DateTime: start.Add(time.Hour).Format(time.RFC3339)}})
	f.put(&calendar.Event{Id: "series", Summary: "Weekly", Status: "confirmed", Recurrence: []string{"RRULE:FREQ=WEEKLY"},
		Start: &calendar.EventDateTime{DateTime: start.Format(time.RFC3339), TimeZone: "UTC"},
		End:   &calendar.EventDateTime{DateTime: start.Add(time.Hour).Format(time.RFC3339), TimeZone: "UTC"}})
	week := start.AddDate(0, 0, 7)
	f.put(&calendar.Event{Id: "series_x", Summary: "Weekly", Status: "cancelled", RecurringEventId: "series",
		OriginalStartTime: &calendar.EventDateTime{DateTime: week.Format(time.RFC3339)}})
	return f
}

func (f *fakeCalendar) put(e *calendar.Event) {
	f.next++
	e.Etag = fmt.Sprintf(`"e%d"`, f.next)
	e.Updated = time.Now().UTC().Format(time.RFC3339)
	f.events[e.Id] = e
	f.changed[e.Id] = true
}

func (f *fakeCalendar) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	path := strings.TrimPrefix(r.URL.Path, "/")
	write := func(v any) { json.NewEncoder(w).Encode(v) }
	switch {
	case path == "users/me/calendarList":
		write(calendar.CalendarList{Items: []*calendar.CalendarListEntry{
			{Id: "me@example.com", Summary: "Me", Primary: true, AccessRole: "owner", BackgroundColor: "#4285f4"},
		}})
	case strings.HasSuffix(path, "/events") && r.Method == http.MethodGet:
		tok := r.URL.Query().Get("syncToken")
		f.lists = append(f.lists, tok)
		if tok != "" && f.expireTok {
			f.expireTok = false
			w.WriteHeader(http.StatusGone)
			write(map[string]any{"error": map[string]any{"code": 410, "message": "gone"}})
			return
		}
		var items []*calendar.Event
		for id, e := range f.events {
			if tok == "" || f.changed[id] {
				items = append(items, e)
			}
		}
		f.changed = map[string]bool{}
		write(calendar.Events{Items: items, NextSyncToken: "tok"})
	case strings.HasSuffix(path, "/events") && r.Method == http.MethodPost:
		var e calendar.Event
		json.NewDecoder(r.Body).Decode(&e)
		e.Id = fmt.Sprintf("new%d", f.next)
		f.put(&e)
		delete(f.changed, e.Id)
		write(e)
	case strings.Contains(path, "/events/"):
		id := path[strings.LastIndex(path, "/")+1:]
		cur := f.events[id]
		if cur == nil {
			w.WriteHeader(http.StatusNotFound)
			write(map[string]any{"error": map[string]any{"code": 404, "message": "not found"}})
			return
		}
		switch r.Method {
		case http.MethodGet:
			write(cur)
		case http.MethodPatch:
			if m := r.Header.Get("If-Match"); m != "" && m != cur.Etag {
				w.WriteHeader(http.StatusPreconditionFailed)
				write(map[string]any{"error": map[string]any{"code": 412, "message": "precondition failed"}})
				return
			}
			var patch calendar.Event
			json.NewDecoder(r.Body).Decode(&patch)
			cur.Summary = patch.Summary
			f.put(cur)
			delete(f.changed, id)
			write(cur)
		case http.MethodDelete:
			delete(f.events, id)
			w.WriteHeader(http.StatusNoContent)
		}
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func setup(t *testing.T) (*Client, *store.Store, *fakeCalendar) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	f := newFakeCalendar()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	c, err := New(context.Background(), st, config.Default().Google,
		option.WithEndpoint(srv.URL+"/"), option.WithHTTPClient(srv.Client()))
	if err != nil {
		t.Fatal(err)
	}
	return c, st, f
}

func primary(t *testing.T, st *store.Store) model.Calendar {
	t.Helper()
	cals, _ := st.Calendars()
	for _, c := range cals {
		if c.Provider == model.ProviderGoogle {
			return c
		}
	}
	t.Fatal("google calendar not synced")
	return model.Calendar{}
}

func TestPullWithRecurringException(t *testing.T) {
	c, st, _ := setup(t)
	if err := c.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	cal := primary(t, st)
	master, _ := st.EventByRemoteID(cal.ID, "series")
	ex, _ := st.EventByRemoteID(cal.ID, "series_x")
	if master == nil || !master.IsRecurring() {
		t.Fatalf("master not stored: %+v", master)
	}
	if ex == nil || ex.RecurringEventID != master.ID || ex.Status != model.StatusCancelled || ex.OriginalStart == nil {
		t.Fatalf("exception not linked: %+v", ex)
	}
}

func TestPushInsertAndPatch(t *testing.T) {
	c, st, f := setup(t)
	ctx := context.Background()
	must(t, c.Sync(ctx))
	cal := primary(t, st)

	start := time.Now().Add(48 * time.Hour).Truncate(time.Hour)
	e := &model.Event{CalendarID: cal.ID, Title: "From CalDo", Start: start, End: start.Add(time.Hour)}
	must(t, st.SaveEvent(e))
	must(t, c.Sync(ctx))
	got, _ := st.Event(e.ID)
	if got.RemoteID == "" || got.Dirty || f.events[got.RemoteID] == nil {
		t.Fatalf("insert not pushed: %+v", got)
	}

	got.Title = "Renamed"
	must(t, st.SaveEvent(got))
	must(t, c.Sync(ctx))
	if s := f.events[got.RemoteID].Summary; s != "Renamed" {
		t.Fatalf("patch not applied on server: %q", s)
	}
	if st.PendingCount() != 0 {
		t.Fatal("outbox should be empty")
	}
}

func TestPushConflictServerWins(t *testing.T) {
	c, st, f := setup(t)
	ctx := context.Background()
	must(t, c.Sync(ctx))
	cal := primary(t, st)
	local, _ := st.EventByRemoteID(cal.ID, "single")

	local.Title = "Local edit"
	must(t, st.SaveEvent(local))
	f.mu.Lock()
	f.events["single"].Summary = "Server edit"
	f.put(f.events["single"])
	f.mu.Unlock()

	// The push gets 412 Precondition Failed; the server version is kept.
	must(t, c.Sync(ctx))
	got, _ := st.Event(local.ID)
	if got.Title != "Server edit" {
		t.Fatalf("server should win, got %q", got.Title)
	}
	if f.events["single"].Summary != "Server edit" {
		t.Fatal("local edit must not overwrite the server")
	}
	if cs, _ := st.Conflicts(); len(cs) != 1 {
		t.Fatalf("want 1 conflict, got %d", len(cs))
	}
}

func TestExpiredSyncTokenTriggersFullSync(t *testing.T) {
	c, st, f := setup(t)
	ctx := context.Background()
	must(t, c.Sync(ctx))
	f.mu.Lock()
	f.expireTok = true
	f.lists = nil
	f.mu.Unlock()
	must(t, c.Sync(ctx))
	if len(f.lists) != 2 || f.lists[0] != "tok" || f.lists[1] != "" {
		t.Fatalf("expected incremental then full list, got %q", f.lists)
	}
	cal := primary(t, st)
	if e, _ := st.EventByRemoteID(cal.ID, "single"); e == nil {
		t.Fatal("events should survive a full resync")
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestExcludeCalendarRemovesIt(t *testing.T) {
	c, st, _ := setup(t)
	ctx := context.Background()
	if err := c.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	cal := primary(t, st)
	ev, _ := st.EventByRemoteID(cal.ID, "single")
	ev.Title = "edited, then excluded"
	if err := st.SaveEvent(ev); err != nil {
		t.Fatal(err)
	}

	c.cfg.ExcludeCalendars = []string{"me"} // names match case-insensitively
	if err := c.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	cals, _ := st.Calendars()
	for _, cl := range cals {
		if cl.Provider == model.ProviderGoogle {
			t.Fatalf("excluded calendar still present: %+v", cl)
		}
	}
	if n := st.PendingCount(); n != 0 {
		t.Fatalf("queued pushes for the excluded calendar left behind: %d", n)
	}
	if !c.wanted("other@example.com", "Other") || c.wanted("me@example.com", "Me") {
		t.Fatal("wanted() should only drop excluded calendars")
	}
}
