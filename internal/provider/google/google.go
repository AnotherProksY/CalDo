// Package google syncs events with Google Calendar.
//
// Pushes go first and use If-Match etags, so a 412 means the server changed the
// event meanwhile: the server version wins and ours is kept as a conflict.
// Pulls are incremental via per-calendar sync tokens; a 410 resets the token
// and triggers a full resync of that calendar.
package google

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	calendar "google.golang.org/api/calendar/v3"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"

	"caldo/internal/config"
	"caldo/internal/model"
	"caldo/internal/store"
)

const maxAttempts = 5

type Client struct {
	svc *calendar.Service
	st  *store.Store
	cfg config.GoogleConfig
	now func() time.Time
}

func New(ctx context.Context, st *store.Store, cfg config.GoogleConfig, opts ...option.ClientOption) (*Client, error) {
	svc, err := calendar.NewService(ctx, opts...)
	if err != nil {
		return nil, err
	}
	return &Client{svc: svc, st: st, cfg: cfg, now: time.Now}, nil
}

func (c *Client) Name() string { return "google" }

func (c *Client) Sync(ctx context.Context) error {
	if err := c.syncCalendars(ctx); err != nil {
		return err
	}
	pushErr := c.push(ctx)
	if pushErr != nil && isTransport(pushErr) {
		return pushErr
	}
	cals, err := c.st.Calendars()
	if err != nil {
		return err
	}
	var errs []error
	for _, cal := range cals {
		if cal.Provider != model.ProviderGoogle {
			continue
		}
		if err := c.pullCalendar(ctx, cal); err != nil {
			if isTransport(err) {
				return err
			}
			errs = append(errs, fmt.Errorf("%s: %w", cal.Name, err))
		}
	}
	return errors.Join(append(errs, pushErr)...)
}

func stateKey(calID string) string { return "google:sync:" + calID }

// --- calendars -----------------------------------------------------------

func (c *Client) syncCalendars(ctx context.Context) error {
	var items []*calendar.CalendarListEntry
	page := ""
	for {
		call := c.svc.CalendarList.List().Context(ctx)
		if page != "" {
			call.PageToken(page)
		}
		res, err := call.Do()
		if err != nil {
			return err
		}
		items = append(items, res.Items...)
		if page = res.NextPageToken; page == "" {
			break
		}
	}
	seen := map[string]bool{}
	for i, it := range items {
		name := it.SummaryOverride
		if name == "" {
			name = it.Summary
		}
		if !c.wanted(it.Id, name) || it.AccessRole == "freeBusyReader" || it.Deleted {
			continue
		}
		sort := i + 1
		if it.Primary {
			sort = 0
		}
		cal, err := c.st.UpsertRemoteCalendar(model.Calendar{
			Provider: model.ProviderGoogle, RemoteID: it.Id, Name: name, Color: it.BackgroundColor,
			Enabled: !it.Hidden, ReadOnly: it.AccessRole == "reader", Sort: sort,
		})
		if err != nil {
			return err
		}
		seen[cal.ID] = true
	}
	cals, err := c.st.Calendars()
	if err != nil {
		return err
	}
	for _, cal := range cals {
		if cal.Provider == model.ProviderGoogle && !seen[cal.ID] {
			if err := c.st.DeleteCalendar(cal.ID); err != nil {
				return err
			}
			if err := c.st.DeleteState(stateKey(cal.ID)); err != nil {
				return err
			}
		}
	}
	return nil
}

func (c *Client) wanted(id, name string) bool {
	if len(c.cfg.Calendars) == 0 {
		return true
	}
	return slices.ContainsFunc(c.cfg.Calendars, func(s string) bool { return s == id || strings.EqualFold(s, name) })
}

// --- pull ----------------------------------------------------------------

