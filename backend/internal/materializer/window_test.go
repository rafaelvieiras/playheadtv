package materializer

import (
	"context"
	"testing"
	"time"
)

// The window handed to New is the horizon of the materialized guide: a
// continuous channel is filled up to now+window, whatever its length, and
// Window() reports what clients are told.
func TestMaterializer_WindowSetsTheHorizon(t *testing.T) {
	for _, window := range []time.Duration{6 * time.Hour, 72 * time.Hour} {
		f := continuousSetup(t)
		m := New(f.db, window)
		if m.Window() != window {
			t.Fatalf("Window() = %v, want %v", m.Window(), window)
		}
		if err := m.Run(context.Background()); err != nil {
			t.Fatalf("Run: %v", err)
		}
		entries := queryEntries(t, f)
		if len(entries) == 0 {
			t.Fatalf("window %v: no entries materialized", window)
		}
		last := time.UnixMilli(entries[len(entries)-1].EndAt)
		horizon := time.Now().Add(window)
		// The last program ends at or past the horizon, but never more than
		// one program (1h here) beyond it.
		if last.Before(horizon.Add(-time.Minute)) || last.After(horizon.Add(time.Hour+time.Minute)) {
			t.Errorf("window %v: last entry ends %v, want within [%v, %v+1h]", window, last, horizon, horizon)
		}
	}
}
