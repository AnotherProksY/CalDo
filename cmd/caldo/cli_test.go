package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"caldo/internal/model"
	"caldo/internal/store"
)

func capture(t *testing.T, fn func() error) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	runErr := fn()
	os.Stdout = old
	w.Close()
	b, _ := io.ReadAll(r)
	if runErr != nil {
		t.Fatal(runErr)
	}
	return string(b)
}

func TestWaybar(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Now().Truncate(time.Minute)
	if err := st.SaveEvent(&model.Event{CalendarID: store.LocalCalendarID, Title: "Review <Q4> & plan",
		Start: now.Add(10 * time.Minute), End: now.Add(70 * time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveTask(&model.Task{ProjectID: store.LocalInboxID, Content: "Old thing",
		Due: &model.Due{Date: now.AddDate(0, 0, -3).Format("2006-01-02")}}); err != nil {
		t.Fatal(err)
	}

	var out waybarOut
	if err := json.Unmarshal([]byte(capture(t, func() error { return runWaybar(nil, st) })), &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.Text, "Review <Q4> & plan · in 10m") || !strings.HasSuffix(out.Text, "· ☐ 1") {
		t.Fatalf("text: %q", out.Text)
	}
	if strings.Join(out.Class, ",") != "soon,overdue" {
		t.Fatalf("class: %v", out.Class)
	}
	if !strings.Contains(out.Tooltip, "Review &lt;Q4&gt; &amp; plan") || !strings.Contains(out.Tooltip, "<b>Overdue</b>") {
		t.Fatalf("tooltip must be Pango-escaped:\n%s", out.Tooltip)
	}
}

func TestQuickTextStdin(t *testing.T) {
	if s, err := quickText([]string{"-"}, strings.NewReader("Buy milk p1\nignored\n")); err != nil || s != "Buy milk p1" {
		t.Fatalf("got %q, %v", s, err)
	}
	if _, err := quickText(nil, strings.NewReader("")); err == nil {
		t.Fatal("empty input should fail")
	}
}
