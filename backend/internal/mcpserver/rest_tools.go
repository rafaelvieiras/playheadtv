package mcpserver

import (
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rafaelvieiras/playheadtv/backend/internal/api"
	"github.com/rafaelvieiras/playheadtv/backend/internal/breaktemplate"
	"github.com/rafaelvieiras/playheadtv/backend/internal/cutruleset"
	"github.com/rafaelvieiras/playheadtv/backend/internal/storage"
)

// Argument shapes shared by the REST-backed tools below.
type (
	noArgs struct{}

	idArgs struct {
		ID string `json:"id" jsonschema:"the id of the resource"`
	}
	idConfirmArgs struct {
		ID string `json:"id" jsonschema:"the id of the resource"`
		Confirmation
	}

	channelArgs struct {
		ID             string  `json:"id" jsonschema:"the channel to replace"`
		Number         int     `json:"number" jsonschema:"channel number shown in the guide"`
		Name           string  `json:"name" jsonschema:"channel display name"`
		GuideArtURL    *string `json:"guide_art_url,omitempty" jsonschema:"guide artwork/logo URL; omitted clears it"`
		Timezone       string  `json:"timezone,omitempty" jsonschema:"IANA timezone name; UTC when omitted"`
		ScheduleID     *string `json:"schedule_id,omitempty" jsonschema:"the Schedule the channel airs; omitted clears it. Changing it resets the EPG grid"`
		BaseScheduleID *string `json:"base_schedule_id,omitempty" jsonschema:"the base Schedule that plays in every gap schedule_id leaves; omitted clears it. Must differ from schedule_id. Changing it resets the EPG grid"`
	}

	// watermarkArgs carries a channel's or block's watermark (id is the owner).
	jellyfinServerConfigArgs struct {
		BaseURL string `json:"base_url" jsonschema:"the Jellyfin server address, http or https, e.g. https://jellyfin.example.com"`
		APIKey  string `json:"api_key,omitempty" jsonschema:"the Jellyfin API key (write-only); omitted keeps the stored key"`
	}
	watermarkArgs struct {
		ID          string  `json:"id" jsonschema:"the channel or block the watermark belongs to"`
		Mode        string  `json:"mode" jsonschema:"one of: inherit (a block uses its channel's; for a channel same as disable), disable (no watermark), override (this owner's own image and options)"`
		Position    *string `json:"position,omitempty" jsonschema:"corner: top_left, top_right, bottom_left or bottom_right; omitted keeps the stored value (bottom_right at first)"`
		OpacityPct  *int    `json:"opacity_pct,omitempty" jsonschema:"0-100; omitted keeps the stored value (70 at first)"`
		SizePct     *int    `json:"size_pct,omitempty" jsonschema:"percent of the video height, 2-40; omitted keeps the stored value (8 at first)"`
		ImageBase64 *string `json:"image_base64,omitempty" jsonschema:"the image, base64 encoded: PNG, JPEG or WebP (no SVG), at most 1 MiB decoded, validated by its content; omitted keeps the stored image"`
	}

	scheduleArgs struct {
		ID          string  `json:"id" jsonschema:"the schedule to replace"`
		Name        string  `json:"name" jsonschema:"schedule display name"`
		Description *string `json:"description,omitempty" jsonschema:"omitted clears it"`
	}
	scheduleIDArgs struct {
		ScheduleID string `json:"schedule_id" jsonschema:"the Schedule whose items to list"`
	}
	scheduleItemArgs struct {
		ID             string  `json:"id" jsonschema:"the schedule item to replace"`
		BlockID        string  `json:"block_id" jsonschema:"the block that airs"`
		RecurrenceType string  `json:"recurrence_type" jsonschema:"one of: weekly, once, continuous"`
		DaysOfWeek     []int   `json:"days_of_week,omitempty" jsonschema:"weekdays 0 (Sunday)-6; weekly only"`
		StartTimeLocal *string `json:"start_time_local,omitempty" jsonschema:"HH:MM; weekly only"`
		StartAt        *int64  `json:"start_at,omitempty" jsonschema:"epoch millis; once only"`
		Active         *bool   `json:"active,omitempty" jsonschema:"defaults to true when omitted"`
	}

	fillerPoolArgs struct {
		ID           string  `json:"id" jsonschema:"the filler pool to replace"`
		Name         string  `json:"name" jsonschema:"pool display name"`
		CollectionID *string `json:"collection_id,omitempty" jsonschema:"makes the pool dynamic (its items follow the collection); omitted keeps it manual"`
	}
	poolIDArgs struct {
		PoolID string `json:"pool_id" jsonschema:"the filler pool"`
	}
	poolItemDeleteArgs struct {
		PoolID string `json:"pool_id" jsonschema:"the filler pool"`
		ItemID string `json:"item_id" jsonschema:"the pool item to remove"`
		Confirmation
	}
	fillerRuleArgs struct {
		ID                string `json:"id" jsonschema:"the filler rule to replace"`
		ScopeType         string `json:"scope_type" jsonschema:"one of: channel, block"`
		ScopeID           string `json:"scope_id" jsonschema:"the Channel.ID or Block.ID, per scope_type"`
		Kind              string `json:"kind" jsonschema:"the kind: preroll, midroll, postroll, tail or fallback"`
		FillerPoolID      string `json:"filler_pool_id" jsonschema:"the pool the rule draws from"`
		SelectionStrategy string `json:"selection_strategy" jsonschema:"one of: sequential, random, weighted"`
		Position          int    `json:"position,omitempty" jsonschema:"ordering among rules of the same scope and kind"`
		MaxFitAttempts    *int   `json:"max_fit_attempts,omitempty" jsonschema:"omitted keeps the stored value"`
	}

	breakTemplateUpdateArgs struct {
		ID string `json:"id" jsonschema:"the break template to replace"`
		breaktemplate.Input
	}
	cutRuleSetUpdateArgs struct {
		ID string `json:"id" jsonschema:"the cut rule set to replace"`
		cutruleset.Input
	}

	mediaSourceUpdateArgs struct {
		ID         string  `json:"id" jsonschema:"the media source to replace"`
		Type       string  `json:"type" jsonschema:"one of: jellyfin, plex, local_folder"`
		Name       string  `json:"name" jsonschema:"display name"`
		BaseURL    *string `json:"base_url,omitempty" jsonschema:"server base URL; required unless type is local_folder"`
		Credential *string `json:"credential,omitempty" jsonschema:"new API key/token, encrypted at rest; omitted keeps the stored one. Never returned by any tool"`
	}
	catalogSyncArgs struct {
		ID                 string           `json:"id" jsonschema:"the push-mode media source"`
		Items              []map[string]any `json:"items,omitempty" jsonschema:"items to upsert, each {external_id, kind (movie|episode|other), title, duration_ms (positive), series_title?, source_modified_at?, genres? (comma-separated), production_year?} plus the optional metadata search_catalog returns (season_number, episode_number, series_external_id, overview, ...)"`
		DeletedExternalIDs []string         `json:"deleted_external_ids,omitempty" jsonschema:"external ids to remove"`
	}

	deviceCreateArgs struct {
		Name     string `json:"name" jsonschema:"device display name"`
		Platform string `json:"platform" jsonschema:"one of: linux, windows, android, android_tv, ios, tvos"`
	}

	streamPrefRulesArgs struct {
		Rules map[string]any `json:"rules" jsonschema:"{steps: [...]} with at most 20 steps, most preferred first; each step is {audio_language (wildcard tag such as pt*, empty matches any), subtitle_language? (omit for no subtitle preference), title_blocklist?, title_allowlist?}; replaces the stored cascade"`
	}
	streamPrefConfigGetArgs struct {
		ScopeType string `json:"scope_type" jsonschema:"one of: global, channel"`
		ScopeID   string `json:"scope_id,omitempty" jsonschema:"the channel id; required when scope_type is channel"`
	}
	streamPrefConfigPutArgs struct {
		ScopeType string         `json:"scope_type" jsonschema:"one of: global, channel"`
		ScopeID   string         `json:"scope_id,omitempty" jsonschema:"the channel id; required when scope_type is channel"`
		Rules     map[string]any `json:"rules" jsonschema:"{steps: [...]} with at most 20 steps, most preferred first; each step is {audio_language, subtitle_language?, title_blocklist?, title_allowlist?}; replaces the stored cascade"`
	}

	epgArgs struct {
		ChannelID string `json:"channel_id,omitempty" jsonschema:"restrict to one channel"`
		Start     int64  `json:"start,omitempty" jsonschema:"window start, epoch millis"`
		End       int64  `json:"end,omitempty" jsonschema:"window end, epoch millis"`
		GroupTail bool   `json:"group_tail,omitempty" jsonschema:"fold a tail into one item carrying its clips (the client's view)"`
	}
)

