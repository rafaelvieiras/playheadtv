package mcpserver

// RouteTools maps every REST route ("METHOD /path", as internal/api registers
// it) to the MCP tool that serves the same operation. The web UI can do
// nothing the MCP cannot: TestEveryRESTRouteHasAnMCPTool walks api.Routes()
// (taken from the registered routes, not a hand-kept list) and fails on a
// route that is in neither RouteTools nor NoMCPCounterpart, and on a tool
// named here that is not registered. A new web UI capability therefore ships
// with its MCP tool, or with a justified entry below.
var RouteTools = map[string]string{
	// Channels.
	"POST /channels":                  "create_channel",
	"GET /channels":                   "list_channels",
	"GET /channels/{id}":              "get_channel",
	"PUT /channels/{id}":              "update_channel",
	"DELETE /channels/{id}":           "delete_channel",
	"POST /channels/{id}/reset-epg":   "reset_channel_epg",
	"GET /channels/{id}/epg-preview":  "preview_channel_grid_reset",
	"GET /channels/{id}/epg-warnings": "get_channel_epg_warnings",

	// Watermarks.
	"GET /channels/{id}/watermark":    "get_channel_watermark",
	"PUT /channels/{id}/watermark":    "set_channel_watermark",
	"DELETE /channels/{id}/watermark": "clear_channel_watermark",
	"GET /blocks/{id}/watermark":      "get_block_watermark",
	"PUT /blocks/{id}/watermark":      "set_block_watermark",
	"DELETE /blocks/{id}/watermark":   "clear_block_watermark",

	// Media sources.
	"POST /media-sources":                   "register_media_source",
	"GET /media-sources":                    "list_media_sources",
	"GET /media-sources/{id}":               "get_media_source",
	"PUT /media-sources/{id}":               "update_media_source",
	"DELETE /media-sources/{id}":            "delete_media_source",
	"POST /media-sources/{id}/catalog-sync": "sync_media_source_catalog",
	"POST /media-sources/{id}/pull-sync":    "pull_sync_media_source",

	// Catalog and series.
	"GET /catalog-items":                   "search_catalog",
	"GET /catalog-items/{id}":              "get_catalog_item",
	"POST /catalog-items/preview-criteria": "preview_criteria",
	"GET /series":                          "search_series",
	"POST /series/slice-preview":           "preview_series_slice",

	// Collections and the criteria migration review.
	"POST /collections":                     "create_collection",
	"GET /collections":                      "list_collections",
	"GET /collections/{id}":                 "get_collection",
	"PUT /collections/{id}":                 "update_collection",
	"DELETE /collections/{id}":              "delete_collection",
	"GET /collections/{id}/usage":           "get_collection_usage",
	"GET /collections/{id}/preview":         "preview_collection",
	"GET /criteria-migration-review":        "list_criteria_migration_review",
	"POST /criteria-migration-review/apply": "apply_criteria_review_proposal",

	// Blocks and block items.
	"POST /blocks":                        "create_block",
	"GET /blocks":                         "list_blocks",
	"GET /blocks/list-mode-review":        "list_block_list_mode_review",
	"GET /blocks/{id}":                    "get_block",
	"PUT /blocks/{id}":                    "update_block",
	"DELETE /blocks/{id}":                 "delete_block",
	"POST /blocks/{id}/items":             "add_block_item",
	"GET /blocks/{id}/items":              "list_block_items",
	"PUT /blocks/{id}/items/{item_id}":    "update_block_item",
	"DELETE /blocks/{id}/items/{item_id}": "delete_block_item",

	// Filler pools, pool items, rules and break templates.
	"POST /filler-pools":                        "create_filler_pool",
	"GET /filler-pools":                         "list_filler_pools",
	"GET /filler-pools/{id}":                    "get_filler_pool",
	"PUT /filler-pools/{id}":                    "update_filler_pool",
	"DELETE /filler-pools/{id}":                 "delete_filler_pool",
	"POST /filler-pools/{id}/items":             "add_filler_pool_item",
	"GET /filler-pools/{id}/items":              "list_filler_pool_items",
	"PUT /filler-pools/{id}/items/{item_id}":    "update_filler_pool_item",
	"DELETE /filler-pools/{id}/items/{item_id}": "delete_filler_pool_item",
	"POST /filler-rules":                        "create_filler_rule",
	"GET /filler-rules":                         "list_filler_rules",
	"GET /filler-rules/{id}":                    "get_filler_rule",
	"PUT /filler-rules/{id}":                    "update_filler_rule",
	"DELETE /filler-rules/{id}":                 "delete_filler_rule",
	"POST /break-templates":                     "create_break_template",
	"GET /break-templates":                      "list_break_templates",
	"GET /break-templates/{id}":                 "get_break_template",
	"PUT /break-templates/{id}":                 "update_break_template",
	"DELETE /break-templates/{id}":              "delete_break_template",

	// Cut rule sets.
	"POST /cut-rule-sets":         "create_cut_rule_set",
	"GET /cut-rule-sets":          "list_cut_rule_sets",
	"POST /cut-rule-sets/preview": "preview_cut_plan",
	"GET /cut-rule-sets/{id}":     "get_cut_rule_set",
	"PUT /cut-rule-sets/{id}":     "update_cut_rule_set",
	"DELETE /cut-rule-sets/{id}":  "delete_cut_rule_set",

	// Schedules and schedule items.
	"POST /schedules":             "create_schedule",
	"GET /schedules":              "list_schedules",
	"GET /schedules/{id}":         "get_schedule",
	"PUT /schedules/{id}":         "update_schedule",
	"DELETE /schedules/{id}":      "delete_schedule",
	"POST /schedules/{id}/items":  "create_schedule_item",
	"GET /schedules/{id}/items":   "list_schedule_items",
	"GET /schedule-items/{id}":    "get_schedule_item",
	"PUT /schedule-items/{id}":    "update_schedule_item",
	"DELETE /schedule-items/{id}": "delete_schedule_item",

	// EPG.
	"GET /epg":      "get_epg",
	"GET /epg/meta": "get_epg_meta",

	// Devices and stream preferences.
	"POST /devices":                  "create_device",
	"GET /devices":                   "list_devices",
	"DELETE /devices/{id}":           "delete_device",
	"GET /stream-preferences/me":     "get_my_stream_preference",
	"PUT /stream-preferences/me":     "set_my_stream_preference",
	"GET /stream-preferences/config": "get_stream_preference_config",
	"PUT /stream-preferences/config": "set_stream_preference_config",
}

