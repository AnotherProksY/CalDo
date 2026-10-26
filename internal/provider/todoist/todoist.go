// Package todoist syncs tasks with Todoist through the v1 Sync API.
//
// A cycle is pull → push → pull. The first pull detects conflicts (the server
// changed a task we also changed offline: the server wins and our version is
// kept in the conflicts table); the push sends queued commands, whose uuid is
// the outbox entry id so retries are idempotent; the last pull picks up ids
// and versions for what we just pushed.
package todoist

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"caldo/internal/model"
	"caldo/internal/store"
)

const (
	stateToken  = "todoist:sync_token"
	maxCommands = 100
	maxAttempts = 5
)

type Client struct {
	base  string
	token string
	http  *http.Client
	st    *store.Store
}

func New(baseURL, token string, hc *http.Client, st *store.Store) *Client {
	if hc == nil {
		hc = http.DefaultClient
	}
	return &Client{base: strings.TrimRight(baseURL, "/"), token: token, http: hc, st: st}
}

func (c *Client) Name() string { return "todoist" }

// --- wire types ----------------------------------------------------------

type wireDue struct {
	Date        string `json:"date"`
	String      string `json:"string"`
	IsRecurring bool   `json:"is_recurring"`
	Timezone    string `json:"timezone"`
}

type wireDeadline struct {
	Date string `json:"date"`
}

type wireItem struct {
	ID          string        `json:"id"`
	ProjectID   string        `json:"project_id"`
	SectionID   string        `json:"section_id"`
	ParentID    string        `json:"parent_id"`
	Content     string        `json:"content"`
	Description string        `json:"description"`
	Priority    int           `json:"priority"`
	Labels      []string      `json:"labels"`
	Due         *wireDue      `json:"due"`
	Deadline    *wireDeadline `json:"deadline"`
	Checked     bool          `json:"checked"`
	IsDeleted   bool          `json:"is_deleted"`
	ChildOrder  int           `json:"child_order"`
	UpdatedAt   string        `json:"updated_at"`
}

type wireProject struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Color        string `json:"color"`
	ParentID     string `json:"parent_id"`
	ChildOrder   int    `json:"child_order"`
	InboxProject bool   `json:"inbox_project"`
	IsDeleted    bool   `json:"is_deleted"`
	IsArchived   bool   `json:"is_archived"`
}

type wireSection struct {
	ID           string `json:"id"`
	ProjectID    string `json:"project_id"`
	Name         string `json:"name"`
	SectionOrder int    `json:"section_order"`
	IsDeleted    bool   `json:"is_deleted"`
	IsArchived   bool   `json:"is_archived"`
}

type wireLabel struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Color     string `json:"color"`
	ItemOrder int    `json:"item_order"`
	IsDeleted bool   `json:"is_deleted"`
}

type command struct {
	Type   string         `json:"type"`
	UUID   string         `json:"uuid"`
	TempID string         `json:"temp_id,omitempty"`
	Args   map[string]any `json:"args"`
}

type syncResponse struct {
	SyncToken     string                     `json:"sync_token"`
	FullSync      bool                       `json:"full_sync"`
	Items         []wireItem                 `json:"items"`
	Projects      []wireProject              `json:"projects"`
	Sections      []wireSection              `json:"sections"`
	Labels        []wireLabel                `json:"labels"`
	SyncStatus    map[string]json.RawMessage `json:"sync_status"`
	TempIDMapping map[string]string          `json:"temp_id_mapping"`
}

func (c *Client) call(ctx context.Context, token string, cmds []command) (*syncResponse, error) {
	form := url.Values{}
	if cmds != nil {
		b, _ := json.Marshal(cmds)
		form.Set("commands", string(b))
	}
	if token != "" {
		form.Set("sync_token", token)
		form.Set("resource_types", `["projects","sections","labels","items"]`)
	}
	body, err := c.post(ctx, form)
	if err != nil {
		return nil, err
	}
	var out syncResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("todoist: decode response: %w", err)
	}
	return &out, nil
}

