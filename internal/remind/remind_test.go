package remind

import (
	"testing"
	"time"
)

func TestParseFormat(t *testing.T) {
	cases := []struct {
		in     string
		allDay bool
		want   time.Duration
		out    string
	}{
		{"10m", false, -10 * time.Minute, "10m"},
		{"1h30m", false, -90 * time.Minute, "1h30m"},
		{"1d", false, -24 * time.Hour, "1d"},
		{"0m", false, 0, "0m"},
		{"09:00", true, 9 * time.Hour, "09:00"},
		{"-1d 18:00", true, -6 * time.Hour, "-1d 18:00"},
		{"-2d 09:30", true, -38*time.Hour - 30*time.Minute, "-2d 09:30"},
	}
	for _, c := range cases {
		got, err := Parse(c.in, c.allDay)
		if err != nil || got != c.want {
			t.Errorf("Parse(%q, %v) = %v, %v; want %v", c.in, c.allDay, got, err, c.want)
			continue
		}
		if s := Format(got, c.allDay); s != c.out {
			t.Errorf("Format(%v, %v) = %q, want %q", got, c.allDay, s, c.out)
		}
	}
	for _, bad := range []struct {
		in     string
		allDay bool
	}{{"", false}, {"10", false}, {"5w", false}, {"10m", true}, {"25:00", true}, {"09:00", false}} {
		if _, err := Parse(bad.in, bad.allDay); err == nil {
			t.Errorf("Parse(%q, %v) should fail", bad.in, bad.allDay)
		}
	}
}
