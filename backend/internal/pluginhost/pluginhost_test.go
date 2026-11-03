package pluginhost

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
	"github.com/rafaelvieiras/playheadtv/backend/internal/storage"
	"github.com/rafaelvieiras/playheadtv/backend/migrations"
)

// ---------------------------------------------------------------------------
// Fixtures: a real plugin binary, a real tar.gz artifact, a real registry.
// ---------------------------------------------------------------------------

var (
	fixtureOnce sync.Once
	fixturePath string
	fixtureErr  error
)

// fixtureBinary builds testdata/fakeplugin once per test run and returns the
// path of the executable. A host is only interesting when it drives a real
// process, so the tests install and run an actual binary.
func fixtureBinary(t *testing.T) string {
	t.Helper()
	fixtureOnce.Do(func() {
		dir, err := os.MkdirTemp("", "fakeplugin-*")
		if err != nil {
			fixtureErr = err
			return
		}
		exe := filepath.Join(dir, "fakeplugin")
		if runtime.GOOS == "windows" {
			exe += ".exe"
		}
		cmd := exec.Command("go", "build", "-o", exe, "./testdata/fakeplugin")
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			fixtureErr = errors.New("build fakeplugin: " + err.Error() + "\n" + string(out))
			return
		}
		fixturePath = exe
	})
	if fixtureErr != nil {
		t.Fatalf("fake plugin fixture: %v", fixtureErr)
	}
	return fixturePath
}

// artifactTarGz builds a plugin artifact in memory: plugin.json at the root plus
// the fixture binary under the same name it is installed as.
func artifactTarGz(t *testing.T, manifest Manifest) []byte {
	t.Helper()
	binary, err := os.ReadFile(fixtureBinary(t))
	if err != nil {
		t.Fatalf("read fixture binary: %v", err)
	}
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	writeEntry := func(name string, mode int64, body []byte) {
		t.Helper()
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: mode, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatalf("tar header %s: %v", name, err)
		}
		if _, err := tw.Write(body); err != nil {
			t.Fatalf("tar body %s: %v", name, err)
		}
	}
	writeEntry("plugin.json", 0o644, manifestJSON)
	writeEntry(manifest.Entrypoint, 0o755, binary)
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	return buf.Bytes()
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// testManifest is a valid manifest for the fixture, matching what the fixture
// reports in its `info` answer.
func testManifest() Manifest {
	return Manifest{
		ID:           "fake",
		Name:         "Fake source",
		Version:      "1.0.0",
		Protocol:     Protocol,
		Entrypoint:   "fakeplugin",
		Capabilities: []string{CapabilityCatalogList, CapabilityMediaResolve},
	}
}

func TestAManifestDeclaresTheSettingsItsSourcesTake(t *testing.T) {
	// A settings screen is built from this, so a field nobody could render is a
	// broken release rather than an empty row on the client.
	m := testManifest()
	m.Config = []ConfigField{
		{Key: "max_height", Label: "Quality", Type: ConfigTypeChoice,
			Options: []ConfigOption{
				{Value: json.RawMessage("1080"), Label: "1080p"},
				{Value: json.RawMessage("720"), Label: "720p"},
			}},
		{Key: "audio_only", Label: "Audio only", Type: ConfigTypeBool},
	}
	if err := m.Validate(); err != nil {
		t.Fatalf("a declared settings list was refused: %v", err)
	}

	broken := map[string]ConfigField{
		"a choice with no options":        {Key: "quality", Label: "Quality", Type: ConfigTypeChoice},
		"a key that is not a member name": {Key: "Max Height", Label: "Quality", Type: ConfigTypeText},
		"a field with no label":           {Key: "quality", Type: ConfigTypeText},
		"an unknown type":                 {Key: "quality", Label: "Quality", Type: "slider"},
		"a choice option with no value": {Key: "quality", Label: "Quality", Type: ConfigTypeChoice,
			Options: []ConfigOption{{Label: "1080p"}}},
	}
	for name, field := range broken {
		t.Run(name, func(t *testing.T) {
			m := testManifest()
			m.Config = []ConfigField{field}
			if err := m.Validate(); !errors.Is(err, ErrIncompatible) {
				t.Fatalf("validate = %v, want an incompatible-plugin error", err)
			}
		})
	}

	t.Run("the same key twice", func(t *testing.T) {
		m := testManifest()
		m.Config = []ConfigField{
			{Key: "quality", Label: "Quality", Type: ConfigTypeText},
			{Key: "quality", Label: "Quality again", Type: ConfigTypeText},
		}
		if err := m.Validate(); !errors.Is(err, ErrIncompatible) {
			t.Fatalf("validate = %v, want an incompatible-plugin error", err)
		}
	})
}

