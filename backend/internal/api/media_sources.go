package api

import (
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/rafaelvieiras/playheadtv/backend/internal/catalogsync"
	"github.com/rafaelvieiras/playheadtv/backend/internal/crypto"
	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
	"github.com/rafaelvieiras/playheadtv/backend/internal/materializer"
	"github.com/rafaelvieiras/playheadtv/backend/internal/pullsync"
	"github.com/rafaelvieiras/playheadtv/backend/internal/storage"
)

func registerMediaSources(mux Router, db storage.DB, mat *materializer.Materializer, sync *pullsync.Syncer, encKey [32]byte) {
	mux.HandleFunc("POST /media-sources", createMediaSource(db, sync, encKey))
	mux.HandleFunc("GET /media-sources", listMediaSources(db))
	mux.HandleFunc("GET /media-sources/{id}", getMediaSource(db))
	mux.HandleFunc("PUT /media-sources/{id}", updateMediaSource(db, sync, encKey))
	mux.HandleFunc("DELETE /media-sources/{id}", deleteMediaSource(db))
	mux.HandleFunc("POST /media-sources/{id}/catalog-sync", catalogSync(db, mat))
	mux.HandleFunc("POST /media-sources/{id}/pull-sync", pullSyncHandler(db, sync))
}

const mediaSourceColumns = "id, tenant_id, type, name, base_url, credential_cipher, sync_mode, last_probe_at, last_probe_ok, last_synced_at, created_at, updated_at"

// probeClient is a short-timeout HTTP client for the real registration
// probe; tests inject their own client/base URL instead of relying on it.
var probeClient = &http.Client{Timeout: 5 * time.Second}