// registerRESTTools adds the tools that are a thin dispatch onto a REST
// handler (rest.go). Together with the hand-written tools in the other files
// they cover every route of the web UI; coverage.go lists which tool serves
// which route and TestEveryRESTRouteHasAnMCPTool keeps the two in step.
//
// Authorization: the REST layer has no permission model beyond "a valid
// session" (internal/api/auth.go), and a call here runs through the same
// RequireAuth with the caller's own Authorization header, so devices, stream
// preferences and media source credentials need exactly the session the web
// UI needs, and a device token is refused as it is on REST.
func registerRESTTools(s *mcp.Server, db storage.DB, mux http.Handler) {
	rc := &restCaller{h: api.RequireAuth(db, mux)}

	// Channels.
	addREST[idArgs](s, rc, "get_channel", "GET /channels/{id}", "Read one channel.", false)
	addREST[channelArgs](s, rc, "update_channel", "PUT /channels/{id}",
		"Replace a channel (a full replace: omitted guide_art_url, schedule_id and base_schedule_id are cleared). Changing the schedule resets the EPG grid.", false)
	addREST[idConfirmArgs](s, rc, "delete_channel", "DELETE /channels/{id}",
		"Delete a channel and its materialized guide.", true)
	addREST[idArgs](s, rc, "get_channel_epg_warnings", "GET /channels/{id}/epg-warnings",
		"List configuration problems that leave holes in a channel's guide (e.g. a criteria item whose query fails). Read only.", false)
	addREST[idConfirmArgs](s, rc, "reset_channel_epg", "POST /channels/{id}/reset-epg",
		"Apply a grid reset now: the materialized future and the program on air are discarded, continuous rotations restart from the beginning and the channel is regenerated before this returns; viewers switch program immediately. Use preview_channel_grid_reset first.", true)

	// Watermarks (client-rendered overlay, see docs/ARCHITECTURE.md "Watermark").
	addREST[idArgs](s, rc, "get_channel_watermark", "GET /channels/{id}/watermark",
		"Read a channel's watermark settings (mode disable with defaults when none was set). The image itself is not returned: image_id names it.", false)
	addREST[watermarkArgs](s, rc, "set_channel_watermark", "PUT /channels/{id}/watermark",
		"Set a channel's watermark: mode override with an image shows it over the channel's video on the client; options and the image keep their stored value when omitted. Returns the stored settings.", false)
	addREST[idConfirmArgs](s, rc, "clear_channel_watermark", "DELETE /channels/{id}/watermark",
		"Remove a channel's watermark settings and its stored image.", true)
	addREST[idArgs](s, rc, "get_block_watermark", "GET /blocks/{id}/watermark",
		"Read a block's watermark settings (mode inherit with defaults when none was set). The image itself is not returned: image_id names it.", false)
	addREST[watermarkArgs](s, rc, "set_block_watermark", "PUT /blocks/{id}/watermark",
		"Set a block's watermark: inherit (the channel's applies), disable (none while this block's programs and the filler decorating them are on air) or override (its own image and options). Omitted options and image keep their stored value.", false)
	addREST[idConfirmArgs](s, rc, "clear_block_watermark", "DELETE /blocks/{id}/watermark",
		"Remove a block's watermark settings (it inherits the channel's again) and its stored image.", true)

	// Blocks.
	addREST[noArgs](s, rc, "list_blocks", "GET /blocks", "List every block. The result is {items: [...]}.", false)
	addREST[idArgs](s, rc, "get_block", "GET /blocks/{id}", "Read one block.", false)
	addREST[idConfirmArgs](s, rc, "delete_block", "DELETE /blocks/{id}", "Delete a block and its items.", true)
	addREST[noArgs](s, rc, "list_block_list_mode_review", "GET /blocks/list-mode-review",
		"List the blocks whose list mode was held for review. Read only.", false)

	// Schedules and schedule items.
	addREST[noArgs](s, rc, "list_schedules", "GET /schedules", "List every schedule. The result is {items: [...]}.", false)
	addREST[idArgs](s, rc, "get_schedule", "GET /schedules/{id}", "Read one schedule.", false)
	addREST[scheduleArgs](s, rc, "update_schedule", "PUT /schedules/{id}", "Replace a schedule's name and description.", false)
	addREST[idConfirmArgs](s, rc, "delete_schedule", "DELETE /schedules/{id}",
		"Delete a schedule and its items. Resets the guide of the channels that used it.", true)
	addREST[scheduleIDArgs](s, rc, "list_schedule_items", "GET /schedules/{id}/items", "List the recurrence rules of a schedule.", false,
		alias("id", "schedule_id"))
	addREST[idArgs](s, rc, "get_schedule_item", "GET /schedule-items/{id}", "Read one schedule item.", false)
	addREST[scheduleItemArgs](s, rc, "update_schedule_item", "PUT /schedule-items/{id}",
		"Replace a schedule item (a full replace). Resets the EPG grid of the channels the schedule is assigned to.", false)
	addREST[idConfirmArgs](s, rc, "delete_schedule_item", "DELETE /schedule-items/{id}",
		"Delete a schedule item. Resets the EPG grid of the channels the schedule is assigned to.", true)

	// Filler pools, pool items and rules.
	addREST[idArgs](s, rc, "get_filler_pool", "GET /filler-pools/{id}", "Read one filler pool.", false)
	addREST[fillerPoolArgs](s, rc, "update_filler_pool", "PUT /filler-pools/{id}", "Replace a filler pool (a full replace). Triggers EPG regeneration.", false)
	addREST[idConfirmArgs](s, rc, "delete_filler_pool", "DELETE /filler-pools/{id}", "Delete a filler pool and its items.", true)
	addREST[poolIDArgs](s, rc, "list_filler_pool_items", "GET /filler-pools/{id}/items", "List the items of a filler pool.", false,
		alias("id", "pool_id"))
	addREST[poolItemDeleteArgs](s, rc, "delete_filler_pool_item", "DELETE /filler-pools/{id}/items/{item_id}",
		"Remove one item from a manual filler pool. Triggers EPG regeneration.", true, alias("id", "pool_id"))
	addREST[noArgs](s, rc, "list_filler_rules", "GET /filler-rules", "List every filler rule. The result is {items: [...]}.", false)
	addREST[idArgs](s, rc, "get_filler_rule", "GET /filler-rules/{id}", "Read one filler rule.", false)
	addREST[fillerRuleArgs](s, rc, "update_filler_rule", "PUT /filler-rules/{id}", "Replace a filler rule. Triggers EPG regeneration.", false)
	addREST[idConfirmArgs](s, rc, "delete_filler_rule", "DELETE /filler-rules/{id}", "Delete a filler rule. Triggers EPG regeneration.", true)

	// Break templates and cut rule sets.
	addREST[idArgs](s, rc, "get_break_template", "GET /break-templates/{id}", "Read one break template with its steps.", false)
	addREST[breakTemplateUpdateArgs](s, rc, "update_break_template", "PUT /break-templates/{id}",
		"Replace a break template and its steps. Triggers EPG regeneration.", false)
	addREST[idConfirmArgs](s, rc, "delete_break_template", "DELETE /break-templates/{id}", "Delete a break template. Triggers EPG regeneration.", true)
	addREST[idArgs](s, rc, "get_cut_rule_set", "GET /cut-rule-sets/{id}", "Read one cut rule set.", false)
	addREST[cutRuleSetUpdateArgs](s, rc, "update_cut_rule_set", "PUT /cut-rule-sets/{id}",
		"Replace a cut rule set (same fields as create_cut_rule_set). Triggers EPG regeneration.", false)
	addREST[idConfirmArgs](s, rc, "delete_cut_rule_set", "DELETE /cut-rule-sets/{id}", "Delete a cut rule set. Triggers EPG regeneration.", true)

	// Media sources. Credentials are written by update_media_source and
	// register_media_source and never come back from a read.
	// The Jellyfin address and key handed to a device when it pairs. The key is
	// write-only: a read reports only whether one is set and its last characters.
	addREST[noArgs](s, rc, "get_jellyfin_server_config", "GET /server-config/jellyfin",
		"Read the Jellyfin address and API key that paired devices receive. The key is never returned: only api_key_set and a short hint of its last characters.", false)
	addREST[jellyfinServerConfigArgs](s, rc, "update_jellyfin_server_config", "PUT /server-config/jellyfin",
		"Set the Jellyfin address and API key that paired devices receive. An omitted api_key keeps the stored one. Returns the masked configuration.", false)
	addREST[noArgs](s, rc, "list_media_sources", "GET /media-sources",
		"List every media source. Credentials are never returned. The result is {items: [...]}.", false)
	addREST[idArgs](s, rc, "get_media_source", "GET /media-sources/{id}", "Read one media source. Credentials are never returned.", false)
	addREST[mediaSourceUpdateArgs](s, rc, "update_media_source", "PUT /media-sources/{id}",
		"Replace a media source; credential, when given, replaces the stored one (it is write-only).", false)
	addREST[idConfirmArgs](s, rc, "delete_media_source", "DELETE /media-sources/{id}",
		"Delete a media source; what references it follows the database foreign-key rules, so a source still in use may be refused.", true)
	addREST[catalogSyncArgs](s, rc, "sync_media_source_catalog", "POST /media-sources/{id}/catalog-sync",
		"Push catalog items to a push-mode media source (upsert items, remove deleted_external_ids). Triggers EPG regeneration.", false)
	addREST[idArgs](s, rc, "pull_sync_media_source", "POST /media-sources/{id}/pull-sync",
		"Run a pull sync of a Jellyfin source in pull mode now and wait for it; returns the media source and the sync result.", false)

	// Devices (playback client tokens).
	addREST[noArgs](s, rc, "list_devices", "GET /devices", "List the paired playback devices. Tokens are never returned. The result is {items: [...]}.", false)
	addREST[deviceCreateArgs](s, rc, "create_device", "POST /devices",
		"Register a playback device. The response carries the device token exactly once; it cannot be read again.", false)
	addREST[idConfirmArgs](s, rc, "delete_device", "DELETE /devices/{id}", "Delete a device; its token stops working immediately.", true)

	// Stream (audio/subtitle) preferences.
	addREST[noArgs](s, rc, "get_my_stream_preference", "GET /stream-preferences/me", "Read the calling user's own audio/subtitle preference cascade.", false)
	addREST[streamPrefRulesArgs](s, rc, "set_my_stream_preference", "PUT /stream-preferences/me", "Replace the calling user's audio/subtitle preference cascade.", false)
	addREST[streamPrefConfigGetArgs](s, rc, "get_stream_preference_config", "GET /stream-preferences/config",
		"Read the global or per-channel audio/subtitle preference cascade.", false)
	addREST[streamPrefConfigPutArgs](s, rc, "set_stream_preference_config", "PUT /stream-preferences/config",
		"Replace the global or per-channel audio/subtitle preference cascade.", false, asQuery("scope_type", "scope_id"))

	// EPG.
	addREST[epgArgs](s, rc, "get_epg", "GET /epg",
		"Read the materialized guide the way the playback client does (enriched entries with titles and art; group_tail folds a tail into one item). preview_epg is the raw rows.", false)
	addREST[noArgs](s, rc, "get_epg_meta", "GET /epg/meta", "Read the guide's revision and window, the cheap staleness check.", false)

	// Collections.
	addREST[idArgs](s, rc, "preview_collection", "GET /collections/{id}/preview", "Preview the catalog items a collection matches.", false)
}