// registryServer serves a registry document and its artifacts. It is the
// smallest thing a host can install from, it can start listing another version
// later (a release published while the host is running), and it records how
// many times each path was fetched so a test can assert the cache was used.
type registryServer struct {
	*httptest.Server
	mu           sync.Mutex
	fetches      map[string]int
	published    []registryEntry
	cacheHeaders []string
}

// registryEntry is one version the test registry currently lists.
type registryEntry struct {
	manifest Manifest
	artifact []byte
	os, arch string
}

func newRegistryServer(t *testing.T, artifact []byte, manifest Manifest) *registryServer {
	t.Helper()
	rs := &registryServer{fetches: map[string]int{}}
	mux := http.NewServeMux()
	var base string
	mux.HandleFunc("/registry.json", func(w http.ResponseWriter, r *http.Request) {
		rs.count("/registry.json")
		rs.mu.Lock()
		rs.cacheHeaders = append(rs.cacheHeaders, r.Header.Get("Cache-Control"))
		rs.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(rs.document(base))
	})
	mux.HandleFunc("/artifact/", func(w http.ResponseWriter, r *http.Request) {
		rs.count(r.URL.Path)
		version := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/artifact/"), ".tar.gz")
		artifact, ok := rs.artifactFor(version)
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(artifact)
	})
	rs.Server = httptest.NewServer(mux)
	base = rs.URL
	t.Cleanup(rs.Close)
	rs.publish(artifact, manifest)
	return rs
}

// publish makes the registry list one more version from now on.
func (rs *registryServer) publish(artifact []byte, manifest Manifest) {
	rs.publishPlatform(runtime.GOOS, runtime.GOARCH, artifact, manifest)
}

// publishPlatform is publish for a version that ships another platform, which is
// how a test asks what a host does when nothing matches this machine.
func (rs *registryServer) publishPlatform(os, arch string, artifact []byte, manifest Manifest) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	rs.published = append(rs.published, registryEntry{
		manifest: manifest, artifact: artifact, os: os, arch: arch,
	})
}

// document builds the registry the server serves right now, oldest version
// first, so a document fetched before a publish does not list it.
func (rs *registryServer) document(base string) Index {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	entry := IndexPlugin{ID: "", Name: ""}
	for _, published := range rs.published {
		entry.ID, entry.Name = published.manifest.ID, published.manifest.Name
		entry.Versions = append(entry.Versions, Version{
			Version:  published.manifest.Version,
			Protocol: published.manifest.Protocol,
			Artifacts: []Artifact{{
				OS: published.os, Arch: published.arch,
				URL:    base + "/artifact/" + published.manifest.Version + ".tar.gz",
				SHA256: sha256Hex(published.artifact),
				Size:   int64(len(published.artifact)),
			}},
		})
	}
	return Index{Schema: 1, Plugins: []IndexPlugin{entry}}
}

func (rs *registryServer) artifactFor(version string) ([]byte, bool) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	for _, published := range rs.published {
		if published.manifest.Version == version {
			return published.artifact, true
		}
	}
	return nil, false
}

// indexURL is the registry document's address, which is what a host is
// configured with.
func (rs *registryServer) indexURL() string { return rs.URL + "/registry.json" }

