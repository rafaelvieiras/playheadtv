// Package pullsync keeps catalog_item in sync with pull-mode Jellyfin media
// sources by querying the Jellyfin API directly (GET /Items), asynchronously
// and on a light periodic timer — see docs/ARCHITECTURE.md "Reachability:
// pull vs push". Structurally analogous to internal/materializer (background
// goroutine + debounced Trigger + ticker), but scoped to media_source rows
// instead of schedule_entry, and additionally exposes SyncOne for the
// synchronous manual "sync now" HTTP path (internal/api's pull-sync
// handler).
//
// This package does not import internal/api: it discovers tenants
// generically from the tenant table rather than assuming
// api.DefaultTenantID, and duplicates the small mediaSourceColumns constant
// it needs (same pattern internal/materializer already uses for
// scheduleEntryColumns etc.), so it keeps working unchanged once real
// multi-tenancy exists and never creates an import cycle with internal/api
// (which imports this package to wire the manual endpoint).
package pullsync

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/rafaelvieiras/playheadtv/backend/internal/catalogsync"
	"github.com/rafaelvieiras/playheadtv/backend/internal/crypto"
	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
	"github.com/rafaelvieiras/playheadtv/backend/internal/materializer"
	"github.com/rafaelvieiras/playheadtv/backend/internal/storage"
)

// mediaSourceColumns mirrors internal/api/media_sources.go's constant of the
// same name — duplicated rather than imported, see the package doc comment.
const mediaSourceColumns = "id, tenant_id, type, name, base_url, credential_cipher, sync_mode, last_probe_at, last_probe_ok, last_synced_at, created_at, updated_at"

// pullInterval is the "light periodic refresh" docs/ARCHITECTURE.md's pull
// section calls for — fixed, not env-configurable (same rationale as
// materializer.tickInterval: an implementation detail, not something a
// self-hoster should need to tune). Longer than materializer's 5 minutes
// because this is network-bound against a real Jellyfin server, not a local
// recompute — the manual "sync now" endpoint covers "I need this right
// away," so the periodic tick only needs to be "eventually."
const pullInterval = 15 * time.Minute

// triggerDebounce coalesces a burst of Trigger() calls (e.g. a user editing
// a source's name right after registering it) into a single sweep.
const triggerDebounce = 2 * time.Second

// runTimeout bounds one full sweep across every pull-mode Jellyfin source
// for every tenant. Generous vs materializer's 30s because this is
// network-bound (a real Jellyfin round trip per page), not local-DB-bound.
const runTimeout = 2 * time.Minute

// httpTimeout bounds a single HTTP request to one Jellyfin server.
const httpTimeout = 10 * time.Second

// ErrNotPullJellyfin is returned by SyncOne when called with a media source
// that isn't a jellyfin source in pull mode — the single source of truth for
// this guard, so internal/api's manual endpoint doesn't have to duplicate
// the condition informally (it maps this error to 409).
var ErrNotPullJellyfin = errors.New("pullsync: media source is not a jellyfin source in pull mode")

// Syncer owns the background pull-sync loop, mirroring
// materializer.Materializer's shape.
type Syncer struct {
	db      storage.DB
	encKey  [32]byte
	client  *http.Client
	trigger chan struct{}
	mat     *materializer.Materializer
}

// New creates a Syncer. encKey decrypts media_source.credential_cipher —
// the same key cmd/server/main.go resolves via crypto.LoadOrGenerateKey and
// already threads through to api.Register/mcpserver.Register. mat is
// triggered after every SyncOne resolves dynamic filler pools, so a channel
// built on a dynamic pool's fallback picks up newly pulled content without a
// separate manual step — see internal/materializer, no import cycle since it
// only imports domain/storage.
func New(db storage.DB, encKey [32]byte, mat *materializer.Materializer) *Syncer {
	return &Syncer{
		db:      db,
		encKey:  encKey,
		client:  &http.Client{Timeout: httpTimeout},
		trigger: make(chan struct{}, 1),
		mat:     mat,
	}
}

