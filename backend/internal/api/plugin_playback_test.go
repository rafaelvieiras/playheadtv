package api

import (
	"net/http/httptest"
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