// NoMCPCounterpart lists the REST routes that deliberately have no MCP tool,
// each with the reason. Keep it short: a route belongs here only when a tool
// would be meaningless or unsafe, never because it was not written yet.
var NoMCPCounterpart = map[string]string{
	"POST /app-users":                  "bootstraps the tenant's one user with a password; an MCP caller is already an authenticated session, and minting credentials is not authoring",
	"POST /sessions":                   "logs in; the MCP transport authenticates with the session token this returns, so a tool for it would be circular",
	"DELETE /sessions":                 "logs the calling session out; ending the transport's own credential from inside it is not an authoring operation",
	"POST /password-resets/consume":    "unauthenticated by design, credentialed by a single-use token that an operator mints at the server console; MCP requires an authenticated session, and a password reset must not be drivable by an agent",
	"GET /me":                          "returns the session's own user (e-mail); there is nothing to author, and the identity is already implied by the session",
	"GET /watermarks/{image_id}/image": "serves the binary image bytes to the playback client (what the guide's watermark image_id names); an MCP tool result is JSON, and the settings tools already return image_id and content_type",
	"GET /stream-preferences/meta":     "the playback client's cheap staleness poll over the preference rows; the preference tools already read the data itself",
	"POST /pair":                       "an unconfigured device bootstraps its first credential here; minting a PIN for a device that has no session yet is device provisioning, not authoring",
	"POST /pair/poll":                  "the second half of the device bootstrap handshake, credentialed only by the one-time poll secret the device just received; there is no authenticated session to drive it from",
	"POST /pin/approve":                "a human approves the pairing from a browser with app_user credentials; an MCP tool would bypass the out-of-band human confirmation this flow exists to require",
	"GET /pin":                         "the human-facing HTML form a browser loads to approve a pairing; it has no session and only posts back to /pin/approve, so there is nothing an authenticated MCP tool could call",
}
