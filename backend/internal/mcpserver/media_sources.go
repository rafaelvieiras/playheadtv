package mcpserver

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rafaelvieiras/playheadtv/backend/internal/crypto"
	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
	"github.com/rafaelvieiras/playheadtv/backend/internal/storage"
)

// mediaSourceColumns mirrors internal/api/media_sources.go's
// mediaSourceColumns constant — duplicated rather than imported, see
// mcpserver.go's package comment.
const mediaSourceColumns = "id, tenant_id, type, name, base_url, credential_cipher, sync_mode, last_probe_at, last_probe_ok, last_synced_at, created_at, updated_at"

// probeClient is a short-timeout HTTP client for the registration probe,
// mirroring internal/api/media_sources.go's probeClient.
var probeClient = &http.Client{Timeout: probeTimeout}

// probeJellyfin mirrors internal/api/media_sources.go's probeJellyfin: any
// error, timeout, or non-200 status counts as unreachable.
func probeJellyfin(baseURL string, client *http.Client) bool {
	url := strings.TrimRight(baseURL, "/") + "/System/Info/Public"
	resp, err := client.Get(url)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func registerMediaSourceTools(s *mcp.Server, db storage.DB, encKey [32]byte) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "register_media_source",
		Description: "Register a new media source (a Jellyfin/Plex server or a local folder) that " +
			"catalog items get synced from. For type=jellyfin, this runs a real connectivity probe " +
			"against base_url (GET /System/Info/Public, 5s timeout) to decide sync_mode pull/push. " +
			"Mirrors POST /media-sources.",
	}, registerMediaSourceHandler(db, encKey))
}

type registerMediaSourceInput struct {
	Type       string  `json:"type" jsonschema:"one of: jellyfin, plex, local_folder"`
	Name       string  `json:"name" jsonschema:"media source display name"`
	BaseURL    *string `json:"base_url,omitempty" jsonschema:"base URL of the server; required unless type is local_folder"`
	Credential *string `json:"credential,omitempty" jsonschema:"optional API key/token, encrypted at rest before storage"`
}

func registerMediaSourceHandler(db storage.DB, encKey [32]byte) mcp.ToolHandlerFor[registerMediaSourceInput, domain.MediaSource] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in registerMediaSourceInput) (*mcp.CallToolResult, domain.MediaSource, error) {
		if in.Name == "" {
			return nil, domain.MediaSource{}, errors.New("name is required")
		}
		switch domain.SourceType(in.Type) {
		case domain.SourceJellyfin, domain.SourcePlex, domain.SourceLocalFolder:
		default:
			return nil, domain.MediaSource{}, errors.New("type must be one of jellyfin, plex, local_folder")
		}
		isLocalFolder := domain.SourceType(in.Type) == domain.SourceLocalFolder
		if !isLocalFolder && (in.BaseURL == nil || *in.BaseURL == "") {
			return nil, domain.MediaSource{}, errors.New("base_url is required unless type is local_folder")
		}

		now := nowMillis()
		ms := domain.MediaSource{
			ID:        uuid.NewString(),
			TenantID:  defaultTenantID,
			Type:      domain.SourceType(in.Type),
			Name:      in.Name,
			BaseURL:   in.BaseURL,
			SyncMode:  domain.SyncPush,
			CreatedAt: now,
			UpdatedAt: now,
		}
		if in.Credential != nil {
			sealed, err := crypto.Encrypt(encKey, []byte(*in.Credential))
			if err != nil {
				return nil, domain.MediaSource{}, err
			}
			ms.CredentialCipher = sealed
		}

		if domain.SourceType(in.Type) == domain.SourceJellyfin {
			ok := probeJellyfin(*in.BaseURL, probeClient)
			ms.LastProbeAt = &now
			ms.LastProbeOK = &ok
			if ok {
				ms.SyncMode = domain.SyncPull
			}
		}

		err := db.Exec(ctx,
			`INSERT INTO media_source (id, tenant_id, type, name, base_url, credential_cipher, sync_mode, last_probe_at, last_probe_ok, last_synced_at, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			ms.ID, ms.TenantID, string(ms.Type), ms.Name, ms.BaseURL, ms.CredentialCipher, string(ms.SyncMode),
			ms.LastProbeAt, ms.LastProbeOK, ms.LastSyncedAt, ms.CreatedAt, ms.UpdatedAt,
		)
		if err != nil {
			return nil, domain.MediaSource{}, err
		}
		return nil, ms, nil
	}
}
