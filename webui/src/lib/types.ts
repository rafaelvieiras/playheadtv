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
  created_at: number
  updated_at: number
}

// ChannelInput is the body shape for POST /channels and PUT /channels/{id}.
export interface ChannelInput {
  number: number
  name: string
  guide_art_url: string | null
  timezone: string
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
  source_schedule_entry_id: string | null
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

// Mirrors backend/internal/domain.BlockItem.
export interface BlockItem {
  id: string
  block_id: string
  position: number
  catalog_item_id: string
  trim_duration_ms: number | null
}

// BlockItemInput is the body shape for POST /blocks/{id}/items.
export interface BlockItemInput {
  catalog_item_id: string
  trim_duration_ms: number | null
}

// Mirrors backend/internal/domain.RecurrenceType.
export type RecurrenceType = 'weekly' | 'once'

// Mirrors backend/internal/domain.ScheduleEntry (backend/internal/api/schedules.go).
export interface ScheduleEntry {
  id: string
  tenant_id: string
  channel_id: string
  block_id: string
  recurrence_type: RecurrenceType
  day_of_week: number | null
  start_time_local: string | null
  start_at: number | null
  active: boolean
  created_at: number
  updated_at: number
}

// ScheduleEntryInput is the body shape for POST /schedule-entries and
// PUT /schedule-entries/{id}.
export interface ScheduleEntryInput {
  channel_id: string
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