// probeJellyfin checks whether baseURL's public Jellyfin endpoint answers
// 200 OK. Any error, timeout, or non-200 status counts as unreachable —
// callers fall back to sync_mode "push" in that case.
func probeJellyfin(baseURL string, client *http.Client) bool {
	url := strings.TrimRight(baseURL, "/") + "/System/Info/Public"
	resp, err := client.Get(url)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

type mediaSourceRequest struct {
	Type       string  `json:"type"`
	Name       string  `json:"name"`
	BaseURL    *string `json:"base_url"`
	Credential *string `json:"credential"`
}

func fetchMediaSource(db storage.DB, r *http.Request, id string) (domain.MediaSource, error) {
	var ms domain.MediaSource
	err := db.Query(r.Context(), &ms,
		"SELECT "+mediaSourceColumns+" FROM media_source WHERE tenant_id = ? AND id = ?",
		DefaultTenantID, id,
	)
	return ms, err
}

func createMediaSource(db storage.DB, sync *pullsync.Syncer, encKey [32]byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req mediaSourceRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if req.Name == "" {
			writeError(w, http.StatusBadRequest, "name is required")
			return
		}
		switch domain.SourceType(req.Type) {
		case domain.SourceJellyfin, domain.SourcePlex, domain.SourceLocalFolder:
		default:
			writeError(w, http.StatusBadRequest, "type must be one of jellyfin, plex, local_folder")
			return
		}
		isLocalFolder := domain.SourceType(req.Type) == domain.SourceLocalFolder
		if !isLocalFolder && (req.BaseURL == nil || *req.BaseURL == "") {
			writeError(w, http.StatusBadRequest, "base_url is required unless type is local_folder")
			return
		}

		now := time.Now().UnixMilli()
		ms := domain.MediaSource{
			ID:        uuid.NewString(),
			TenantID:  DefaultTenantID,
			Type:      domain.SourceType(req.Type),
			Name:      req.Name,
			BaseURL:   req.BaseURL,
			SyncMode:  domain.SyncPush,
			CreatedAt: now,
			UpdatedAt: now,
		}
		if req.Credential != nil {
			sealed, err := crypto.Encrypt(encKey, []byte(*req.Credential))
			if err != nil {
				log.Printf("api: encrypt media source credential: %v", err)
				writeError(w, http.StatusInternalServerError, "internal error")
				return
			}
			ms.CredentialCipher = sealed
		}

		if domain.SourceType(req.Type) == domain.SourceJellyfin {
			// Only Jellyfin has a real probe endpoint; Plex isn't implemented
			// in v1 (see domain.SourcePlex) so it stays push without one.
			ok := probeJellyfin(*req.BaseURL, probeClient)
			ms.LastProbeAt = &now
			ms.LastProbeOK = &ok
			if ok {
				ms.SyncMode = domain.SyncPull
			}
		}

		err := db.Exec(r.Context(),
			`INSERT INTO media_source (id, tenant_id, type, name, base_url, credential_cipher, sync_mode, last_probe_at, last_probe_ok, last_synced_at, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			ms.ID, ms.TenantID, string(ms.Type), ms.Name, ms.BaseURL, ms.CredentialCipher, string(ms.SyncMode),
			ms.LastProbeAt, ms.LastProbeOK, ms.LastSyncedAt, ms.CreatedAt, ms.UpdatedAt,
		)
		if err != nil {
			handleDBError(w, err)
			return
		}
		if ms.Type == domain.SourceJellyfin && ms.SyncMode == domain.SyncPull {
			// Non-blocking: populates catalog_item in the background within
			// seconds, without holding this create request open for however
			// long a full Jellyfin library listing takes.
			sync.Trigger()
		}
		writeJSON(w, http.StatusCreated, ms)
	}
}

func listMediaSources(db storage.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var sources []domain.MediaSource
		err := db.Query(r.Context(), &sources,
			"SELECT "+mediaSourceColumns+" FROM media_source WHERE tenant_id = ? ORDER BY created_at",
			DefaultTenantID,
		)
		if err != nil {
			handleDBError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, sources)
	}
}

func getMediaSource(db storage.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ms, err := fetchMediaSource(db, r, r.PathValue("id"))
		if err != nil {
			handleDBError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, ms)
	}
}

func updateMediaSource(db storage.DB, sync *pullsync.Syncer, encKey [32]byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		existing, err := fetchMediaSource(db, r, id)
		if err != nil {
			handleDBError(w, err)
			return
		}

		var req mediaSourceRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if req.Name == "" {
			writeError(w, http.StatusBadRequest, "name is required")
			return
		}
		switch domain.SourceType(req.Type) {
		case domain.SourceJellyfin, domain.SourcePlex, domain.SourceLocalFolder:
		default:
			writeError(w, http.StatusBadRequest, "type must be one of jellyfin, plex, local_folder")
			return
		}
		isLocalFolder := domain.SourceType(req.Type) == domain.SourceLocalFolder
		if !isLocalFolder && (req.BaseURL == nil || *req.BaseURL == "") {
			writeError(w, http.StatusBadRequest, "base_url is required unless type is local_folder")
			return
		}

		now := time.Now().UnixMilli()
		ms := domain.MediaSource{
			ID:               id,
			TenantID:         DefaultTenantID,
			Type:             domain.SourceType(req.Type),
			Name:             req.Name,
			BaseURL:          req.BaseURL,
			CredentialCipher: existing.CredentialCipher,
			SyncMode:         existing.SyncMode,
			LastProbeAt:      existing.LastProbeAt,
			LastProbeOK:      existing.LastProbeOK,
			LastSyncedAt:     existing.LastSyncedAt,
			CreatedAt:        existing.CreatedAt,
			UpdatedAt:        now,
		}
		if req.Credential != nil {
			sealed, err := crypto.Encrypt(encKey, []byte(*req.Credential))
			if err != nil {
				log.Printf("api: encrypt media source credential: %v", err)
				writeError(w, http.StatusInternalServerError, "internal error")
				return
			}
			ms.CredentialCipher = sealed
		}

		baseURLChanged := (existing.BaseURL == nil) != (req.BaseURL == nil) ||
			(existing.BaseURL != nil && req.BaseURL != nil && *existing.BaseURL != *req.BaseURL)

		switch {
		case isLocalFolder:
			ms.SyncMode = domain.SyncPush
			ms.LastProbeAt = nil
			ms.LastProbeOK = nil
		case domain.SourceType(req.Type) == domain.SourceJellyfin && (baseURLChanged || existing.Type != domain.SourceJellyfin):
			// Only Jellyfin has a real probe endpoint (see the analogous
			// comment in createMediaSource); also re-probe when switching
			// into jellyfin from another type even if the URL string is
			// unchanged, since there's no prior Jellyfin probe to trust.
			ok := probeJellyfin(*req.BaseURL, probeClient)
			ms.LastProbeAt = &now
			ms.LastProbeOK = &ok
			ms.SyncMode = domain.SyncPush
			if ok {
				ms.SyncMode = domain.SyncPull
			}
		case domain.SourceType(req.Type) == domain.SourcePlex:
			ms.SyncMode = domain.SyncPush
			ms.LastProbeAt = nil
			ms.LastProbeOK = nil
		}

		err = db.Exec(r.Context(),
			`UPDATE media_source SET type = ?, name = ?, base_url = ?, credential_cipher = ?, sync_mode = ?,
			 last_probe_at = ?, last_probe_ok = ?, updated_at = ? WHERE tenant_id = ? AND id = ?`,
			string(ms.Type), ms.Name, ms.BaseURL, ms.CredentialCipher, string(ms.SyncMode),
			ms.LastProbeAt, ms.LastProbeOK, ms.UpdatedAt, DefaultTenantID, id,
		)
		if err != nil {
			handleDBError(w, err)
			return
		}
		if ms.Type == domain.SourceJellyfin && ms.SyncMode == domain.SyncPull {
			// Same rationale as createMediaSource's trigger: covers both a
			// source that just became pull and one that already was, in
			// case its name/URL edit is otherwise never followed by a sync.
			sync.Trigger()
		}
		writeJSON(w, http.StatusOK, ms)
	}
}

func deleteMediaSource(db storage.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if _, err := fetchMediaSource(db, r, id); err != nil {
			handleDBError(w, err)
			return
		}
		if err := db.Exec(r.Context(), "DELETE FROM media_source WHERE tenant_id = ? AND id = ?", DefaultTenantID, id); err != nil {
			handleDBError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

type catalogSyncItem struct {
	ExternalID       string  `json:"external_id"`
	Kind             string  `json:"kind"`
	Title            string  `json:"title"`
	SeriesTitle      *string `json:"series_title"`
	DurationMs       int64   `json:"duration_ms"`
	SourceModifiedAt *int64  `json:"source_modified_at"`
	Genres           string  `json:"genres"` // comma-separated, as stored and returned
	ProductionYear   *int64  `json:"production_year"`

	// The same optional metadata GET /catalog-items returns. At most
	// catalogsync.MaxPeople people (directors first, then cast) are kept.
	domain.CatalogMetadata
}

type catalogSyncRequest struct {
	Items              []catalogSyncItem `json:"items"`
	DeletedExternalIDs []string          `json:"deleted_external_ids"`
}

func catalogSync(db storage.DB, mat *materializer.Materializer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		ms, err := fetchMediaSource(db, r, id)
		if err != nil {
			handleDBError(w, err)
			return
		}
		if ms.SyncMode != domain.SyncPush {
			writeError(w, http.StatusConflict, "catalog-sync is only allowed for push-mode media sources")
			return
		}

		var req catalogSyncRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}

		now := time.Now().UnixMilli()
		for _, item := range req.Items {
			if item.ExternalID == "" || item.Title == "" {
				writeError(w, http.StatusBadRequest, "each item requires external_id and title")
				return
			}
			kind := domain.CatalogItemKind(item.Kind)
			switch kind {
			case domain.CatalogMovie, domain.CatalogEpisode, domain.CatalogOther:
			default:
				writeError(w, http.StatusBadRequest, "kind must be one of movie, episode, other")
				return
			}
			err := catalogsync.Upsert(r.Context(), db, DefaultTenantID, id, catalogsync.Item{
				ExternalID:       item.ExternalID,
				Kind:             kind,
				Title:            item.Title,
				SeriesTitle:      item.SeriesTitle,
				DurationMs:       item.DurationMs,
				SourceModifiedAt: item.SourceModifiedAt,
				Genres:           item.Genres,
				ProductionYear:   item.ProductionYear,
				CatalogMetadata:  item.CatalogMetadata,
			}, now)
			if err != nil {
				handleDBError(w, err)
				return
			}
		}
		for _, externalID := range req.DeletedExternalIDs {
			if err := catalogsync.Delete(r.Context(), db, id, externalID); err != nil {
				handleDBError(w, err)
				return
			}
		}

		if err := catalogsync.TouchLastSynced(r.Context(), db, DefaultTenantID, id, now); err != nil {
			handleDBError(w, err)
			return
		}

		if err := catalogsync.ResolveDynamic(r.Context(), db, DefaultTenantID); err != nil {
			handleDBError(w, err)
			return
		}
		mat.Trigger()

		ms, err = fetchMediaSource(db, r, id)
		if err != nil {
			handleDBError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, ms)
	}
}

type pullSyncResponse struct {
	MediaSource domain.MediaSource  `json:"media_source"`
	Result      pullsync.SyncResult `json:"result"`
}

// pullSyncHandler triggers a synchronous pull sync for one media source —
// the mirror image of catalogSync's guard: rejects anything that ISN'T a
// jellyfin source in pull mode (409), instead of catalogSync's "isn't
// push."
func pullSyncHandler(db storage.DB, sync *pullsync.Syncer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		ms, err := fetchMediaSource(db, r, id)
		if err != nil {
			handleDBError(w, err)
			return
		}
		if ms.Type != domain.SourceJellyfin || ms.SyncMode != domain.SyncPull {
			writeError(w, http.StatusConflict, "pull-sync is only allowed for jellyfin sources in pull mode")
			return
		}

		result, err := sync.SyncOne(r.Context(), ms)
		if err != nil {
			log.Printf("api: pull-sync media source %s: %v", id, err)
			writeError(w, http.StatusBadGateway, "failed to sync with jellyfin: "+err.Error())
			return
		}

		ms, err = fetchMediaSource(db, r, id) // refetch: last_synced_at/updated_at just changed
		if err != nil {
			handleDBError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, pullSyncResponse{MediaSource: ms, Result: result})
	}
}