// User fetches the account's name, which also checks that the token works.
func (c *Client) User(ctx context.Context) (string, error) {
	form := url.Values{}
	form.Set("sync_token", "*")
	form.Set("resource_types", `["user"]`)
	body, err := c.post(ctx, form)
	if err != nil {
		return "", err
	}
	var out struct {
		User struct {
			FullName string `json:"full_name"`
			Email    string `json:"email"`
		} `json:"user"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("todoist: decode response: %w", err)
	}
	if out.User.FullName != "" {
		return out.User.FullName, nil
	}
	return out.User.Email, nil
}

func (c *Client) post(ctx context.Context, form url.Values) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/sync", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &HTTPError{Status: resp.StatusCode, Body: string(bytes.TrimSpace(body))}
	}
	return body, nil
}

// Rejected reports whether err means the token is invalid.
func Rejected(err error) bool {
	var he *HTTPError
	return errors.As(err, &he) && (he.Status == http.StatusUnauthorized || he.Status == http.StatusForbidden)
}

type HTTPError struct {
	Status int
	Body   string
}

func (e *HTTPError) Error() string {
	if e.Status == http.StatusUnauthorized || e.Status == http.StatusForbidden {
		return "todoist: token rejected — run `caldo auth todoist`"
	}
	return fmt.Sprintf("todoist: HTTP %d: %s", e.Status, e.Body)
}

// --- cycle ---------------------------------------------------------------

func (c *Client) Sync(ctx context.Context) error {
	if err := c.pull(ctx); err != nil {
		return err
	}
	pushed, err := c.push(ctx)
	if err != nil {
		return err
	}
	if pushed {
		return c.pull(ctx)
	}
	return nil
}

func (c *Client) pull(ctx context.Context) error {
	token, err := c.st.GetState(stateToken)
	if err != nil {
		return err
	}
	if token == "" {
		token = "*"
	}
	resp, err := c.call(ctx, token, nil)
	if err != nil {
		return err
	}
	if err := c.apply(resp); err != nil {
		return err
	}
	return c.st.SetState(stateToken, resp.SyncToken)
}

func (c *Client) apply(r *syncResponse) error {
	for _, p := range r.Projects {
		local, ok, err := c.st.ProjectByRemoteID(model.ProviderTodoist, p.ID)
		if err != nil {
			return err
		}
		if p.IsDeleted || p.IsArchived {
			if ok {
				if err := c.st.DeleteProject(local.ID); err != nil {
					return err
				}
			}
			continue
		}
		if _, err := c.st.PutRemoteProject(model.Project{
			Provider: model.ProviderTodoist, RemoteID: p.ID, Name: p.Name, Color: Color(p.Color),
			ParentID: p.ParentID, Sort: p.ChildOrder, IsInbox: p.InboxProject,
		}); err != nil {
			return err
		}
	}
	for _, s := range r.Sections {
		if s.IsDeleted || s.IsArchived {
			if err := c.st.DeleteSectionByRemoteID(s.ID); err != nil {
				return err
			}
			continue
		}
		p, ok, err := c.st.ProjectByRemoteID(model.ProviderTodoist, s.ProjectID)
		if err != nil || !ok {
			continue
		}
		if _, err := c.st.PutRemoteSection(model.Section{ProjectID: p.ID, RemoteID: s.ID, Name: s.Name, Sort: s.SectionOrder}); err != nil {
			return err
		}
	}
	for _, l := range r.Labels {
		if l.IsDeleted {
			if err := c.st.DeleteLabelByRemoteID(l.ID); err != nil {
				return err
			}
			continue
		}
		if err := c.st.PutRemoteLabel(model.Label{RemoteID: l.ID, Name: l.Name, Color: Color(l.Color), Sort: l.ItemOrder}); err != nil {
			return err
		}
	}

	seen := map[string]bool{}
	var unresolved []*model.Task
	parents := map[string]string{} // local task id -> remote parent id
	for _, it := range r.Items {
		seen[it.ID] = true
		t, err := c.applyItem(it)
		if err != nil {
			return err
		}
		if t != nil && it.ParentID != "" && t.ParentID == "" {
			unresolved = append(unresolved, t)
			parents[t.ID] = it.ParentID
		}
	}
	// Parents may arrive after their children in one response.
	for _, t := range unresolved {
		if pid := c.st.TaskIDByRemote(parents[t.ID]); pid != "" {
			t.ParentID = pid
			if err := c.st.PutRemoteTask(t); err != nil {
				return err
			}
		}
	}
	if r.FullSync {
		return c.dropUnseen(seen)
	}
	return nil
}

// applyItem writes one server item, resolving conflicts in the server's favour.
// It returns the stored task, or nil when nothing was stored.
func (c *Client) applyItem(it wireItem) (*model.Task, error) {
	local, err := c.st.TaskByRemoteID(it.ID)
	if err != nil {
		return nil, err
	}
	pending := local != nil && c.st.HasPending(model.EntityTask, local.ID)
	if pending && local.RemoteVersion == it.UpdatedAt && !it.IsDeleted {
		// Server unchanged since our edit: keep ours, it will be pushed.
		return nil, nil
	}
	if pending {
		if err := c.keepConflict(local); err != nil {
			return nil, err
		}
	}
	if it.IsDeleted {
		if local != nil {
			return nil, c.st.PurgeTask(local.ID)
		}
		return nil, nil
	}
	p, ok, err := c.st.ProjectByRemoteID(model.ProviderTodoist, it.ProjectID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	t := &model.Task{
		ProjectID:     p.ID,
		RemoteID:      it.ID,
		Content:       it.Content,
		Description:   it.Description,
		Priority:      model.Priority(it.Priority),
		Labels:        it.Labels,
		Checked:       it.Checked,
		Sort:          it.ChildOrder,
		RemoteVersion: it.UpdatedAt,
	}
	if local != nil {
		t.ID = local.ID
		t.CompletedAt = local.CompletedAt
	}
	if it.SectionID != "" {
		t.SectionID = c.st.SectionIDByRemote(it.SectionID)
	}
	if it.ParentID != "" {
		t.ParentID = c.st.TaskIDByRemote(it.ParentID)
	}
	if it.Due != nil {
		t.Due = &model.Due{Date: it.Due.Date, String: it.Due.String, IsRecurring: it.Due.IsRecurring, Timezone: it.Due.Timezone}
	}
	if it.Deadline != nil {
		t.Deadline = it.Deadline.Date
	}
	return t, c.st.PutRemoteTask(t)
}

func (c *Client) keepConflict(local *model.Task) error {
	b, _ := json.Marshal(local)
	if err := c.st.AddConflict(model.Conflict{EntityType: model.EntityTask, EntityID: local.ID, Title: local.Content, LocalJSON: string(b)}); err != nil {
		return err
	}
	return c.st.DropPending(model.EntityTask, local.ID)
}

// dropUnseen removes synced local tasks missing from a full sync (deleted or
// completed elsewhere while our token was invalid).
func (c *Client) dropUnseen(seen map[string]bool) error {
	ts, err := c.st.Tasks(true)
	if err != nil {
		return err
	}
	for _, t := range ts {
		if t.RemoteID == "" || seen[t.RemoteID] || t.Dirty {
			continue
		}
		p, err := c.st.Project(t.ProjectID)
		if err != nil || p.Provider != model.ProviderTodoist {
			continue
		}
		if err := c.st.PurgeTask(t.ID); err != nil {
			return err
		}
	}
	return nil
}

// --- push ----------------------------------------------------------------

func (c *Client) push(ctx context.Context) (bool, error) {
	entries, err := c.st.Pending(model.ProviderTodoist)
	if err != nil || len(entries) == 0 {
		return false, err
	}
	for start := 0; start < len(entries); start += maxCommands {
		batch := entries[start:min(start+maxCommands, len(entries))]
		cmds := make([]command, 0, len(batch))
		byUUID := map[string]model.OutboxEntry{}
		for _, e := range batch {
			cmd, ok, err := c.buildCommand(e)
			if err != nil {
				return false, err
			}
			if !ok {
				if err := c.st.AckOutbox(e.ID); err != nil {
					return false, err
				}
				continue
			}
			cmds = append(cmds, cmd)
			byUUID[e.ID] = e
		}
		if len(cmds) == 0 {
			continue
		}
		resp, err := c.call(ctx, "", cmds)
		if err != nil {
			return false, err
		}
		for tempID, remoteID := range resp.TempIDMapping {
			if err := c.st.MarkTaskPushed(tempID, remoteID); err != nil {
				return false, err
			}
		}
		for uuid, raw := range resp.SyncStatus {
			e, ok := byUUID[uuid]
			if !ok {
				continue
			}
			if string(raw) == `"ok"` {
				if err := c.ackCommand(e); err != nil {
					return false, err
				}
				continue
			}
			if err := c.failCommand(e, fmt.Errorf("todoist rejected %s: %s", e.Op, raw)); err != nil {
				return false, err
			}
		}
	}
	return true, nil
}

func (c *Client) ackCommand(e model.OutboxEntry) error {
	if err := c.st.AckOutbox(e.ID); err != nil {
		return err
	}
	if e.Op == model.OpDelete {
		return c.st.PurgeTask(e.EntityID)
	}
	if t, err := c.st.Task(e.EntityID); err == nil {
		return c.st.MarkTaskPushed(t.ID, t.RemoteID)
	}
	return nil
}

// failCommand records a rejected command; after repeated failures it gives up and
// keeps the local version as a conflict so nothing is silently lost.
func (c *Client) failCommand(e model.OutboxEntry, cause error) error {
	if err := c.st.FailOutbox(e.ID, cause); err != nil {
		return err
	}
	if e.Attempts+1 < maxAttempts {
		return nil
	}
	if err := c.st.AckOutbox(e.ID); err != nil {
		return err
	}
	t, err := c.st.Task(e.EntityID)
	if err != nil {
		return nil
	}
	b, _ := json.Marshal(t)
	return c.st.AddConflict(model.Conflict{EntityType: model.EntityTask, EntityID: t.ID,
		Title: t.Content + " (" + cause.Error() + ")", LocalJSON: string(b)})
}

// buildCommand turns an outbox entry into a Sync API command from the task's current state.
// ok=false means the entry is obsolete and can be dropped.
func (c *Client) buildCommand(e model.OutboxEntry) (command, bool, error) {
	t, err := c.st.Task(e.EntityID)
	if err != nil {
		return command{}, false, nil
	}
	id := t.RemoteID
	if id == "" {
		id = t.ID // temp id from a create in this batch
	}
	cmd := command{UUID: e.ID}
	switch e.Op {
	case model.OpCreate:
		p, err := c.st.Project(t.ProjectID)
		if err != nil {
			return cmd, false, nil
		}
		args := c.taskArgs(t)
		args["project_id"] = p.RemoteID
		if sec := c.sectionRemote(t.SectionID); sec != "" {
			args["section_id"] = sec
		}
		if t.ParentID != "" {
			args["parent_id"] = c.taskRemoteOrTemp(t.ParentID)
		}
		cmd.Type, cmd.TempID, cmd.Args = "item_add", t.ID, args
	case model.OpUpdate:
		args := c.taskArgs(t)
		args["id"] = id
		cmd.Type, cmd.Args = "item_update", args
	case model.OpMove:
		args := map[string]any{"id": id}
		switch {
		case t.ParentID != "":
			args["parent_id"] = c.taskRemoteOrTemp(t.ParentID)
		case t.SectionID != "":
			args["section_id"] = c.sectionRemote(t.SectionID)
		default:
			p, err := c.st.Project(t.ProjectID)
			if err != nil {
				return cmd, false, nil
			}
			args["project_id"] = p.RemoteID
		}
		cmd.Type, cmd.Args = "item_move", args
	case model.OpComplete:
		cmd.Type, cmd.Args = "item_close", map[string]any{"id": id}
	case model.OpUncomplete:
		cmd.Type, cmd.Args = "item_uncomplete", map[string]any{"id": id}
	case model.OpDelete:
		if t.RemoteID == "" {
			return cmd, false, nil
		}
		cmd.Type, cmd.Args = "item_delete", map[string]any{"id": id}
	default:
		return cmd, false, nil
	}
	return cmd, true, nil
}

func (c *Client) taskArgs(t *model.Task) map[string]any {
	labels := t.Labels
	if labels == nil {
		labels = []string{}
	}
	args := map[string]any{
		"content":     t.Content,
		"description": t.Description,
		"priority":    int(t.Priority),
		"labels":      labels,
		"due":         nil,
		"deadline":    nil,
	}
	if d := t.Due; d != nil {
		switch {
		case d.IsRecurring && d.String != "":
			args["due"] = map[string]any{"string": d.String}
		case d.Date != "":
			args["due"] = map[string]any{"date": d.Date}
		case d.String != "":
			args["due"] = map[string]any{"string": d.String}
		}
	}
	if t.Deadline != "" {
		args["deadline"] = map[string]any{"date": t.Deadline}
	}
	return args
}

func (c *Client) sectionRemote(localID string) string {
	if localID == "" {
		return ""
	}
	secs, _ := c.st.Sections()
	for _, s := range secs {
		if s.ID == localID {
			return s.RemoteID
		}
	}
	return ""
}

func (c *Client) taskRemoteOrTemp(localID string) string {
	if t, err := c.st.Task(localID); err == nil && t.RemoteID != "" {
		return t.RemoteID
	}
	return localID
}

// Color maps Todoist color names to hex.
func Color(name string) string {
	if c, ok := colors[name]; ok {
		return c
	}
	return "#808080"
}

var colors = map[string]string{
	"berry_red": "#b8256f", "red": "#db4035", "orange": "#ff9933", "yellow": "#fad000",
	"olive_green": "#afb83b", "lime_green": "#7ecc49", "green": "#299438", "mint_green": "#6accbc",
	"teal": "#158fad", "sky_blue": "#14aaf5", "light_blue": "#96c3eb", "blue": "#4073ff",
	"grape": "#884dff", "violet": "#af38eb", "lavender": "#eb96eb", "magenta": "#e05194",
	"salmon": "#ff8d85", "charcoal": "#808080", "grey": "#b8b8b8", "taupe": "#ccac93",
}