func (c *Client) pullCalendar(ctx context.Context, cal model.Calendar) error {
	token, err := c.st.GetState(stateKey(cal.ID))
	if err != nil {
		return err
	}
	full := token == ""
	var items []*calendar.Event
	page := ""
	for {
		call := c.svc.Events.List(cal.RemoteID).Context(ctx).ShowDeleted(true).MaxResults(2500)
		if full {
			call.TimeMin(c.now().AddDate(0, 0, -c.cfg.PastDays).Format(time.RFC3339))
		} else {
			call.SyncToken(token)
		}
		if page != "" {
			call.PageToken(page)
		}
		res, err := call.Do()
		if isStatus(err, http.StatusGone) {
			// Sync token expired: start over with a full sync.
			if err := c.st.DeleteState(stateKey(cal.ID)); err != nil {
				return err
			}
			return c.pullCalendar(ctx, cal)
		}
		if err != nil {
			return err
		}
		items = append(items, res.Items...)
		if res.NextPageToken != "" {
			page = res.NextPageToken
			continue
		}
		token = res.NextSyncToken
		break
	}

	// Masters before exceptions so exceptions can link to their master.
	slices.SortStableFunc(items, func(a, b *calendar.Event) int {
		ae, be := a.RecurringEventId != "", b.RecurringEventId != ""
		switch {
		case ae == be:
			return 0
		case ae:
			return 1
		}
		return -1
	})
	seen := map[string]bool{}
	for _, it := range items {
		seen[it.Id] = true
		if err := c.applyEvent(cal, it); err != nil {
			return fmt.Errorf("event %s: %w", it.Id, err)
		}
	}
	if full {
		if err := c.dropUnseen(cal, seen); err != nil {
			return err
		}
	}
	return c.st.SetState(stateKey(cal.ID), token)
}

func (c *Client) applyEvent(cal model.Calendar, it *calendar.Event) error {
	local, err := c.st.EventByRemoteID(cal.ID, it.Id)
	if err != nil {
		return err
	}
	if local != nil && c.st.HasPending(model.EntityEvent, local.ID) {
		if local.ETag == it.Etag {
			return nil // unchanged on the server; our edit will be pushed
		}
		if err := c.keepConflict(local); err != nil {
			return err
		}
	}

	if it.Status == model.StatusCancelled && it.RecurringEventId == "" {
		if local != nil {
			return c.st.PurgeEvent(local.ID)
		}
		return nil
	}
	e, ok, err := c.toModel(cal, it)
	if err != nil || !ok {
		return err
	}
	if local != nil {
		e.ID = local.ID
	}
	return c.st.PutRemoteEvent(e)
}

func (c *Client) toModel(cal model.Calendar, it *calendar.Event) (*model.Event, bool, error) {
	e := &model.Event{
		CalendarID:  cal.ID,
		RemoteID:    it.Id,
		ETag:        it.Etag,
		Title:       it.Summary,
		Description: it.Description,
		Location:    it.Location,
		Recurrence:  it.Recurrence,
		Status:      it.Status,
	}
	if it.RecurringEventId != "" {
		master, err := c.st.EventByRemoteID(cal.ID, it.RecurringEventId)
		if err != nil {
			return nil, false, err
		}
		if master == nil {
			// Exception of a series outside our window; nothing to attach it to.
			return nil, false, nil
		}
		e.RecurringEventID = master.ID
		if it.OriginalStartTime != nil {
			orig, _, err := parseDT(it.OriginalStartTime)
			if err != nil {
				return nil, false, err
			}
			e.OriginalStart = &orig
		}
		e.AllDay = master.AllDay
	}
	if it.Start == nil || it.End == nil {
		if e.Status == model.StatusCancelled && e.OriginalStart != nil {
			// Cancelled instances often carry only originalStartTime.
			e.Start, e.End = *e.OriginalStart, *e.OriginalStart
			return e, true, nil
		}
		return nil, false, nil
	}
	start, allDay, err := parseDT(it.Start)
	if err != nil {
		return nil, false, err
	}
	end, _, err := parseDT(it.End)
	if err != nil {
		return nil, false, err
	}
	e.Start, e.End, e.AllDay = start, end, allDay
	e.Timezone = it.Start.TimeZone
	if u, err := time.Parse(time.RFC3339, it.Updated); err == nil {
		e.UpdatedAt = u
	}
	return e, true, nil
}

