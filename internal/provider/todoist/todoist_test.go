package todoist

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"

	"caldo/internal/model"
	"caldo/internal/store"
)

// fakeServer is a minimal in-memory Todoist Sync API.
type fakeServer struct {
	mu       sync.Mutex
	items    map[string]*wireItem
	version  int
	nextID   int
	seenUUID map[string]bool
	commands []command
}

func newFake() *fakeServer {
	return &fakeServer{
		items:    map[string]*wireItem{"100": {ID: "100", ProjectID: "p1", Content: "Server task", Priority: 1, UpdatedAt: "v0"}},
		seenUUID: map[string]bool{},
		nextID:   200,
	}
}

func (f *fakeServer) bump(it *wireItem) {
	f.version++
	it.UpdatedAt = fmt.Sprintf("v%d", f.version)
}

func (f *fakeServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer tok" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	r.ParseForm()
	resp := syncResponse{SyncToken: "t1", SyncStatus: map[string]json.RawMessage{}, TempIDMapping: map[string]string{}}
	if c := r.Form.Get("commands"); c != "" {
		var cmds []command
		json.Unmarshal([]byte(c), &cmds)
		for _, cmd := range cmds {
			f.commands = append(f.commands, cmd)
			if f.seenUUID[cmd.UUID] {
				resp.SyncStatus[cmd.UUID] = json.RawMessage(`"ok"`)
				continue
			}
			f.seenUUID[cmd.UUID] = true
			id, _ := cmd.Args["id"].(string)
			if real, ok := resp.TempIDMapping[id]; ok {
				id = real // temp ids from earlier commands in the same request
			}
			switch cmd.Type {
			case "item_add":
				f.nextID++
				nid := fmt.Sprint(f.nextID)
				it := &wireItem{ID: nid, ProjectID: cmd.Args["project_id"].(string), Content: cmd.Args["content"].(string), Priority: 1}
				f.bump(it)
				f.items[nid] = it
				resp.TempIDMapping[cmd.TempID] = nid
			case "item_update":
				it := f.items[id]
				it.Content = cmd.Args["content"].(string)
				f.bump(it)
			case "item_close":
				f.items[id].Checked = true
				f.bump(f.items[id])
			case "item_delete":
				f.items[id].IsDeleted = true
			}
			resp.SyncStatus[cmd.UUID] = json.RawMessage(`"ok"`)
		}
	}
	if tok := r.Form.Get("sync_token"); tok != "" {
		resp.FullSync = tok == "*"
		resp.Projects = []wireProject{{ID: "p1", Name: "Inbox", InboxProject: true, Color: "red"}}
		for _, it := range f.items {
			resp.Items = append(resp.Items, *it)
		}
	}
	json.NewEncoder(w).Encode(resp)
}

func setup(t *testing.T) (*Client, *store.Store, *fakeServer) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	f := newFake()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return New(srv.URL, "tok", srv.Client(), st), st, f
}

func TestPullCreatesProjectsAndTasks(t *testing.T) {
	c, st, _ := setup(t)
	if err := c.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	p, ok, _ := st.ProjectByRemoteID(model.ProviderTodoist, "p1")
	if !ok || !p.IsInbox || p.Color != "#db4035" {
		t.Fatalf("project not synced: %+v", p)
	}
	tk, _ := st.TaskByRemoteID("100")
	if tk == nil || tk.Content != "Server task" || tk.Dirty {
		t.Fatalf("task not synced: %+v", tk)
	}
}

func TestPushCreateCompleteDelete(t *testing.T) {
	c, st, f := setup(t)
	ctx := context.Background()
	must(t, c.Sync(ctx))
	p, _, _ := st.ProjectByRemoteID(model.ProviderTodoist, "p1")

	tk := &model.Task{ProjectID: p.ID, Content: "Local task"}
	must(t, st.SaveTask(tk))
	must(t, st.SetTaskChecked(tk.ID, true))
	must(t, c.Sync(ctx))

	got, _ := st.Task(tk.ID)
	if got.RemoteID == "" || got.Dirty || st.PendingCount() != 0 {
		t.Fatalf("after push: %+v pending=%d", got, st.PendingCount())
	}
	if srv := f.items[got.RemoteID]; srv == nil || !srv.Checked || srv.Content != "Local task" {
		t.Fatalf("server state: %+v", srv)
	}

	must(t, st.DeleteTask(tk.ID))
	must(t, c.Sync(ctx))
	if _, err := st.Task(tk.ID); err == nil {
		t.Fatal("deleted task should be purged after push")
	}
	if !f.items[got.RemoteID].IsDeleted {
		t.Fatal("server item should be deleted")
	}
}

func TestConflictServerWins(t *testing.T) {
	c, st, f := setup(t)
	ctx := context.Background()
	must(t, c.Sync(ctx))
	tk, _ := st.TaskByRemoteID("100")

	// Offline edit locally, while someone edits the same task on the server.
	tk.Content = "Local edit"
	must(t, st.SaveTask(tk))
	f.mu.Lock()
	f.items["100"].Content = "Server edit"
	f.bump(f.items["100"])
	f.commands = nil
	f.mu.Unlock()

	must(t, c.Sync(ctx))
	got, _ := st.Task(tk.ID)
	if got.Content != "Server edit" {
		t.Fatalf("server should win, got %q", got.Content)
	}
	cs, _ := st.Conflicts()
	if len(cs) != 1 || cs[0].EntityID != tk.ID {
		t.Fatalf("conflict not recorded: %+v", cs)
	}
	var kept model.Task
	json.Unmarshal([]byte(cs[0].LocalJSON), &kept)
	if kept.Content != "Local edit" {
		t.Fatalf("local version not kept: %q", kept.Content)
	}
	if len(f.commands) != 0 {
		t.Fatalf("conflicting edit must not be pushed: %+v", f.commands)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
