package domain

// CatalogPerson is one credited person on a catalog item. Type is "Actor" or
// "Director" (derived from how the person is linked), Role is the character
// played and only set for actors.
type CatalogPerson struct {
	Name string `json:"name"`
	Type string `json:"type,omitempty"`
	Role string `json:"role,omitempty"`
}

// CatalogChapter is one chapter marker; StartMs is the offset from the start
// of the item.
type CatalogChapter struct {
	Name    string `json:"name"`
	StartMs int64  `json:"start_ms"`
}

// Media segment types stored on an item (lower case). The planner reads
// SegmentIntro.
const (
	SegmentIntro      = "intro"
	SegmentOutro      = "outro"
	SegmentRecap      = "recap"
	SegmentPreview    = "preview"
	SegmentCommercial = "commercial"
)

// CatalogSegment is one media segment (a marked stretch such as the intro) of
// an item; both offsets are milliseconds from the start of the item.
type CatalogSegment struct {
	Type    string `json:"type"`
	StartMs int64  `json:"start_ms"`
	EndMs   int64  `json:"end_ms"`
}

// CatalogMetadata is the descriptive metadata of a catalog item beyond what
// scheduling needs. It is embedded in CatalogItem (responses, flattened by
// both encoding/json and storage's row scanner) and in the sync input types,
// so the push API accepts exactly what the API returns.
//
// Scalar fields are plain catalog_item columns. The list fields are stored
// in per-tenant dimension and link tables (migrations/0009), except
// ProductionLocations and Chapters which are JSON columns. List fields are
// omitted from JSON when empty. Dates are epoch milliseconds (UTC); images
// are Jellyfin image tags only, never URLs or bytes.
type CatalogMetadata struct {
	SeasonNumber     *int64   `json:"season_number"` // 0 = specials
	EpisodeNumber    *int64   `json:"episode_number"`
	EpisodeNumberEnd *int64   `json:"episode_number_end"` // last episode of a multi-episode file
	SeriesExternalID *string  `json:"series_external_id"` // the parent series' id in the source
	SeasonName       *string  `json:"season_name"`
	OriginalTitle    *string  `json:"original_title"`
	SortTitle        *string  `json:"sort_title"`
	PremiereDate     *int64   `json:"premiere_date"`
	DateAdded        *int64   `json:"date_added"`
	Overview         *string  `json:"overview"`
	Tagline          *string  `json:"tagline"`
	ContentRating    *string  `json:"content_rating"`
	CommunityRating  *float64 `json:"community_rating"`
	CriticRating     *float64 `json:"critic_rating"`
	ImdbID           *string  `json:"imdb_id"`
	TmdbID           *string  `json:"tmdb_id"`
	TvdbID           *string  `json:"tvdb_id"`

	// Technical info of the primary video file.
	Width      *int64  `json:"width"`
	Height     *int64  `json:"height"`
	VideoCodec *string `json:"video_codec"`
	Container  *string `json:"container"`
	Bitrate    *int64  `json:"bitrate"` // bits per second

	// Image references (Jellyfin image tags). The backend never serves art.
	ImagePrimaryTag       *string `json:"image_primary_tag"`
	ImageBackdropTag      *string `json:"image_backdrop_tag"`
	ImageThumbTag         *string `json:"image_thumb_tag"`
	SeriesImagePrimaryTag *string `json:"series_image_primary_tag"` // parent series' primary art, fallback for episodes

	Studios           []string         `json:"studios,omitempty"`
	Tags              []string         `json:"tags,omitempty"`
	AudioLanguages    []string         `json:"audio_languages,omitempty"`
	SubtitleLanguages []string         `json:"subtitle_languages,omitempty"`
	Cast              []CatalogPerson  `json:"cast,omitempty"` // main cast, billing order
	Directors         []CatalogPerson  `json:"directors,omitempty"`
	Locations         []string         `json:"production_locations,omitempty"`
	Chapters          []CatalogChapter `json:"chapters,omitempty"`
	// Segments are the item's media segments (intro, outro, ...); empty when
	// the source has none or was not asked (migrations/0021).
	Segments []CatalogSegment `json:"segments,omitempty"`
}
