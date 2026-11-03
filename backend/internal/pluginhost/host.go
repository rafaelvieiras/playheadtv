package pluginhost

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/rafaelvieiras/playheadtv/backend/internal/catalogsync"
	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
	"github.com/rafaelvieiras/playheadtv/backend/internal/logging"
	"github.com/rafaelvieiras/playheadtv/backend/internal/storage"
)

// registryTTL is how long a fetched registry index is reused before the host
// asks for it again. It only affects how quickly a newly published plugin
// version becomes installable, so minutes are plenty and a registry that is
// briefly down keeps working from the cached copy.
const registryTTL = 5 * time.Minute

// registryTimeout bounds one registry fetch.
const registryTimeout = 20 * time.Second

// registryForceFloor keeps an install that is looking for an unlisted version
// from re-fetching the registry on every retry. The first miss re-fetches, so
// the copy in hand is already the answer, and a second miss inside this window
// would only ask the same question again.
const registryForceFloor = 5 * time.Second

// catalogPageTimeout bounds one `catalog.list` call. It is generous on purpose:
// a plugin that lists a remote channel may have to page through a site, and the
// alternative to waiting is a source that never populates.
const catalogPageTimeout = 90 * time.Second

// resolveTimeout bounds one media.resolve. An extractor has to fetch the item's
// page and plan formats, and a stream it produces is already running when it
// answers, so this is deliberately longer than a catalog page.
const resolveTimeout = 120 * time.Second

// The modes a plugin may answer in (docs/PLUGINS.md "Modes").
const (
	ModeSingle = "single"
	ModePair   = "pair"
	ModeRemux  = "remux"
)

// catalogPageLimit is the page size the host asks for; a plugin may return any
// number it likes.
const catalogPageLimit = 50

// catalogMaxPages bounds a single listing. 200 pages × 50 items is 10k items
// for one source — far above a sane channel or playlist, and low enough that a
// plugin stuck on a repeating cursor cannot loop forever.
const catalogMaxPages = 200

// ErrNotInstalled is returned when a source names a plugin this host does not
// have (or has disabled).
var ErrNotInstalled = errors.New("pluginhost: plugin is not installed")

// Host owns the installed plugins of one deployment: the registry it can install
// from, the extracted artifacts on disk, and the child processes currently
// running. Both the backend and a desktop client use this same shape (the client
// has its own implementation in Dart).
type Host struct {
	db       storage.DB
	dir      string
	client   *http.Client
	indexURL string

	indexMu      sync.Mutex
	index        *Index
	indexRaw     []byte
	indexAt      time.Time
	indexForceAt time.Time

	installMu sync.Mutex

	procMu sync.Mutex
	procs  map[string]*Process
}

// New creates a Host. dir is the directory plugins are installed under (created
// on demand); indexURL is the registry document, empty for "no registry" (the
// host then installs nothing and reports every plugin source as not installed).
func New(db storage.DB, dir, indexURL string) *Host {
	return &Host{
		db:       db,
		dir:      dir,
		client:   &http.Client{Timeout: registryTimeout},
		indexURL: strings.TrimSpace(indexURL),
		procs:    make(map[string]*Process),
	}
}

// RegistryConfigured reports whether the host has a registry to install from.
func (h *Host) RegistryConfigured() bool { return h.indexURL != "" }

// InstalledDir is where artifacts are extracted, exposed so callers (and tests)
// can point a process at a plugin without going through the DB.
func (h *Host) InstalledDir(id, version string) string {
	return filepath.Join(h.dir, "installed", id, version)
}

func (h *Host) cacheDir() string { return filepath.Join(h.dir, "cache", "artifacts") }

// Index returns the registry document and its raw bytes, fetching and caching
// as needed. The raw bytes are what GET /plugins serves, so a client installs
// from exactly the registry this backend validated.
func (h *Host) Index(ctx context.Context) (Index, []byte, error) {
	if !h.RegistryConfigured() {
		return Index{Schema: 1}, []byte(`{"schema":1,"plugins":[]}`), nil
	}
	h.indexMu.Lock()
	defer h.indexMu.Unlock()
	if h.index != nil && time.Since(h.indexAt) < registryTTL {
		return *h.index, h.indexRaw, nil
	}
	return h.fetchIndex(ctx, false)
}

