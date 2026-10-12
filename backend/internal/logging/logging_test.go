package logging

import (
	"bytes"
	"log"
	"os"
	"strings"
	"testing"
)

func TestParseLevel(t *testing.T) {
	cases := []struct {
		in   string
		want Level
		ok   bool
	}{
		{"error", LevelError, true},
		{" WARN ", LevelWarn, true},
		{"Info", LevelInfo, true},
		{"debug", LevelDebug, true},
		{"TRACE", LevelTrace, true},
		{"", LevelInfo, false},
		{"verbose", LevelInfo, false},
		{"warning", LevelInfo, false},
	}
	for _, c := range cases {
		got, ok := ParseLevel(c.in)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("ParseLevel(%q) = %v, %v; want %v, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestLevelString(t *testing.T) {
	for i, n := range levelNames {
		if Level(i).String() != n {
			t.Errorf("Level(%d).String() = %q, want %q", i, Level(i).String(), n)
		}
	}
	if Level(9).String() != "unknown" {
		t.Error("an out-of-range level should print as unknown")
	}
}

// With no level set, messages keep going through the standard logger exactly
// as before, and debug is dropped.
func TestUnsetKeepsStandardLoggerOutput(t *testing.T) {
	Setup("", nil)
	var buf bytes.Buffer
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(os.Stderr)
		log.SetFlags(log.LstdFlags)
	})
	Errorf("e %d", 1)
	Warnf("w %d", 2)
	Infof("i %d", 3)
	Debugf("d %d", 4)
	want := "e 1\nw 2\ni 3\n"
	if buf.String() != want {
		t.Fatalf("output = %q, want %q", buf.String(), want)
	}
	if _, set := Configured(); set {
		t.Error("Configured() should be false when the variable is unset")
	}
}

func TestSetLevelFilters(t *testing.T) {
	t.Cleanup(func() { Setup("", nil) })
	for _, c := range []struct {
		level string
		want  []string
	}{
		{"error", []string{"e"}},
		{"warn", []string{"e", "w"}},
		{"info", []string{"e", "w", "i"}},
		{"debug", []string{"e", "w", "i", "d"}},
		{"trace", []string{"e", "w", "i", "d", "t"}},
	} {
		var buf bytes.Buffer
		if note := Setup(c.level, &buf); note != "" {
			t.Fatalf("Setup(%q) note = %q", c.level, note)
		}
		Errorf("e")
		Warnf("w")
		Infof("i")
		Debugf("d")
		Tracef("t")
		var got []string
		for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
			if line == "" {
				continue
			}
			i := strings.Index(line, "msg=")
			got = append(got, strings.Trim(line[i+4:], `"`))
		}
		if strings.Join(got, ",") != strings.Join(c.want, ",") {
			t.Errorf("level %s wrote %v, want %v (raw %q)", c.level, got, c.want, buf.String())
		}
		if !strings.Contains(buf.String(), "level=ERROR") {
			t.Errorf("level %s: slog output should carry the level, got %q", c.level, buf.String())
		}
	}
}

func TestInvalidValueIsReportedAndIgnored(t *testing.T) {
	t.Cleanup(func() { Setup("", nil) })
	note := Setup("loud", nil)
	if !strings.Contains(note, "loud") || !strings.Contains(note, EnvVar) {
		t.Errorf("note = %q, want it to name the variable and the value", note)
	}
	if _, set := Configured(); set {
		t.Error("an invalid value must leave the logger unconfigured")
	}
}

func TestEnabled(t *testing.T) {
	t.Cleanup(func() { Setup("", nil) })
	Setup("", nil)
	if !Enabled(LevelInfo) || Enabled(LevelDebug) {
		t.Error("unset: info is on, debug is off")
	}
	Setup("warn", nil)
	if !Enabled(LevelWarn) || Enabled(LevelInfo) {
		t.Error("warn: warn is on, info is off")
	}
}
