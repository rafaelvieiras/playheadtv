package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rafaelvieiras/playheadtv/backend/internal/crypto"
	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
	"github.com/rafaelvieiras/playheadtv/backend/internal/pluginhost"
	"github.com/rafaelvieiras/playheadtv/backend/internal/storage"
)

// mediaSourceColumns mirrors internal/api/media_sources.go's
// mediaSourceColumns constant — duplicated rather than imported, see
// mcpserver.go's package comment.
const mediaSourceColumns = "id, tenant_id, type, name, base_url, credential_cipher, sync_mode, plugin_id, plugin_min_version, plugin_config_json, last_probe_at, last_probe_ok, last_synced_at, created_at, updated_at"

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
		Description: "Register a new media source (a Jellyfin/Plex server, a local folder, or a plugin-backed source) that " +
			"catalog items get synced from. For type=jellyfin, this runs a real connectivity probe " +
			"against base_url (GET /System/Info/Public, 5s timeout) to decide sync_mode pull/push. " +
			"Mirrors POST /media-sources.",
	}, registerMediaSourceHandler(db, encKey))
}

type registerMediaSourceInput struct {
	Type             string         `json:"type" jsonschema:"one of: jellyfin, plex, local_folder, plugin"`
	Name             string         `json:"name" jsonschema:"media source display name"`
	BaseURL          *string        `json:"base_url,omitempty" jsonschema:"base URL of the server; required unless type is local_folder or plugin"`
	Credential       *string        `json:"credential,omitempty" jsonschema:"optional API key/token, encrypted at rest before storage"`
	PluginID         *string        `json:"plugin_id,omitempty" jsonschema:"only for type plugin: which installed plugin lists this source (see list_plugins)"`
	PluginMinVersion *string        `json:"plugin_min_version,omitempty" jsonschema:"only for type plugin: oldest plugin version that can read plugin_config"`
	PluginConfig     map[string]any `json:"plugin_config,omitempty" jsonschema:"only for type plugin: the plugin's own configuration, handed back to it verbatim. Plain text: never a secret"`
}

// pluginColumns mirrors internal/api's helper: a plugin source names its plugin
// and stores its own configuration, and no other type carries those fields.
func pluginColumns(inType string, id, minVersion *string, config map[string]any) (pid, pmin, pconfig *string, err error) {
	if domain.SourceType(inType) != domain.SourcePlugin {
		if id != nil || minVersion != nil || len(config) > 0 {
			return nil, nil, nil, errors.New("plugin_id, plugin_min_version and plugin_config are only valid for type plugin")
		}
		return nil, nil, nil, nil
	}
	if id == nil || !pluginhost.ValidID(*id) {
		return nil, nil, nil, errors.New("plugin_id is required for type plugin and must be a lowercase id")
	}
	if minVersion != nil && *minVersion != "" {
		if !pluginhost.ValidVersion(*minVersion) {
			return nil, nil, nil, errors.New("plugin_min_version must look like a version (e.g. 1.4.0 or 2026.10.26)")
		}
		pmin = minVersion
	}
	if len(config) > 0 {
		raw, err := json.Marshal(config)
		if err != nil {
			return nil, nil, nil, err
		}
		v := string(raw)
		pconfig = &v
	}
	return id, pmin, pconfig, nil
}

func registerMediaSourceHandler(db storage.DB, encKey [32]byte) mcp.ToolHandlerFor[registerMediaSourceInput, domain.MediaSource] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in registerMediaSourceInput) (*mcp.CallToolResult, domain.MediaSource, error) {
		if in.Name == "" {
			return nil, domain.MediaSource{}, errors.New("name is required")
		}
		switch domain.SourceType(in.Type) {
		case domain.SourceJellyfin, domain.SourcePlex, domain.SourceLocalFolder, domain.SourcePlugin:
		default:
			return nil, domain.MediaSource{}, errors.New("type must be one of jellyfin, plex, local_folder, plugin")
		}
		isLocalFolder := domain.SourceType(in.Type) == domain.SourceLocalFolder
		isPlugin := domain.SourceType(in.Type) == domain.SourcePlugin
		if !isLocalFolder && !isPlugin && (in.BaseURL == nil || *in.BaseURL == "") {
			return nil, domain.MediaSource{}, errors.New("base_url is required unless type is local_folder or plugin")
		}
		pluginID, pluginMinVersion, pluginConfig, err := pluginColumns(in.Type, in.PluginID, in.PluginMinVersion, in.PluginConfig)
		if err != nil {
			return nil, domain.MediaSource{}, err
		}

		now := nowMillis()
		ms := domain.MediaSource{
			ID:               uuid.NewString(),
			TenantID:         defaultTenantID,
			Type:             domain.SourceType(in.Type),
			Name:             in.Name,
			BaseURL:          in.BaseURL,
			SyncMode:         domain.SyncPush,
			PluginID:         pluginID,
			PluginMinVersion: pluginMinVersion,
			PluginConfigJSON: pluginConfig,
			CreatedAt:        now,
			UpdatedAt:        now,
		}
		if in.Credential != nil {
			sealed, err := crypto.Encrypt(encKey, []byte(*in.Credential))
			if err != nil {
				return nil, domain.MediaSource{}, err
			}
			ms.CredentialCipher = sealed
		}

		switch domain.SourceType(in.Type) {
		case domain.SourceJellyfin:
			ok := probeJellyfin(*in.BaseURL, probeClient)
			ms.LastProbeAt = &now
			ms.LastProbeOK = &ok
			if ok {
				ms.SyncMode = domain.SyncPull
			}
		case domain.SourcePlugin:
			// Pulled by this backend through the plugin, with no server probe.
			ms.SyncMode = domain.SyncPull
		}

		err = db.Exec(ctx,
			`INSERT INTO media_source (id, tenant_id, type, name, base_url, credential_cipher, sync_mode,
			  plugin_id, plugin_min_version, plugin_config_json, last_probe_at, last_probe_ok, last_synced_at, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			ms.ID, ms.TenantID, string(ms.Type), ms.Name, ms.BaseURL, ms.CredentialCipher, string(ms.SyncMode),
			ms.PluginID, ms.PluginMinVersion, ms.PluginConfigJSON,
			ms.LastProbeAt, ms.LastProbeOK, ms.LastSyncedAt, ms.CreatedAt, ms.UpdatedAt,
		)
		if err != nil {
			return nil, domain.MediaSource{}, err
		}
		return nil, ms, nil
	}
}