// IndexFresh fetches the registry now instead of waiting for the TTL to expire,
// and replaces the cached copy with it. It exists for the one case where the
// cached copy gives a wrong answer: an install naming a version that was
// published minutes ago and is not in the cache yet (docs/PLUGINS.md "Install").
//
// A round of install misses therefore costs one fetch, not one per miss:
// registryForceFloor reuses the copy the previous miss just fetched.
func (h *Host) IndexFresh(ctx context.Context) (Index, []byte, error) {
	if !h.RegistryConfigured() {
		return Index{Schema: 1}, []byte(`{"schema":1,"plugins":[]}`), nil
	}
	h.indexMu.Lock()
	defer h.indexMu.Unlock()
	if h.index != nil && time.Since(h.indexForceAt) < registryForceFloor {
		return *h.index, h.indexRaw, nil
	}
	idx, raw, err := h.fetchIndex(ctx, true)
	if err != nil {
		return idx, raw, err
	}
	h.indexForceAt = time.Now()
	logging.Infof("pluginhost: re-read the registry, %d plugin(s) offered", len(idx.Plugins))
	return idx, raw, nil
}

// fetchIndex is the actual registry read. Callers hold indexMu. revalidate asks
// any cache in the way to answer with the document it has now instead of the one
// it holds: the point of the forced read is that the held copy is what is wrong.
func (h *Host) fetchIndex(ctx context.Context, revalidate bool) (Index, []byte, error) {
	reqCtx, cancel := context.WithTimeout(ctx, registryTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, h.indexURL, nil)
	if err != nil {
		return Index{}, nil, err
	}
	if revalidate {
		req.Header.Set("Cache-Control", "no-cache")
	}
	resp, err := h.client.Do(req)
	if err != nil {
		return Index{}, nil, fmt.Errorf("pluginhost: fetch registry: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Index{}, nil, fmt.Errorf("pluginhost: fetch registry: %s", resp.Status)
	}
	raw, err := readAllLimited(resp.Body, 4<<20)
	if err != nil {
		return Index{}, nil, fmt.Errorf("pluginhost: read registry: %w", err)
	}
	idx, err := ParseIndex(raw)
	if err != nil {
		return Index{}, nil, err
	}
	h.index, h.indexRaw, h.indexAt = &idx, raw, time.Now()
	return idx, raw, nil
}

func readAllLimited(r io.Reader, limit int64) ([]byte, error) {
	buf, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(buf)) > limit {
		return nil, fmt.Errorf("response exceeds %d bytes", limit)
	}
	return buf, nil
}

// ListInstalled returns every installed plugin row.
func (h *Host) ListInstalled(ctx context.Context) ([]domain.Plugin, error) {
	var plugins []domain.Plugin
	err := h.db.Query(ctx, &plugins, "SELECT "+pluginColumns+" FROM plugin ORDER BY id")
	return plugins, err
}

// Installed returns one plugin row.
func (h *Host) Installed(ctx context.Context, id string) (domain.Plugin, error) {
	var p domain.Plugin
	if err := h.db.Query(ctx, &p, "SELECT "+pluginColumns+" FROM plugin WHERE id = ?", id); err != nil {
		return domain.Plugin{}, err
	}
	return p, nil
}

// Manifest reads the installed plugin's own plugin.json, so a caller can show
// what the plugin declares without knowing anything about it. The second return
// is false when the row exists but its files are gone: the plugin cannot run,
// and its declared settings are not worth guessing at.
func (h *Host) Manifest(ctx context.Context, id string) (Manifest, bool) {
	installed, err := h.Installed(ctx, id)
	if err != nil {
		return Manifest{}, false
	}
	manifest, err := readManifest(h.InstalledDir(installed.ID, installed.Version))
	if err != nil {
		return Manifest{}, false
	}
	return manifest, true
}

