// Wire types mirroring backend/internal/domain and the API contracts in
// backend/internal/api/{auth,channels}.go. Kept minimal — only what this
// slice of the web UI (auth + channels) touches.

export interface User {
  id: string
  tenant_id: string
  email: string
  created_at: number
}

export interface LoginResponse {
  token: string
  expires_at: number
  user: User
}

export interface Channel {
  id: string
  tenant_id: string
  number: number
  name: string
  guide_art_url: string | null
  timezone: string
  // schedule_id is which Schedule (if any) drives this channel — issue #7's
  // Schedule/Playout split. null means no schedule assigned (e.g. a channel
  // relying only on a channel-scoped fallback filler rule).
  schedule_id: string | null
  created_at: number
  updated_at: number
}

// ChannelInput is the body shape for POST /channels and PUT /channels/{id}.
export interface ChannelInput {
  number: number
  name: string
  guide_art_url: string | null
  timezone: string
  schedule_id: string | null
}

// Mirrors backend/internal/domain.EpgEntryKind.
export type EpgEntryKind = 'program' | 'preroll' | 'midroll' | 'postroll' | 'tail' | 'fallback'

// Mirrors backend/internal/domain.EpgEntry (backend/internal/api/epg.go).
export interface EpgEntry {
  id: string
  tenant_id: string
  channel_id: string
  kind: EpgEntryKind
  catalog_item_id: string
  start_at: number
  end_at: number
  source_schedule_item_id: string | null
  generated_at: number
}

// Subset of backend/internal/domain.CatalogItem this page reads for display.
export interface CatalogItem {
  id: string
  tenant_id: string
  media_source_id: string
  external_id: string
  kind: string
  title: string
  series_title: string | null
  duration_ms: number
  source_modified_at: number | null
  synced_at: number
  // Comma-separated, in the source's language, not normalized.
  genres: string
  production_year: number | null
  // Descriptive metadata (backend/internal/domain.CatalogMetadata). All
  // optional: items synced before the metadata sync existed have none, and
  // the list fields are omitted when empty. Dates are epoch ms (UTC), images
  // are Jellyfin image tags (the backend never serves art). cast, directors,
  // production_locations and chapters come only from GET /catalog-items/{id}.
  season_number?: number | null // 0 = specials
  episode_number?: number | null
  episode_number_end?: number | null
  series_external_id?: string | null
  season_name?: string | null
  original_title?: string | null
  sort_title?: string | null
  premiere_date?: number | null
  date_added?: number | null
  overview?: string | null
  tagline?: string | null
  content_rating?: string | null
  community_rating?: number | null
  critic_rating?: number | null
  imdb_id?: string | null
  tmdb_id?: string | null
  tvdb_id?: string | null
  width?: number | null
  height?: number | null
  video_codec?: string | null
  container?: string | null
  bitrate?: number | null
  image_primary_tag?: string | null
  image_backdrop_tag?: string | null
  image_thumb_tag?: string | null
  series_image_primary_tag?: string | null
  studios?: string[]
  tags?: string[]
  audio_languages?: string[]
  subtitle_languages?: string[]
  cast?: CatalogPerson[]
  directors?: CatalogPerson[]
  production_locations?: string[]
  chapters?: CatalogChapter[]
}

export interface CatalogPerson {
  name: string
  type?: string
  role?: string
}

export interface CatalogChapter {
  name: string
  start_ms: number
}

// Mirrors backend/internal/api's fillerPoolCriteriaRequest / poolCriteria —
// the shape sent when WRITING a pool's or block's criteria (POST/PUT). Not
// what GET returns (see FillerPool.criteria/Block.criteria below).
export interface DynamicCriteria {
  kind: 'movie' | 'episode'
  genres: string[]
  year_from: number | null
  year_to: number | null
}

// Mirrors backend/internal/domain.Block (backend/internal/api/blocks.go).
export interface Block {
  id: string
  tenant_id: string
  name: string
  description: string | null
  criteria: string | null // raw JSON string or null — see DynamicCriteria
  created_at: number
  updated_at: number
}

// BlockInput is the body shape for POST /blocks and PUT /blocks/{id}.
export interface BlockInput {
  name: string
  description: string | null
  criteria: DynamicCriteria | null
}

// Mirrors backend/internal/domain.PlaybackOrder values: how a series block
// item walks its episodes.
export type PlaybackOrder = 'original' | 'random' | 'shuffle' | 'release_date'

export const PLAYBACK_ORDER_LABELS: Record<PlaybackOrder, string> = {
  original: 'Original order',
  random: 'Random',
  shuffle: 'Shuffle (no repeats)',
  release_date: 'Release date',
}

// Mirrors backend/internal/domain.SeriesItemView: the identity and order of a
// series block item plus what it currently expands to (read live from the
// catalog). `slice` is reserved and always null for now.
export interface SeriesItemView {
  media_source_id: string
  series_external_id?: string
  series_title?: string
  playback_order: PlaybackOrder
  slice: unknown
  episode_count: number
  total_duration_ms: number
}

