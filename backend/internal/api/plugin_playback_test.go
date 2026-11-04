package api

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// The player is not on the machine the plugin runs on, so the loopback address a
// plugin hands out has to become the address the client used — while the port,
// the path and the token stay the plugin's own.
func TestReachableURLRewritesOnlyTheHost(t *testing.T) {
	cases := []struct {
		name   string
		url    string
		host   string
		expect string
	}{
		{
			name:   "loopback becomes the address the client used",
			url:    "http://127.0.0.1:41234/s/tok/index.m3u8?token=abc",
			host:   "192.0.2.10:9394",
			expect: "http://192.0.2.10:41234/s/tok/index.m3u8?token=abc",
		},
		{
			name:   "localhost too",
			url:    "http://localhost:8000/s/seg00001.ts",
			host:   "telly.lan",
			expect: "http://telly.lan:8000/s/seg00001.ts",
		},
		{
			// What a listener bound to every interface reports as its own address.
			name:   "an unspecified host is this machine too",
			url:    "http://[::]:42597/s/index.m3u8",
			host:   "192.0.2.10:9394",
			expect: "http://192.0.2.10:42597/s/index.m3u8",
		},
		{
			name:   "asked from loopback: leave it alone",
			url:    "http://127.0.0.1:41234/s/index.m3u8",
			host:   "127.0.0.1:9394",
			expect: "http://127.0.0.1:41234/s/index.m3u8",
		},
		{
			name:   "a url that already names a host is the plugin's business",
			url:    "https://cdn.example.test/v.mp4?x=1",
			host:   "192.0.2.10:9394",
			expect: "https://cdn.example.test/v.mp4?x=1",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "http://example.test/catalog-items/x/play", nil)
			r.Host = c.host
			got, err := reachableURL(c.url, r, "")
			if err != nil {
				t.Fatalf("reachableURL: %v", err)
			}
			if got != c.expect {
				t.Errorf("reachableURL(%q, host %q) = %q, want %q", c.url, c.host, got, c.expect)
			}
		})
	}
}

// A client behind a proxy cannot reach the plugin's ephemeral port on the proxy,
// so the host to advertise is configurable.
func TestReachableURLUsesTheConfiguredStreamHost(t *testing.T) {
	r := httptest.NewRequest("GET", "http://example.test/x", nil)
	r.Host = "tv.example.test"
	got, err := reachableURL("http://127.0.0.1:41234/s/index.m3u8", r, "192.168.1.10")
	if err != nil {
		t.Fatalf("reachableURL: %v", err)
	}
	if got != "http://192.168.1.10:41234/s/index.m3u8" {
		t.Errorf("got %q, want the configured host with the plugin's own port", got)
	}
	// A url that already names somewhere else is still the plugin's business.
	got, err = reachableURL("https://cdn.example.test/v.mp4", r, "192.168.1.10")
	if err != nil || got != "https://cdn.example.test/v.mp4" {
		t.Errorf("a remote url must not be rewritten: %q, %v", got, err)
	}
}

func TestReachableURLRejectsNonsense(t *testing.T) {
	r := httptest.NewRequest("GET", "http://example.test/x", nil)
	if _, err := reachableURL("not a url", r, ""); err == nil {
		t.Fatal("an unreadable url must be an error, not a link the player cannot open")
	}
}

func TestParseStartMs(t *testing.T) {
	for _, raw := range []string{"", "0", "600000"} {
		if _, err := parseStartMs(raw); err != nil {
			t.Errorf("parseStartMs(%q) = %v, want no error", raw, err)
		}
	}
	for _, raw := range []string{"-1", "abc", "1.5"} {
		if _, err := parseStartMs(raw); err == nil {
			t.Errorf("parseStartMs(%q) should fail", raw)
		}
	}
}