// Install downloads, verifies and extracts one plugin version (empty version
// means the newest the host can run), then records it. Installing a second
// version replaces the first: one row and one extracted directory per plugin.
// artifactToInstall resolves what to download for id; an empty version means the
// latest one the registry lists. It answers from the cached registry copy when
// that copy knows the version, and asks the registry again when it does not: a
// version published minutes ago is exactly the case an install exists for, and a
// copy that predates it would otherwise report it as absent until registryTTL
// expired. The error therefore comes from the fresh copy, never the stale one.
func (h *Host) artifactToInstall(ctx context.Context, id, version string) (Version, Artifact, error) {
	idx, _, err := h.Index(ctx)
	if err != nil {
		return Version{}, Artifact{}, err
	}
	ver, artifact, ok := installTarget(idx, id, version)
	if ok {
		return ver, artifact, nil
	}
	logging.Debugf("pluginhost: %s %q is not in the registry copy in hand", id, version)
	fresh, _, err := h.IndexFresh(ctx)
	if err != nil {
		return Version{}, Artifact{}, err
	}
	if ver, artifact, ok := installTarget(fresh, id, version); ok {
		return ver, artifact, nil
	}
	return Version{}, Artifact{}, installTargetError(fresh, id, version)
}

// installTarget resolves the version and this platform's artifact for it.
func installTarget(idx Index, id, version string) (Version, Artifact, bool) {
	entry, ok := idx.Find(id)
	if !ok {
		return Version{}, Artifact{}, false
	}
	ver, ok := entry.VersionByNumber(version)
	if !ok {
		return Version{}, Artifact{}, false
	}
	artifact, ok := ver.ArtifactFor(runtime.GOOS, runtime.GOARCH)
	if !ok {
		return Version{}, Artifact{}, false
	}
	return ver, artifact, true
}

// installTargetError says why nothing can be installed, in the order an operator
// can act on it: unknown plugin, unknown version, wrong platform.
func installTargetError(idx Index, id, version string) error {
	entry, ok := idx.Find(id)
	if !ok {
		return fmt.Errorf("%w: no plugin %q", ErrNotFound, id)
	}
	ver, ok := entry.VersionByNumber(version)
	if !ok {
		return fmt.Errorf("%w: %s has no version %q", ErrNotFound, id, version)
	}
	return fmt.Errorf("%w: %s %s ships nothing for %s/%s",
		ErrNoArtifact, id, ver.Version, runtime.GOOS, runtime.GOARCH)
}

func (h *Host) Install(ctx context.Context, id, version string) (domain.Plugin, error) {
	h.installMu.Lock()
	defer h.installMu.Unlock()

	ver, artifact, err := h.artifactToInstall(ctx, id, version)
	if err != nil {
		return domain.Plugin{}, err
	}

	archive, err := h.downloadArtifact(ctx, artifact)
	if err != nil {
		return domain.Plugin{}, err
	}

	parent := filepath.Dir(h.InstalledDir(id, ver.Version))
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return domain.Plugin{}, fmt.Errorf("pluginhost: create plugin directory: %w", err)
	}
	staging, err := os.MkdirTemp(parent, ".staging-*")
	if err != nil {
		return domain.Plugin{}, err
	}
	defer os.RemoveAll(staging)

	if err := extractArtifact(archive, staging); err != nil {
		return domain.Plugin{}, err
	}
	manifest, err := readManifest(staging)
	if err != nil {
		return domain.Plugin{}, err
	}
	if manifest.ID != id || manifest.Version != ver.Version {
		return domain.Plugin{}, fmt.Errorf("%w: artifact is %s %s, registry said %s %s",
			ErrIncompatible, manifest.ID, manifest.Version, id, ver.Version)
	}

	target := h.InstalledDir(id, ver.Version)
	if err := os.RemoveAll(target); err != nil {
		return domain.Plugin{}, err
	}
	if err := os.Rename(staging, target); err != nil {
		return domain.Plugin{}, fmt.Errorf("pluginhost: install plugin: %w", err)
	}

	now := time.Now().UnixMilli()
	_, err = h.Installed(ctx, id)
	exists := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return domain.Plugin{}, err
	}
	if exists {
		err = h.db.Exec(ctx,
			`UPDATE plugin SET version = ?, protocol = ?, os = ?, arch = ?, artifact_sha256 = ?,
			 entrypoint = ?, capabilities = ?, enabled = 1, updated_at = ? WHERE id = ?`,
			manifest.Version, manifest.Protocol, artifact.OS, artifact.Arch, artifact.SHA256,
			manifest.Entrypoint, strings.Join(manifest.Capabilities, ","), now, id)
	} else {
		err = h.db.Exec(ctx,
			`INSERT INTO plugin (id, version, protocol, os, arch, artifact_sha256, entrypoint, capabilities, enabled, installed_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?)`,
			id, manifest.Version, manifest.Protocol, artifact.OS, artifact.Arch, artifact.SHA256,
			manifest.Entrypoint, strings.Join(manifest.Capabilities, ","), now, now)
	}
	if err != nil {
		return domain.Plugin{}, err
	}

	// A replaced version leaves its directory behind; remove every sibling so
	// the disk only ever holds what the row says.
	h.pruneVersions(id, manifest.Version)
	h.stop(id)

	logging.Infof("pluginhost: installed %s %s (%s/%s)", id, manifest.Version, artifact.OS, artifact.Arch)
	return h.Installed(ctx, id)
}