func (rs *registryServer) count(path string) {
	rs.mu.Lock()
	rs.fetches[path]++
	rs.mu.Unlock()
}

func (rs *registryServer) fetchCount(path string) int {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return rs.fetches[path]
}

// lastCacheControl is what the last registry fetch asked of any cache in the way.
func (rs *registryServer) lastCacheControl() string {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if len(rs.cacheHeaders) == 0 {
		return ""
	}
	return rs.cacheHeaders[len(rs.cacheHeaders)-1]
}

// artifactFetches counts artifact downloads, whichever version they were for.
func (rs *registryServer) artifactFetches() int {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	total := 0
	for path, n := range rs.fetches {
		if strings.HasPrefix(path, "/artifact/") {
			total += n
		}
	}
	return total
}

// newTestDB opens a migrated database so the host can record installs.
func newTestDB(t *testing.T) *storage.SQLite {
	t.Helper()
	db, err := storage.Open(filepath.Join(t.TempDir(), "playheadtv.db"))
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := storage.RunMigrations(context.Background(), db, migrations.FS); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}
	return db
}

// newTestHost builds a host pointed at a registry serving the fixture plugin,
// plus the media source that references it.
func newTestHost(t *testing.T, config string) (*Host, domain.MediaSource) {
	t.Helper()
	manifest := testManifest()
	artifact := artifactTarGz(t, manifest)
	rs := newRegistryServer(t, artifact, manifest)

	db := newTestDB(t)
	host := New(db, t.TempDir(), rs.indexURL())
	t.Cleanup(host.Close)
	if _, err := host.Install(context.Background(), manifest.ID, ""); err != nil {
		t.Fatalf("Install: %v", err)
	}

	pluginID := manifest.ID
	configJSON := config
	ms := domain.MediaSource{
		ID:               "src-1",
		TenantID:         "default",
		Type:             domain.SourcePlugin,
		Name:             "Fixture",
		SyncMode:         domain.SyncPull,
		PluginID:         &pluginID,
		PluginConfigJSON: &configJSON,
	}
	return host, ms
}

// ---------------------------------------------------------------------------
// Manifest and registry parsing.
// ---------------------------------------------------------------------------

func TestManifestValidate(t *testing.T) {
	valid := testManifest()
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid manifest rejected: %v", err)
	}

	cases := map[string]func(*Manifest){
		"bad id":             func(m *Manifest) { m.ID = "Fake Plugin" },
		"missing name":       func(m *Manifest) { m.Name = "" },
		"bad version":        func(m *Manifest) { m.Version = "../1.0" },
		"wrong protocol":     func(m *Manifest) { m.Protocol = 2 },
		"absolute entry":     func(m *Manifest) { m.Entrypoint = "/bin/sh" },
		"traversal entry":    func(m *Manifest) { m.Entrypoint = "../fakeplugin" },
		"no capabilities":    func(m *Manifest) { m.Capabilities = nil },
		"unknown capability": func(m *Manifest) { m.Capabilities = []string{"catalog.write"} },
	}
	for name, mutate := range cases {
		m := testManifest()
		mutate(&m)
		if err := m.Validate(); !errors.Is(err, ErrIncompatible) {
			t.Errorf("%s: err = %v, want ErrIncompatible", name, err)
		}
	}
}

func TestCompareVersionsOrdersDatesAndSemver(t *testing.T) {
	ordered := []string{"1.4.0", "1.10.0", "2026.9.1", "2026.10.26"}
	for i := 1; i < len(ordered); i++ {
		if compareVersions(ordered[i-1], ordered[i]) >= 0 {
			t.Errorf("%q should sort before %q", ordered[i-1], ordered[i])
		}
		if compareVersions(ordered[i], ordered[i-1]) <= 0 {
			t.Errorf("%q should sort after %q", ordered[i], ordered[i-1])
		}
	}
	if compareVersions("1.0.0", "1.0.0") != 0 {
		t.Error("equal versions should compare equal")
	}
}