// Absent, `modes` keeps the answer every client has always received: a remux.
// Present, the list is the client's preference and travels in its order, because
// a plugin answers with the first mode it can serve.
func TestPlayModes(t *testing.T) {
	cases := []struct {
		name   string
		raw    string
		expect []string
		bad    bool
	}{
		{name: "absent is remux", raw: "", expect: []string{"remux"}},
		{name: "blank is absent", raw: "  ", expect: []string{"remux"}},
		{name: "order is the client's preference", raw: "pair,remux", expect: []string{"pair", "remux"}},
		{name: "single first", raw: "single, pair ,remux", expect: []string{"single", "pair", "remux"}},
		{name: "a trailing comma is not a mode", raw: "remux,", expect: []string{"remux"}},
		{name: "an unknown mode is refused, not ignored", raw: "remux,transcode", bad: true},
		{name: "nothing named is refused", raw: ",", bad: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := playModes(c.raw)
			if c.bad {
				if err == nil {
					t.Fatalf("playModes(%q) = %v, want an error", c.raw, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("playModes(%q): %v", c.raw, err)
			}
			if strings.Join(got, ",") != strings.Join(c.expect, ",") {
				t.Errorf("playModes(%q) = %v, want %v", c.raw, got, c.expect)
			}
		})
	}
}

// A pair is two addresses the player merges. Both have to be rewritten to the
// machine the client can reach, and a pair missing a track is an error rather
// than a silent video with no sound.
func TestPlaybackFromAnswerRewritesBothTracksOfAPair(t *testing.T) {
	r := httptest.NewRequest("GET", "http://example.test/catalog-items/x/play", nil)
	r.Host = "192.0.2.10:9394"

	got, err := playbackFromAnswer("pair", "", []pairSource{
		{Type: "video", URL: "https://cdn.example.test/v?itag=137", Mime: "video/mp4"},
		{Type: "audio", URL: "https://cdn.example.test/a?itag=140", Mime: "audio/mp4"},
	}, "video/mp4", false, nil, r, "")
	if err != nil {
		t.Fatalf("playbackFromAnswer: %v", err)
	}
	if got.Mode != "pair" || got.AudioURL == "" {
		t.Fatalf("answer = %+v, want a pair with an audio url", got)
	}
	if got.URL != "https://cdn.example.test/v?itag=137" || got.AudioURL != "https://cdn.example.test/a?itag=140" {
		t.Errorf("urls = %q / %q, want the plugin's own addresses", got.URL, got.AudioURL)
	}

	// A plugin that runs here names 127.0.0.1: both tracks must come back as the
	// address the client used, or the player would try its own loopback.
	got, err = playbackFromAnswer("pair", "", []pairSource{
		{Type: "video", URL: "http://127.0.0.1:41234/s/v/index.m3u8"},
		{Type: "audio", URL: "http://127.0.0.1:41234/s/a/index.m3u8"},
	}, "", false, nil, r, "")
	if err != nil {
		t.Fatalf("playbackFromAnswer: %v", err)
	}
	if got.URL != "http://192.0.2.10:41234/s/v/index.m3u8" ||
		got.AudioURL != "http://192.0.2.10:41234/s/a/index.m3u8" {
		t.Errorf("urls = %q / %q, want both rewritten to the client's host", got.URL, got.AudioURL)
	}
}

func TestPlaybackFromAnswerRefusesAnIncompletePair(t *testing.T) {
	r := httptest.NewRequest("GET", "http://example.test/catalog-items/x/play", nil)
	for _, sources := range [][]pairSource{
		{{Type: "video", URL: "https://cdn.example.test/v"}},
		{{Type: "audio", URL: "https://cdn.example.test/a"}},
		{{Type: "video", URL: "https://cdn.example.test/v"}, {Type: "video", URL: "https://cdn.example.test/v2"}},
	} {
		if _, err := playbackFromAnswer("pair", "", sources, "", false, nil, r, ""); err == nil {
			t.Errorf("playbackFromAnswer(%+v) accepted a pair with no audio"+
				" or no video", sources)
		}
	}
}

func TestPlaybackFromAnswerOpensOneAddressForEveryOtherMode(t *testing.T) {
	r := httptest.NewRequest("GET", "http://example.test/catalog-items/x/play", nil)
	r.Host = "192.0.2.10:9394"
	got, err := playbackFromAnswer("remux", "http://127.0.0.1:41234/s/tok/index.m3u8", nil, "application/vnd.apple.mpegurl",
		true, nil, r, "")
	if err != nil {
		t.Fatalf("playbackFromAnswer: %v", err)
	}
	if got.Mode != "remux" || got.AudioURL != "" {
		t.Errorf("answer = %+v, want a single address", got)
	}
	if got.URL != "http://192.0.2.10:41234/s/tok/index.m3u8" {
		t.Errorf("url = %q, want the client's host", got.URL)
	}
	if _, err := playbackFromAnswer("remux", "", nil, "", false, nil, r, ""); err == nil {
		t.Error("an answer with no url was accepted")
	}
}