// pruneVersions removes every extracted version of id except keep.
func (h *Host) pruneVersions(id, keep string) {
	dir := filepath.Join(h.dir, "installed", id)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.Name() == keep {
			continue
		}
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			logging.Warnf("pluginhost: remove old version %s/%s: %v", id, e.Name(), err)
		}
	}
}

// Uninstall removes the row, the extracted artifact and any running process.
func (h *Host) Uninstall(ctx context.Context, id string) error {
	h.installMu.Lock()
	defer h.installMu.Unlock()

	h.stop(id)
	if err := h.db.Exec(ctx, "DELETE FROM plugin WHERE id = ?", id); err != nil {
		return err
	}
	if err := os.RemoveAll(filepath.Join(h.dir, "installed", id)); err != nil {
		return fmt.Errorf("pluginhost: remove plugin directory: %w", err)
	}
	return nil
}

// SetEnabled toggles a plugin without removing it: a disabled plugin is not
// started, and its sources report no items.
func (h *Host) SetEnabled(ctx context.Context, id string, enabled bool) error {
	if _, err := h.Installed(ctx, id); err != nil {
		return err
	}
	if !enabled {
		h.stop(id)
	}
	return h.db.Exec(ctx,
		"UPDATE plugin SET enabled = ?, updated_at = ? WHERE id = ?",
		enabled, time.Now().UnixMilli(), id)
}

// Close stops every running plugin process.
func (h *Host) Close() {
	h.procMu.Lock()
	procs := h.procs
	h.procs = make(map[string]*Process)
	h.procMu.Unlock()
	for _, p := range procs {
		p.Close()
	}
}

// stop closes one plugin's process, if running.
func (h *Host) stop(id string) {
	h.procMu.Lock()
	p := h.procs[id]
	delete(h.procs, id)
	h.procMu.Unlock()
	if p != nil {
		p.Close()
	}
}

// processFor returns the running process for an installed plugin, starting it
// when needed.
func (h *Host) processFor(ctx context.Context, installed domain.Plugin) (*Process, error) {
	h.procMu.Lock()
	defer h.procMu.Unlock()
	if p, ok := h.procs[installed.ID]; ok {
		select {
		case <-p.exited:
			delete(h.procs, installed.ID) // crashed since last use; start a new one
		default:
			return p, nil
		}
	}
	dir := h.InstalledDir(installed.ID, installed.Version)
	manifest, err := readManifest(dir)
	if err != nil {
		return nil, err
	}
	p, err := Start(ctx, dir, manifest)
	if err != nil {
		return nil, err
	}
	h.procs[installed.ID] = p
	return p, nil
}

