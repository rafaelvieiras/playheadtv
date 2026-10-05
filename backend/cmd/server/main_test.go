package main

import (
	"strings"
	"testing"
	"time"
)

func TestEPGWindowFromEnv(t *testing.T) {
	cases := []struct {
		in      string
		want    time.Duration
		warning bool
	}{
		{"", 48 * time.Hour, false},
		{"72h", 72 * time.Hour, false},
		{"168h", 168 * time.Hour, false},
		{"1h", time.Hour, false},
		{"90m", 90 * time.Minute, false},
		{"30m", 48 * time.Hour, true},      // below the minimum
		{"9000h", 48 * time.Hour, true},    // above the maximum
		{"-5h", 48 * time.Hour, true},      // non-positive
		{"two days", 48 * time.Hour, true}, // not a duration
	}
	for _, c := range cases {
		got, warning := epgWindowFromEnv(c.in)
		if got != c.want {
			t.Errorf("epgWindowFromEnv(%q) = %v, want %v", c.in, got, c.want)
		}
		if (warning != "") != c.warning {
			t.Errorf("epgWindowFromEnv(%q) warning = %q, want warning: %v", c.in, warning, c.warning)
		}
		if c.warning && !strings.Contains(warning, "PLAYHEADTV_EPG_WINDOW") {
			t.Errorf("warning %q does not name the variable", warning)
		}
	}
}