// Mirrors backend/internal/domain.BlockItem. A catalog item has
// catalog_item_id and an optional trim; a series item has `series` and no
// catalog_item_id (omitted).
export interface BlockItem {
  id: string
  block_id: string
  position: number
  item_type: 'catalog' | 'series'
  catalog_item_id?: string
  trim_duration_ms: number | null
  series?: SeriesItemView
}

// BlockItemInput is the body shape for POST /blocks/{id}/items: a catalog item
// or, with `series`, a whole series.
export interface BlockItemInput {
  catalog_item_id?: string
  trim_duration_ms?: number | null
  series?: {
    media_source_id: string
    series_external_id?: string
    series_title?: string
    playback_order: PlaybackOrder
  }
}

// One row of GET /series (backend/internal/series.Summary).
export interface SeriesSummary {
  media_source_id: string
  series_external_id: string | null
  series_title: string
  episode_count: number
  total_duration_ms: number
}

// Mirrors backend/internal/domain.RecurrenceType.
export type RecurrenceType = 'weekly' | 'once' | 'continuous'

// Mirrors backend/internal/domain.Schedule (backend/internal/api/schedules.go)
// — a reusable, named group of ScheduleItems, not tied to a channel. Issue
// #7's Schedule/Playout split: a Channel is assigned one via
// Channel.schedule_id, and the same Schedule can drive more than one
// channel.
export interface Schedule {
  id: string
  tenant_id: string
  name: string
  description: string | null
  created_at: number
  updated_at: number
}

// ScheduleInput is the body shape for POST /schedules and
// PUT /schedules/{id}.
export interface ScheduleInput {
  name: string
  description: string | null
}

// Mirrors backend/internal/domain.ScheduleItem — one recurrence rule inside
// a Schedule (schedule_entry's old shape, before issue #7's split, minus the
// channel_id it used to carry directly).
export interface ScheduleItem {
  id: string
  tenant_id: string
  schedule_id: string
  block_id: string
  recurrence_type: RecurrenceType
  day_of_week: number | null
  start_time_local: string | null
  start_at: number | null
  active: boolean
  created_at: number
  updated_at: number
}

// ScheduleItemInput is the body shape for POST /schedules/{id}/items and
// PUT /schedule-items/{id} (schedule_id is the URL path, not a body field).
export interface ScheduleItemInput {
  block_id: string
  recurrence_type: RecurrenceType
  day_of_week: number | null
  start_time_local: string | null
  start_at: number | null
  active: boolean
}

// Mirrors backend/internal/domain.SourceType.
export type SourceType = 'jellyfin' | 'plex' | 'local_folder'

// Mirrors backend/internal/domain.SyncMode.
export type SyncMode = 'pull' | 'push'

// Mirrors backend/internal/domain.MediaSource (backend/internal/api/media_sources.go).
// The credential is never serialized back by the API (CredentialCipher has
// `json:"-"`), so there is no field for it here.
export interface MediaSource {
  id: string
  tenant_id: string
  type: SourceType
  name: string
  base_url: string | null
  sync_mode: SyncMode
  last_probe_at: number | null
  last_probe_ok: boolean | null
  last_synced_at: number | null
  created_at: number
  updated_at: number
}

// Mirrors backend/internal/pullsync.SyncResult — the POST
// /media-sources/{id}/pull-sync response body's "result" field.
export interface PullSyncResult {
  media_source_id: string
  items_upserted: number
  items_deleted: number
  synced_at: number
}

// PullSyncResponse is the full POST /media-sources/{id}/pull-sync response
// shape (backend/internal/api/media_sources.go's pullSyncResponse).
export interface PullSyncResponse {
  media_source: MediaSource
  result: PullSyncResult
}

// MediaSourceInput is the body shape for POST /media-sources and
// PUT /media-sources/{id}. `credential` is optional — omit it entirely to
// leave a previously-saved credential untouched (an empty string would
// overwrite it, since the backend's field is a nullable pointer that only
// stays nil when the JSON key is absent).
export interface MediaSourceInput {
  type: SourceType
  name: string
  base_url: string | null
  credential?: string
}

// Mirrors backend/internal/domain.Platform.
export type Platform = 'linux' | 'windows' | 'android' | 'android_tv' | 'ios' | 'tvos'

// Mirrors backend/internal/domain.Device (backend/internal/api/auth.go). The
// API token hash never round-trips to a client.
export interface Device {
  id: string
  tenant_id: string
  name: string
  platform: Platform
  last_seen_at: number | null
  created_at: number
}

// DeviceInput is the body shape for POST /devices.
export interface DeviceInput {
  name: string
  platform: Platform
}

// CreateDeviceResponse is the POST /devices response shape: the only time
// the raw token is ever exposed — it is never returned again afterward.
export interface CreateDeviceResponse {
  device: Device
  token: string
}