// ArtifactPath returns a local path holding the artifact's verified bytes,
// downloading it into the cache when it is not there yet. The API's artifact
// endpoint serves this file, so a client installs from the same bytes this host
// would.
func (h *Host) ArtifactPath(ctx context.Context, a Artifact) (string, error) {
	return h.downloadArtifact(ctx, a)
}

// ListSource lists every item behind one plugin-backed media source, paging the
// plugin until it reports no more. It returns catalogsync.Item values so the
// caller's upsert/delete diff is identical to the Jellyfin pull path.
func (h *Host) ListSource(ctx context.Context, ms domain.MediaSource) ([]catalogsync.Item, error) {
	if ms.Type != domain.SourcePlugin {
		return nil, fmt.Errorf("pluginhost: media source %s is not a plugin source", ms.ID)
	}
	if ms.PluginID == nil || *ms.PluginID == "" {
		return nil, fmt.Errorf("pluginhost: media source %s names no plugin", ms.ID)
	}
	installed, err := h.Installed(ctx, *ms.PluginID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("%w: %s", ErrNotInstalled, *ms.PluginID)
		}
		return nil, err
	}
	if !installed.Enabled {
		return nil, fmt.Errorf("%w: %s is disabled", ErrNotInstalled, installed.ID)
	}
	if min := ms.PluginMinVersion; min != nil && *min != "" && compareVersions(installed.Version, *min) < 0 {
		return nil, fmt.Errorf("pluginhost: %s %s is older than the source's minimum %s",
			installed.ID, installed.Version, *min)
	}
	if !hasCapability(installed.Capabilities, CapabilityCatalogList) {
		return nil, fmt.Errorf("pluginhost: %s does not provide %s", installed.ID, CapabilityCatalogList)
	}

	proc, err := h.processFor(ctx, installed)
	if err != nil {
		return nil, err
	}

	config := json.RawMessage("null")
	if ms.PluginConfigJSON != nil && strings.TrimSpace(*ms.PluginConfigJSON) != "" {
		if !json.Valid([]byte(*ms.PluginConfigJSON)) {
			return nil, fmt.Errorf("pluginhost: media source %s has invalid plugin_config_json", ms.ID)
		}
		config = json.RawMessage(*ms.PluginConfigJSON)
	}

	items := make([]catalogsync.Item, 0, 64)
	cursor := ""
	for page := 0; page < catalogMaxPages; page++ {
		var req struct {
			Config json.RawMessage `json:"config"`
			Cursor string          `json:"cursor"`
			Limit  int             `json:"limit"`
		}
		req.Config, req.Cursor, req.Limit = config, cursor, catalogPageLimit

		var resp struct {
			Items      []wireItem `json:"items"`
			NextCursor string     `json:"next_cursor"`
		}
		pageCtx, cancel := context.WithTimeout(ctx, catalogPageTimeout)
		err := proc.Call(pageCtx, "catalog.list", req, &resp)
		cancel()
		if err != nil {
			return nil, fmt.Errorf("pluginhost: %s catalog.list: %w", installed.ID, err)
		}
		if len(resp.Items) == 0 && resp.NextCursor != "" {
			return nil, fmt.Errorf("pluginhost: %s returned an empty page with a cursor", installed.ID)
		}
		for _, wi := range resp.Items {
			item, ok := wi.toCatalogItem()
			if !ok {
				logging.Warnf("pluginhost: %s: skipping invalid item %q", installed.ID, wi.ExternalID)
				continue
			}
			items = append(items, item)
		}
		if resp.NextCursor == "" {
			return items, nil
		}
		cursor = resp.NextCursor
	}
	return items, nil
}