func parseDT(d *calendar.EventDateTime) (time.Time, bool, error) {
	if d.Date != "" {
		t, err := time.ParseInLocation("2006-01-02", d.Date, time.Local)
		return t, true, err
	}
	t, err := time.Parse(time.RFC3339, d.DateTime)
	return t.Local(), false, err
}

func (c *Client) keepConflict(local *model.Event) error {
	b, _ := json.Marshal(local)
	if err := c.st.AddConflict(model.Conflict{EntityType: model.EntityEvent, EntityID: local.ID, Title: local.Title, LocalJSON: string(b)}); err != nil {
		return err
	}
	return c.st.DropPending(model.EntityEvent, local.ID)
}

// dropUnseen removes synced events that a full sync no longer returns.
func (c *Client) dropUnseen(cal model.Calendar, seen map[string]bool) error {
	evs, err := c.st.EventsInCalendar(cal.ID)
	if err != nil {
		return err
	}
	for _, e := range evs {
		if e.RemoteID == "" || seen[e.RemoteID] || e.Dirty {
			continue
		}
		// Old events before the window are simply not returned; keep them.
		if e.End.Before(c.now().AddDate(0, 0, -c.cfg.PastDays)) && !e.IsRecurring() {
			continue
		}
		if err := c.st.PurgeEvent(e.ID); err != nil {
			return err
		}
	}
	return nil
}

// --- push ----------------------------------------------------------------

func (c *Client) push(ctx context.Context) error {
	entries, err := c.st.Pending(model.ProviderGoogle)
	if err != nil {
		return err
	}
	var errs []error
	for _, en := range entries {
		err := c.pushOne(ctx, en)
		if err == nil {
			continue
		}
		if isTransport(err) {
			return err
		}
		errs = append(errs, err)
		if ferr := c.st.FailOutbox(en.ID, err); ferr != nil {
			return ferr
		}
		if en.Attempts+1 >= maxAttempts {
			// Give up but keep the local version restorable.
			if err := c.st.AckOutbox(en.ID); err != nil {
				return err
			}
			if e, gerr := c.st.Event(en.EntityID); gerr == nil {
				if err := c.keepConflict(e); err != nil {
					return err
				}
			}
		}
	}
	return errors.Join(errs...)
}

func (c *Client) pushOne(ctx context.Context, en model.OutboxEntry) error {
	e, err := c.st.Event(en.EntityID)
	if err != nil {
		// Entity vanished locally (e.g. purged by a pull); nothing to push.
		return c.st.AckOutbox(en.ID)
	}
	cal, err := c.st.Calendar(e.CalendarID)
	if err != nil {
		return c.st.AckOutbox(en.ID)
	}

	if en.Op == model.OpDelete || e.Deleted {
		if e.RemoteID != "" {
			err := c.svc.Events.Delete(cal.RemoteID, e.RemoteID).Context(ctx).Do()
			if err != nil && !isStatus(err, http.StatusGone) && !isStatus(err, http.StatusNotFound) {
				return err
			}
		}
		if err := c.st.AckOutbox(en.ID); err != nil {
			return err
		}
		return c.st.PurgeEvent(e.ID)
	}

	ge, err := c.toGoogle(e)
	if err != nil {
		return err
	}
	var res *calendar.Event
	switch {
	case e.RemoteID == "" && e.IsException():
		res, err = c.patchInstance(ctx, cal, e, ge)
	case e.RemoteID == "":
		res, err = c.svc.Events.Insert(cal.RemoteID, ge).Context(ctx).Do()
	default:
		call := c.svc.Events.Patch(cal.RemoteID, e.RemoteID, ge).Context(ctx)
		if e.ETag != "" {
			call.Header().Set("If-Match", e.ETag)
		}
		res, err = call.Do()
		if isStatus(err, http.StatusPreconditionFailed) {
			return c.resolvePushConflict(ctx, cal, e, en)
		}
	}
	if err != nil {
		return err
	}
	if err := c.st.AckOutbox(en.ID); err != nil {
		return err
	}
	return c.st.MarkEventPushed(e.ID, res.Id, res.Etag)
}