func TestParseIndexRejectsBadEntries(t *testing.T) {
	good := `{"schema":1,"plugins":[{"id":"fake","name":"Fake","versions":[{"version":"1.0.0","protocol":1,` +
		`"artifacts":[{"os":"linux","arch":"amd64","url":"https://example.test/a.tar.gz","sha256":"` +
		strings.Repeat("a", 64) + `"}]}]}]}`
	if _, err := ParseIndex([]byte(good)); err != nil {
		t.Fatalf("valid index rejected: %v", err)
	}

	bad := map[string]string{
		"schema":  `{"schema":2,"plugins":[]}`,
		"id":      strings.Replace(good, `"id":"fake"`, `"id":"../fake"`, 1),
		"version": strings.Replace(good, `"version":"1.0.0"`, `"version":"1.0.0/../x"`, 1),
		"sha256":  strings.Replace(good, strings.Repeat("a", 64), "deadbeef", 1),
		"os":      strings.Replace(good, `"os":"linux"`, `"os":"plan9"`, 1),
		"url":     strings.Replace(good, `"url":"https://example.test/a.tar.gz"`, `"url":"file:///etc/passwd"`, 1),
	}
	for name, raw := range bad {
		if _, err := ParseIndex([]byte(raw)); err == nil {
			t.Errorf("%s: invalid index accepted", name)
		}
	}
}

func TestIndexLatestSkipsIncompatibleProtocols(t *testing.T) {
	idx, err := ParseIndex([]byte(`{"schema":1,"plugins":[{"id":"fake","name":"Fake","versions":[
		{"version":"2.0.0","protocol":2,"artifacts":[]},
		{"version":"1.0.0","protocol":1,"artifacts":[]}]}]}`))
	if err != nil {
		t.Fatalf("ParseIndex: %v", err)
	}
	entry, ok := idx.Find("fake")
	if !ok {
		t.Fatal("plugin not found")
	}
	latest, ok := entry.Latest()
	if !ok || latest.Version != "1.0.0" {
		t.Errorf("Latest = %v (ok=%v), want 1.0.0", latest.Version, ok)
	}
}

// ---------------------------------------------------------------------------
// Artifact handling.
// ---------------------------------------------------------------------------