// Resolve asks a plugin for something a player can open
// (docs/PLUGINS.md "media.resolve").
//
// [modes] is what the caller's player can actually play, cheapest first. The
// answer names the mode the plugin used, and its url is the plugin's own: a
// plugin running on this machine names 127.0.0.1, so a host that serves a player
// elsewhere rewrites the host part and leaves the rest alone (the port and the
// token are the plugin's).
func (h *Host) Resolve(ctx context.Context, pluginID string, config json.RawMessage,
	externalID string, durationMs, startAtMs int64, modes []string) (json.RawMessage, error) {
	installed, err := h.ResolveCapable(ctx, pluginID)
	if err != nil {
		return nil, err
	}
	proc, err := h.processFor(ctx, installed)
	if err != nil {
		return nil, err
	}
	if len(modes) == 0 {
		modes = []string{ModeSingle, ModePair}
	}
	var req struct {
		Config json.RawMessage `json:"config"`
		Item   struct {
			ExternalID string `json:"external_id"`
			DurationMs int64  `json:"duration_ms,omitempty"`
		} `json:"item"`
		StartAtMs int64    `json:"start_at_ms,omitempty"`
		Modes     []string `json:"modes"`
	}
	req.Config = config
	req.Item.ExternalID = externalID
	req.Item.DurationMs = durationMs
	req.StartAtMs = startAtMs
	req.Modes = modes

	var resp json.RawMessage
	callCtx, cancel := context.WithTimeout(ctx, resolveTimeout)
	defer cancel()
	if err := proc.Call(callCtx, "media.resolve", req, &resp); err != nil {
		return nil, fmt.Errorf("pluginhost: %s media.resolve: %w", installed.ID, err)
	}
	if !json.Valid(resp) {
		return nil, fmt.Errorf("pluginhost: %s media.resolve returned invalid JSON", installed.ID)
	}
	return resp, nil
}

// ResolveCapable returns the installed plugin, refusing one that cannot resolve
// playback (missing, disabled, too old, or without the capability).
func (h *Host) ResolveCapable(ctx context.Context, pluginID string) (domain.Plugin, error) {
	if pluginID == "" {
		return domain.Plugin{}, fmt.Errorf("pluginhost: no plugin named")
	}
	installed, err := h.Installed(ctx, pluginID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.Plugin{}, fmt.Errorf("%w: %s", ErrNotInstalled, pluginID)
		}
		return domain.Plugin{}, err
	}
	if !installed.Enabled {
		return domain.Plugin{}, fmt.Errorf("%w: %s is disabled", ErrNotInstalled, installed.ID)
	}
	if !hasCapability(installed.Capabilities, CapabilityMediaResolve) {
		return domain.Plugin{}, fmt.Errorf("pluginhost: %s does not provide %s", installed.ID, CapabilityMediaResolve)
	}
	return installed, nil
}

// wireItem is the catalog item shape a plugin returns, identical to the push
// sync payload (internal/api's catalogSyncItem) so both directions agree.
type wireItem struct {
	ExternalID       string  `json:"external_id"`
	Kind             string  `json:"kind"`
	Title            string  `json:"title"`
	SeriesTitle      *string `json:"series_title"`
	DurationMs       int64   `json:"duration_ms"`
	SourceModifiedAt *int64  `json:"source_modified_at"`
	Genres           string  `json:"genres"`
	ProductionYear   *int64  `json:"production_year"`
	domain.CatalogMetadata
}

func (w wireItem) toCatalogItem() (catalogsync.Item, bool) {
	if strings.TrimSpace(w.ExternalID) == "" || strings.TrimSpace(w.Title) == "" || w.DurationMs <= 0 {
		return catalogsync.Item{}, false
	}
	kind := domain.CatalogItemKind(w.Kind)
	switch kind {
	case domain.CatalogMovie, domain.CatalogEpisode, domain.CatalogOther:
	case "":
		kind = domain.CatalogOther
	default:
		return catalogsync.Item{}, false
	}
	item := catalogsync.Item{
		ExternalID:       w.ExternalID,
		Kind:             kind,
		Title:            w.Title,
		DurationMs:       w.DurationMs,
		SourceModifiedAt: w.SourceModifiedAt,
		Genres:           w.Genres,
		ProductionYear:   w.ProductionYear,
		CatalogMetadata:  w.CatalogMetadata,
	}
	if kind == domain.CatalogEpisode {
		item.SeriesTitle = w.SeriesTitle
	}
	return item, true
}

// pluginColumns pins the column order the scanner expects for domain.Plugin.
const pluginColumns = "id, version, protocol, os, arch, artifact_sha256, entrypoint, capabilities, enabled, installed_at, updated_at"