// patchInstance materialises a local exception by patching the matching
// instance of the server-side series.
func (c *Client) patchInstance(ctx context.Context, cal model.Calendar, e *model.Event, ge *calendar.Event) (*calendar.Event, error) {
	master, err := c.st.Event(e.RecurringEventID)
	if err != nil {
		return nil, fmt.Errorf("master of exception: %w", err)
	}
	if master.RemoteID == "" {
		return nil, errors.New("series not pushed yet")
	}
	call := c.svc.Events.Instances(cal.RemoteID, master.RemoteID).Context(ctx).ShowDeleted(true)
	if e.OriginalStart != nil {
		if master.AllDay {
			call.OriginalStart(e.OriginalStart.Format("2006-01-02"))
		} else {
			call.OriginalStart(e.OriginalStart.Format(time.RFC3339))
		}
	}
	res, err := call.Do()
	if err != nil {
		return nil, err
	}
	if len(res.Items) == 0 {
		return nil, errors.New("recurring instance not found on server")
	}
	return c.svc.Events.Patch(cal.RemoteID, res.Items[0].Id, ge).Context(ctx).Do()
}

func (c *Client) resolvePushConflict(ctx context.Context, cal model.Calendar, e *model.Event, en model.OutboxEntry) error {
	if err := c.keepConflict(e); err != nil {
		return err
	}
	if err := c.st.AckOutbox(en.ID); err != nil {
		return err
	}
	srv, err := c.svc.Events.Get(cal.RemoteID, e.RemoteID).Context(ctx).Do()
	if err != nil {
		if isStatus(err, http.StatusNotFound) || isStatus(err, http.StatusGone) {
			return c.st.PurgeEvent(e.ID)
		}
		return err
	}
	return c.applyEvent(cal, srv)
}

func (c *Client) toGoogle(e *model.Event) (*calendar.Event, error) {
	ge := &calendar.Event{
		Summary:         e.Title,
		Description:     e.Description,
		Location:        e.Location,
		Status:          e.Status,
		Recurrence:      e.Recurrence,
		ForceSendFields: []string{"Summary", "Description", "Location", "Recurrence"},
	}
	if e.IsException() {
		// Instances cannot carry their own recurrence.
		ge.Recurrence, ge.ForceSendFields = nil, ge.ForceSendFields[:3]
	} else if ge.Recurrence == nil {
		ge.Recurrence = []string{}
	}
	if e.Status == model.StatusCancelled {
		return ge, nil
	}
	if e.AllDay {
		ge.Start = &calendar.EventDateTime{Date: e.Start.Format("2006-01-02"), NullFields: []string{"DateTime"}}
		ge.End = &calendar.EventDateTime{Date: e.End.Format("2006-01-02"), NullFields: []string{"DateTime"}}
		return ge, nil
	}
	tz := e.Timezone
	if tz == "" {
		tz = LocalZone()
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		loc = time.Local
	}
	ge.Start = &calendar.EventDateTime{DateTime: e.Start.In(loc).Format(time.RFC3339), TimeZone: tz, NullFields: []string{"Date"}}
	ge.End = &calendar.EventDateTime{DateTime: e.End.In(loc).Format(time.RFC3339), TimeZone: tz, NullFields: []string{"Date"}}
	return ge, nil
}

// LocalZone returns the IANA name of the local zone (Google requires one for recurring events).
func LocalZone() string {
	if tz := os.Getenv("TZ"); tz != "" {
		return strings.TrimPrefix(tz, ":")
	}
	if p, err := filepath.EvalSymlinks("/etc/localtime"); err == nil {
		if i := strings.Index(p, "zoneinfo/"); i >= 0 {
			return p[i+len("zoneinfo/"):]
		}
	}
	if n := time.Local.String(); n != "Local" {
		return n
	}
	return "UTC"
}

func isStatus(err error, code int) bool {
	var ge *googleapi.Error
	return errors.As(err, &ge) && ge.Code == code
}

// isTransport reports network-level failures (no HTTP response).
func isTransport(err error) bool {
	if err == nil {
		return false
	}
	var ge *googleapi.Error
	return !errors.As(err, &ge)
}