func TestExtractRejectsPathTraversal(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "evil.tar.gz")
	f, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	body := []byte("pwned")
	if err := tw.WriteHeader(&tar.Header{Name: "../escaped.txt", Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatal(err)
	}
	tw.Close()
	gz.Close()
	f.Close()

	dest := filepath.Join(dir, "out")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := extractArtifact(archive, dest); err == nil {
		t.Fatal("path traversal accepted")
	}
	if _, err := os.Stat(filepath.Join(dir, "escaped.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("entry escaped the target directory")
	}
}

func TestInstallRejectsHashMismatch(t *testing.T) {
	manifest := testManifest()
	artifact := artifactTarGz(t, manifest)

	mux := http.NewServeMux()
	var base string
	mux.HandleFunc("/registry.json", func(w http.ResponseWriter, r *http.Request) {
		// Advertise the right hash but serve different bytes.
		_ = json.NewEncoder(w).Encode(Index{Schema: 1, Plugins: []IndexPlugin{{
			ID: manifest.ID, Name: manifest.Name,
			Versions: []Version{{Version: manifest.Version, Protocol: 1, Artifacts: []Artifact{{
				OS: runtime.GOOS, Arch: runtime.GOARCH, URL: base + "/artifact.tar.gz", SHA256: sha256Hex(artifact),
			}}}},
		}}})
	})
	mux.HandleFunc("/artifact.tar.gz", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not the archive"))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	base = srv.URL

	db := newTestDB(t)
	host := New(db, t.TempDir(), srv.URL)
	defer host.Close()

	if _, err := host.Install(context.Background(), manifest.ID, ""); err == nil {
		t.Fatal("install accepted an artifact whose bytes do not match the registry hash")
	}
	if _, err := host.Installed(context.Background(), manifest.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("plugin row exists after a failed install: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Process protocol.
// ---------------------------------------------------------------------------

func TestProcessSpeaksTheProtocol(t *testing.T) {
	host, _ := newTestHost(t, `{"url":"https://example.test/channel"}`)
	installed, err := host.Installed(context.Background(), "fake")
	if err != nil {
		t.Fatalf("Installed: %v", err)
	}
	proc, err := host.processFor(context.Background(), installed)
	if err != nil {
		t.Fatalf("processFor: %v", err)
	}

	var resolved struct {
		URL       string `json:"url"`
		Mime      string `json:"mime"`
		ExpiresAt int64  `json:"expires_at"`
	}
	if err := proc.Call(context.Background(), "media.resolve", map[string]any{
		"config": map[string]any{},
		"item":   map[string]any{"external_id": "ext-1", "duration_ms": 600000},
	}, &resolved); err != nil {
		t.Fatalf("media.resolve: %v", err)
	}
	if resolved.URL == "" || resolved.Mime != "video/mp4" || resolved.ExpiresAt == 0 {
		t.Errorf("resolve = %+v", resolved)
	}

	// A plugin-reported failure is a typed error, not a transport failure.
	err = proc.Call(context.Background(), "fail", nil, nil)
	var perr *Error
	if !errors.As(err, &perr) || perr.Code != "unavailable" {
		t.Errorf("fail call: err = %v, want *Error{Code: unavailable}", err)
	}
}

func TestProcessTimeoutKillsThePlugin(t *testing.T) {
	host, _ := newTestHost(t, `{}`)
	installed, err := host.Installed(context.Background(), "fake")
	if err != nil {
		t.Fatalf("Installed: %v", err)
	}
	proc, err := host.processFor(context.Background(), installed)
	if err != nil {
		t.Fatalf("processFor: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := proc.Call(ctx, "hang", nil, nil); err == nil {
		t.Fatal("a hung call returned no error")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("timeout took %s, the watchdog did not kill the plugin", elapsed)
	}
}

// ---------------------------------------------------------------------------
// Host: install, list, enable/disable, uninstall.
// ---------------------------------------------------------------------------

func TestListSourcePagesThroughThePlugin(t *testing.T) {
	host, ms := newTestHost(t, `{"url":"https://example.test/channel"}`)
	items, err := host.ListSource(context.Background(), ms)
	if err != nil {
		t.Fatalf("ListSource: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("got %d items, want 3 across two pages: %+v", len(items), items)
	}
	if items[0].ExternalID != "ext-1" || items[2].ExternalID != "ext-3" {
		t.Errorf("items out of order: %s ... %s", items[0].ExternalID, items[2].ExternalID)
	}
	if items[2].Kind != domain.CatalogEpisode || items[2].SeriesTitle == nil || *items[2].SeriesTitle != "A series" {
		t.Errorf("episode fields not mapped: %+v", items[2])
	}
	if items[1].Genres != "documentary" || items[1].ProductionYear == nil || *items[1].ProductionYear != 2026 {
		t.Errorf("metadata not mapped: %+v", items[1])
	}
}

func TestListSourceDropsInvalidDurations(t *testing.T) {
	host, ms := newTestHost(t, `{"url":"bad-duration"}`)
	items, err := host.ListSource(context.Background(), ms)
	if err != nil {
		t.Fatalf("ListSource: %v", err)
	}
	for _, it := range items {
		if it.DurationMs <= 0 {
			t.Errorf("item %q kept with duration %d", it.ExternalID, it.DurationMs)
		}
	}
	if len(items) != 3 {
		t.Errorf("got %d items, want the 3 valid ones", len(items))
	}
}

func TestListSourceEmptyConfig(t *testing.T) {
	host, ms := newTestHost(t, `{"url":"empty"}`)
	items, err := host.ListSource(context.Background(), ms)
	if err != nil {
		t.Fatalf("ListSource: %v", err)
	}
	if len(items) != 0 {
		t.Errorf("got %d items, want 0", len(items))
	}
}

func TestListSourceRequiresAnEnabledPlugin(t *testing.T) {
	host, ms := newTestHost(t, `{}`)
	ctx := context.Background()
	if err := host.SetEnabled(ctx, "fake", false); err != nil {
		t.Fatalf("SetEnabled: %v", err)
	}
	if _, err := host.ListSource(ctx, ms); !errors.Is(err, ErrNotInstalled) {
		t.Errorf("disabled plugin: err = %v, want ErrNotInstalled", err)
	}

	missing := ms
	other := "not-installed"
	missing.PluginID = &other
	if _, err := host.ListSource(ctx, missing); !errors.Is(err, ErrNotInstalled) {
		t.Errorf("missing plugin: err = %v, want ErrNotInstalled", err)
	}
}

func TestListSourceRejectsAnOlderPluginThanTheMinimum(t *testing.T) {
	host, ms := newTestHost(t, `{}`)
	min := "2.0.0"
	ms.PluginMinVersion = &min
	if _, err := host.ListSource(context.Background(), ms); err == nil {
		t.Fatal("a source requiring 2.0.0 was listed by a 1.0.0 plugin")
	}
}

func TestInstallIsIdempotentAndUninstallRemovesEverything(t *testing.T) {
	manifest := testManifest()
	artifact := artifactTarGz(t, manifest)
	rs := newRegistryServer(t, artifact, manifest)
	db := newTestDB(t)
	host := New(db, t.TempDir(), rs.indexURL())
	defer host.Close()
	ctx := context.Background()

	first, err := host.Install(ctx, manifest.ID, "")
	if err != nil {
		t.Fatalf("first Install: %v", err)
	}
	if first.Version != manifest.Version || !first.Enabled {
		t.Errorf("installed row = %+v", first)
	}
	if _, err := host.Install(ctx, manifest.ID, ""); err != nil {
		t.Fatalf("second Install: %v", err)
	}
	installed, err := host.ListInstalled(ctx)
	if err != nil {
		t.Fatalf("ListInstalled: %v", err)
	}
	if len(installed) != 1 {
		t.Fatalf("got %d installed plugins, want 1", len(installed))
	}
	// The second install reused the cached artifact: one download.
	if got := rs.artifactFetches(); got != 1 {
		t.Errorf("artifact fetched %d times, want 1 (cache miss on the second install)", got)
	}

	if err := host.Uninstall(ctx, manifest.ID); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	if _, err := host.Installed(ctx, manifest.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("row survived uninstall: %v", err)
	}
	if _, err := os.Stat(filepath.Join(host.dir, "installed", manifest.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("plugin directory survived uninstall: %v", err)
	}
}

// A release published while the backend is running is the case an install
// exists for: the cached registry copy does not list it yet, and answering "no
// such version" from that copy (for the rest of registryTTL) is exactly what
// made an install right after a release fail with a 404.
func TestAnInstallForAVersionTheCachedRegistryLacksAsksTheRegistryAgain(t *testing.T) {
	older := testManifest()
	olderArtifact := artifactTarGz(t, older)
	rs := newRegistryServer(t, olderArtifact, older)

	db := newTestDB(t)
	host := New(db, t.TempDir(), rs.indexURL())
	defer host.Close()
	ctx := context.Background()

	// The host has looked at the registry, so it holds a copy that does not
	// know about the newer version yet.
	if _, _, err := host.Index(ctx); err != nil {
		t.Fatalf("Index: %v", err)
	}

	newer := older
	newer.Version = "1.1.0"
	newerArtifact := artifactTarGz(t, newer)
	rs.publish(newerArtifact, newer)

	got, err := host.Install(ctx, newer.ID, newer.Version)
	if err != nil {
		t.Fatalf("Install of a just-published version: %v", err)
	}
	if got.Version != newer.Version {
		t.Errorf("installed %s, want %s", got.Version, newer.Version)
	}
	if got := rs.lastCacheControl(); got != "no-cache" {
		t.Errorf("the forced read sent Cache-Control %q, want no-cache", got)
	}
	// The re-fetched copy replaced the cached one, so waiting for the TTL is
	// not a workaround anyone needs any more.
	if idx, _, err := host.Index(ctx); err != nil {
		t.Fatalf("Index after install: %v", err)
	} else if _, ok := mustEntry(t, idx, newer.ID).VersionByNumber(newer.Version); !ok {
		t.Errorf("cached index still does not list %s", newer.Version)
	}
}

// One round of misses costs one fetch, not one per miss: the second attempt
// inside registryForceFloor reuses the copy the first one fetched.
func TestAMissThatFoundNothingDoesNotRefetchTheRegistry(t *testing.T) {
	manifest := testManifest()
	rs := newRegistryServer(t, artifactTarGz(t, manifest), manifest)
	db := newTestDB(t)
	host := New(db, t.TempDir(), rs.indexURL())
	defer host.Close()
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if _, err := host.Install(ctx, manifest.ID, "9.9.9"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("attempt %d: err = %v, want ErrNotFound", i+1, err)
		}
	}
	// One for the first Index, one for the forced re-check that found nothing.
	if got := rs.fetchCount("/registry.json"); got != 2 {
		t.Errorf("registry fetched %d times for three misses, want 2", got)
	}
}

// An install that the cached copy can answer still never touches the registry
// a second time.
func TestAnInstallTheCachedRegistryAnswersDoesNotRefetch(t *testing.T) {
	manifest := testManifest()
	rs := newRegistryServer(t, artifactTarGz(t, manifest), manifest)
	db := newTestDB(t)
	host := New(db, t.TempDir(), rs.indexURL())
	defer host.Close()
	ctx := context.Background()

	if _, err := host.Install(ctx, manifest.ID, manifest.Version); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if got := rs.fetchCount("/registry.json"); got != 1 {
		t.Errorf("registry fetched %d times for one install, want 1", got)
	}
}

// A version the registry lists but ships nothing for this machine is a different
// answer from "unknown version", and the fresh copy must not blur the two: the
// operator installs elsewhere, or waits for a release that includes the platform.
func TestAnInstallWithoutAnArtifactForThisPlatformSaysNoArtifact(t *testing.T) {
	manifest := testManifest()
	rs := newRegistryServer(t, artifactTarGz(t, manifest), manifest)
	db := newTestDB(t)
	host := New(db, t.TempDir(), rs.indexURL())
	defer host.Close()

	elsewhere := manifest
	elsewhere.Version = "2.0.0"
	otherOS, otherArch := "windows", "arm64"
	if runtime.GOOS == otherOS && runtime.GOARCH == otherArch {
		otherOS, otherArch = "linux", "amd64"
	}
	rs.publishPlatform(otherOS, otherArch, artifactTarGz(t, elsewhere), elsewhere)

	_, err := host.Install(context.Background(), manifest.ID, elsewhere.Version)
	if !errors.Is(err, ErrNoArtifact) {
		t.Errorf("install of a version for another platform: err = %v, want ErrNoArtifact", err)
	}
}

func mustEntry(t *testing.T, idx Index, id string) IndexPlugin {
	t.Helper()
	entry, ok := idx.Find(id)
	if !ok {
		t.Fatalf("registry does not list %q", id)
	}
	return entry
}

func TestIndexWithoutARegistryIsEmpty(t *testing.T) {
	host := New(newTestDB(t), t.TempDir(), "")
	defer host.Close()
	idx, raw, err := host.Index(context.Background())
	if err != nil {
		t.Fatalf("Index: %v", err)
	}
	if len(idx.Plugins) != 0 {
		t.Errorf("got %d plugins from an unconfigured registry", len(idx.Plugins))
	}
	if !strings.Contains(string(raw), `"plugins":[]`) {
		t.Errorf("raw index = %s", raw)
	}
	if _, err := host.Install(context.Background(), "fake", ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("install without a registry: err = %v, want ErrNotFound", err)
	}
}
