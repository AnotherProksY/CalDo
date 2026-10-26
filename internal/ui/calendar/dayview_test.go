package calendar

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"caldo/internal/config"
	"caldo/internal/model"
	"caldo/internal/ui/theme"
)

func TestDayTimeline(t *testing.T) {
	day := time.Date(2026, 11, 2, 0, 0, 0, 0, time.Local)
	ev := func(title string, start, end time.Duration) model.Occurrence {
		return model.Occurrence{Event: &model.Event{Title: title, Location: "Room 1"}, Start: day.Add(start), End: day.Add(end)}
	}
	h := time.Hour
	src := fakeSource{
		{Event: &model.Event{Title: "Holiday", AllDay: true}, Start: day, End: day.AddDate(0, 0, 1)},
		ev("Standup", 9*h, 9*h+15*time.Minute),
		ev("Interview A", 12*h, 13*h),
		ev("Interview B", 12*h, 13*h),
		ev("Lunch", 13*h, 14*h),
	}
	m := New(src, theme.New(config.Default().Theme), Month, time.Monday, 14, "15:04")
	m.cursor = day
	m.now = func() time.Time { return day.Add(20 * h) }
	const w = 80
	lines, spans := m.timeline([]dayCol{{day, m.dayOccurrences(day), w - gutterW}}, "", true)

	var plain []string
	for _, l := range lines {
		p := ansi.Strip(l.text)
		if got := theme.StringWidth(p); got != w {
			t.Errorf("line %q is %d cells wide, want %d", p, got, w)
		}
		plain = append(plain, p)
	}
	screen := strings.Join(plain, "\n")
	t.Logf("\n%s", screen)

	rows := func(i int) int { return spans[i][1] - spans[i][0] }
	if rows(0) != 1 || !strings.HasPrefix(plain[0], "all-day ▌ Holiday") {
		t.Errorf("all-day row: %q", plain[0])
	}
	if rows(1) != 2 {
		t.Errorf("a 15-minute event takes the 30-minute minimum, got %d rows", rows(1))
	}
	if rows(4) != 4 {
		t.Errorf("a one-hour event takes 4 rows, got %d", rows(4))
	}
	// Free time between 09:30 and 12:00 collapses into one row.
	if r := spans[1][1]; !strings.Contains(plain[r], "⋯ 2h 30m free") || spans[2][0] != r+1 {
		t.Errorf("expected one gap row after Standup, got %q", plain[r])
	}
	// The two interviews overlap: same rows, side by side.
	if spans[2] != spans[3] {
		t.Errorf("overlapping events should share rows: %v vs %v", spans[2], spans[3])
	}
	title := plain[spans[2][0]+1]
	if a, b := strings.Index(title, "Interview A"), strings.Index(title, "Interview B"); a < 0 || b <= a {
		t.Errorf("expected the interviews side by side: %q", title)
	}
	if hits := lines[spans[2][0]].hits; len(hits) != 2 || hits[0].idx == hits[1].idx {
		t.Errorf("each column needs its own hit region: %+v", hits)
	}
	// Lunch starts right after: time, title, location.
	if got := plain[spans[4][0]+2]; !strings.Contains(got, "@ Room 1") {
		t.Errorf("a one-hour block shows its location: %q", got)
	}
	// Now (20:00) is after the last event: a marker row at the end.
	if last := plain[len(plain)-1]; !strings.HasPrefix(last, "  20:00▸") {
		t.Errorf("expected a now marker at the end, got %q", last)
	}
}

func TestTint(t *testing.T) {
	th := theme.New(config.Default().Theme)
	r, g, b, _ := th.Tint("#ff0000", 0.5).RGBA()
	if r>>8 != 127 || g != 0 || b != 0 {
		t.Errorf("half red over black = %d,%d,%d", r>>8, g>>8, b>>8)
	}
}