// Trigger asks for a sweep as soon as possible, without blocking the caller
// — identical contract to materializer.Trigger (safe to call before Start;
// buffered cap 1).
func (s *Syncer) Trigger() {
	select {
	case s.trigger <- struct{}{}:
	default:
	}
}

// Start runs Run() once immediately, then again on every debounced Trigger()
// and on a periodic ticker, until ctx is canceled — same loop shape as
// materializer.Start, including "never panics, never log.Fatal's, always
// its own goroutine."
func (s *Syncer) Start(ctx context.Context) {
	go func() {
		s.runLogged(ctx)

		ticker := time.NewTicker(pullInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-s.trigger:
				select {
				case <-time.After(triggerDebounce):
				case <-ctx.Done():
					return
				}
				s.drainTrigger()
				s.runLogged(ctx)
			case <-ticker.C:
				s.runLogged(ctx)
			}
		}
	}()
}

// drainTrigger discards any Trigger() signals that arrived during the
// debounce wait, so a burst of writes collapses into the one Run() that
// follows instead of one per signal.
func (s *Syncer) drainTrigger() {
	for {
		select {
		case <-s.trigger:
		default:
			return
		}
	}
}

func (s *Syncer) runLogged(ctx context.Context) {
	runCtx, cancel := context.WithTimeout(ctx, runTimeout)
	defer cancel()
	if err := s.Run(runCtx); err != nil {
		log.Printf("pullsync: run failed: %v", err)
	}
}

// Run sweeps every tenant's pull-mode jellyfin media sources once. Only a
// failure to list tenants is returned as an error; a failure syncing one
// source is logged and does not stop the rest (same tradeoff as
// materializer.Run's per-channel error isolation).
func (s *Syncer) Run(ctx context.Context) error {
	var tenantIDs []string
	if err := s.db.Query(ctx, &tenantIDs, "SELECT id FROM tenant"); err != nil {
		return fmt.Errorf("pullsync: list tenants: %w", err)
	}
	for _, tenantID := range tenantIDs {
		s.runTenant(ctx, tenantID)
	}
	return nil
}

func (s *Syncer) runTenant(ctx context.Context, tenantID string) {
	var sources []domain.MediaSource
	if err := s.db.Query(ctx, &sources,
		"SELECT "+mediaSourceColumns+" FROM media_source WHERE tenant_id = ? AND type = ? AND sync_mode = ?",
		tenantID, string(domain.SourceJellyfin), string(domain.SyncPull),
	); err != nil {
		log.Printf("pullsync: tenant %s: list media sources: %v", tenantID, err)
		return
	}
	for _, ms := range sources {
		if _, err := s.SyncOne(ctx, ms); err != nil {
			log.Printf("pullsync: media source %s (%s): %v", ms.ID, ms.Name, err)
		}
	}
}

// SyncResult is returned to the manual "sync now" HTTP handler — a compact
// summary, not the full catalog (GET /catalog-items already serves that).
type SyncResult struct {
	MediaSourceID string `json:"media_source_id"`
	ItemsUpserted int    `json:"items_upserted"`
	ItemsDeleted  int    `json:"items_deleted"`
	SyncedAt      int64  `json:"synced_at"`
}