// Mirrors backend/internal/domain.FillerPool. criteria is the raw JSON string
// stored in the DB (null for a manually managed pool) — JSON.parse it to get
// a DynamicCriteria when editing.
export interface FillerPool {
  id: string
  tenant_id: string
  name: string
  criteria: string | null
  created_at: number
}

// FillerPoolInput is the body shape for POST /filler-pools and
// PUT /filler-pools/{id}. criteria here is the parsed OBJECT (or null),
// unlike FillerPool.criteria above which is the raw string GET returns.
export interface FillerPoolInput {
  name: string
  criteria: DynamicCriteria | null
}

// Mirrors backend/internal/domain.FillerPoolItem.
export interface FillerPoolItem {
  id: string
  filler_pool_id: string
  catalog_item_id: string
  weight: number
}

// Mirrors backend/internal/domain.FillerScope.
export type FillerScope = 'channel' | 'block'

// Mirrors backend/internal/domain.FillerKind.
export type FillerKind = 'preroll' | 'midroll' | 'postroll' | 'tail' | 'fallback'

// Mirrors backend/internal/domain.SelectionStrategy.
export type SelectionStrategy = 'sequential' | 'random' | 'weighted'

// Mirrors backend/internal/domain.FillerRule.
export interface FillerRule {
  id: string
  tenant_id: string
  scope_type: FillerScope
  scope_id: string
  kind: FillerKind
  filler_pool_id: string
  selection_strategy: SelectionStrategy
  position: number
  created_at: number
}

// FillerRuleInput is the body shape for POST /filler-rules and
// PUT /filler-rules/{id}.
export interface FillerRuleInput {
  scope_type: FillerScope
  scope_id: string
  kind: FillerKind
  filler_pool_id: string
  selection_strategy: SelectionStrategy
  position: number
}

// Mirrors backend/internal/domain.StreamRuleStep. subtitle_language is null
// for "no subtitle preference at this step" (audio-only match); an empty
// string means "any subtitle language is fine" once a subtitle IS requested.
// Same distinction applies to audio_language, minus the null case: "" always
// means "any audio language". Both are wildcard-capable (e.g. "pt*").
export interface StreamRuleStep {
  audio_language: string
  subtitle_language: string | null
  title_blocklist: string[]
  title_allowlist: string[]
}

// Mirrors backend/internal/domain.StreamRuleCascade: an ordered list of
// fallback steps, most-preferred first. steps is null (not []) on the wire
// for an unconfigured scope — Go's encoding/json marshals a nil slice as
// null — so every reader must treat null the same as an empty list.
export interface StreamRuleCascade {
  steps: StreamRuleStep[] | null
}

// Mirrors backend/internal/domain.PreferenceScope — tier 4 (global, at most
// one row per tenant) or tier 3 (one channel's admin override) of the stream
// selection cascade. Tier 2 (a user's own preference, GET/PUT
// /stream-preferences/me) has no admin-UI shape here: it needs a logged-in
// end-user concept the client doesn't have yet.
export type StreamPreferenceScope = 'global' | 'channel'

// Mirrors the GET/PUT /stream-preferences/config response body
// (backend/internal/api/stream_preferences.go's writeStreamPreferenceRule).
// A GET for a scope with no row yet still returns 200 with an empty cascade
// rather than 404 — see that handler's comment.
export interface StreamPreferenceRule {
  id: string
  scope_type: StreamPreferenceScope
  scope_id: string
  rules: StreamRuleCascade
  updated_at: number
}

// StreamPreferenceRuleInput is the body shape for
// PUT /stream-preferences/config?scope_type=...&scope_id=... — scope itself
// is a query param, not part of the body (see parseScopeQuery in
// backend/internal/api/stream_preferences.go).
export interface StreamPreferenceRuleInput {
  rules: StreamRuleCascade
}

// BlockItemUpdateInput is the body of PUT /blocks/{id}/items/{item_id}: at
// least one field is required, an absent/null one is left unchanged.
// `position` is the new 0-based index (the backend keeps positions contiguous);
// `trim_duration_ms` must be positive (catalog items only); `playback_order`
// is for series items only and restarts the series. Manual blocks only (409 on
// dynamic).
export interface BlockItemUpdateInput {
  position?: number
  trim_duration_ms?: number
  playback_order?: PlaybackOrder
}

// FillerPoolItemUpdateInput is the body of
// PUT /filler-pools/{id}/items/{item_id}. Manual pools only (409 on dynamic).
export interface FillerPoolItemUpdateInput {
  weight: number
}

// Response of POST /catalog-items/preview-criteria
// (backend/internal/catalogsync.CriteriaPreview): what the criteria match
// right now; nothing is persisted. `sample` holds at most 20 items.
export interface CriteriaPreview {
  match_count: number
  total_duration_ms: number
  sample: CatalogItem[]
}