// SyncOne synchronizes exactly one media source, synchronously, and returns
// a result. This is what POST /media-sources/{id}/pull-sync calls directly
// (an HTTP caller sees real success/failure, not fire-and-forget); Run()
// also calls it once per eligible source, swallowing its error into a log
// line as shown above.
//
// Each call fetches the source's ENTIRE remote catalog and diffs it against
// what's already in catalog_item for this media_source_id: anything no
// longer present remotely is deleted. There's no incremental/checkpoint
// tracking (unlike the push flow's DateModified-based diffing on the
// client) — a self-hosted Jellyfin library is small enough that a full
// re-list on every sync is simpler and always correct, at the cost of being
// less efficient than a real incremental sync would be for a very large
// library.
func (s *Syncer) SyncOne(ctx context.Context, ms domain.MediaSource) (SyncResult, error) {
	if ms.Type != domain.SourceJellyfin || ms.SyncMode != domain.SyncPull {
		return SyncResult{}, ErrNotPullJellyfin
	}
	if ms.BaseURL == nil || *ms.BaseURL == "" {
		return SyncResult{}, fmt.Errorf("pullsync: media source %s has no base_url", ms.ID)
	}
	apiKey, err := s.decryptCredential(ms)
	if err != nil {
		return SyncResult{}, err
	}

	remote, err := fetchAllItems(ctx, s.client, *ms.BaseURL, apiKey)
	if err != nil {
		if errors.Is(err, ErrJellyfinAuth) {
			return SyncResult{}, fmt.Errorf("pullsync: %w", err)
		}
		return SyncResult{}, fmt.Errorf("pullsync: fetch jellyfin items: %w", err)
	}

	var existingExternalIDs []string
	if err := s.db.Query(ctx, &existingExternalIDs,
		"SELECT external_id FROM catalog_item WHERE media_source_id = ?", ms.ID,
	); err != nil {
		return SyncResult{}, fmt.Errorf("pullsync: list existing catalog_item: %w", err)
	}

	// Collect each episode's unique SeriesId up front so genre and other
	// series-level metadata for episodes can be resolved with one request per
	// series, not one per episode — see fetchSeriesInfo's doc comment.
	var episodeSeriesIDs []string
	seriesIDSeen := make(map[string]bool)
	for _, ri := range remote {
		if ri.Type == "Episode" && ri.SeriesId != nil && !seriesIDSeen[*ri.SeriesId] {
			seriesIDSeen[*ri.SeriesId] = true
			episodeSeriesIDs = append(episodeSeriesIDs, *ri.SeriesId)
		}
	}
	series := fetchSeriesInfo(ctx, s.client, *ms.BaseURL, apiKey, episodeSeriesIDs)

	now := time.Now().UnixMilli()
	segments := s.resolveSegments(ctx, ms, apiKey, remote, now)
	seen := make(map[string]struct{}, len(remote))
	upserted := 0
	for _, ri := range remote {
		item, ok := mapJellyfinItem(ri, series)
		if !ok {
			continue // unsupported/malformed item — skip, don't fail the whole sync
		}
		seen[item.ExternalID] = struct{}{}
		item.Segments = segments[item.ExternalID]
		if err := catalogsync.Upsert(ctx, s.db, ms.TenantID, ms.ID, item, now); err != nil {
			return SyncResult{}, fmt.Errorf("pullsync: upsert %s: %w", item.ExternalID, err)
		}
		upserted++
	}

	deleted := 0
	for _, extID := range existingExternalIDs {
		if _, ok := seen[extID]; !ok {
			if err := catalogsync.Delete(ctx, s.db, ms.ID, extID); err != nil {
				return SyncResult{}, fmt.Errorf("pullsync: delete %s: %w", extID, err)
			}
			deleted++
		}
	}

	if err := catalogsync.TouchLastSynced(ctx, s.db, ms.TenantID, ms.ID, now); err != nil {
		return SyncResult{}, err
	}

	if err := catalogsync.ResolveDynamic(ctx, s.db, ms.TenantID); err != nil {
		return SyncResult{}, fmt.Errorf("pullsync: resolve dynamic pools/blocks: %w", err)
	}
	s.mat.Trigger()

	return SyncResult{MediaSourceID: ms.ID, ItemsUpserted: upserted, ItemsDeleted: deleted, SyncedAt: now}, nil
}

// decryptCredential is the first production call site of crypto.Decrypt —
// every other reference to it in the repo is a test proving the
// encrypt/decrypt round trip. This is the one place the Jellyfin API key
// ever exists in plaintext in process memory, and only for the duration of
// building the outgoing request.
func (s *Syncer) decryptCredential(ms domain.MediaSource) (string, error) {
	if len(ms.CredentialCipher) == 0 {
		return "", fmt.Errorf("pullsync: media source %s has no credential configured", ms.ID)
	}
	plain, err := crypto.Decrypt(s.encKey, ms.CredentialCipher)
	if err != nil {
		return "", fmt.Errorf("pullsync: decrypt credential: %w", err)
	}
	return string(plain), nil
}
