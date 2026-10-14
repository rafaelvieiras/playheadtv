package domain

// WatermarkMode is how a channel or block treats the watermark.
type WatermarkMode string

const (
	// WatermarkInherit uses the channel's watermark (a block); for a channel
	// it means no watermark, like WatermarkDisable.
	WatermarkInherit WatermarkMode = "inherit"
	// WatermarkDisable shows no watermark.
	WatermarkDisable WatermarkMode = "disable"
	// WatermarkOverride shows this owner's own image with its own options.
	WatermarkOverride WatermarkMode = "override"
)

// Watermark corner positions.
const (
	WatermarkTopLeft     = "top_left"
	WatermarkTopRight    = "top_right"
	WatermarkBottomLeft  = "bottom_left"
	WatermarkBottomRight = "bottom_right"
)

// Defaults and bounds of the options (size is a percentage of the video
// height).
const (
	WatermarkDefaultPosition = WatermarkBottomRight
	WatermarkDefaultOpacity  = 70
	WatermarkDefaultSize     = 8
	WatermarkMinSize         = 2
	WatermarkMaxSize         = 40
)

// Watermark is the stored configuration of one owner (channel or block).
type Watermark struct {
	OwnerType string        `json:"owner_type"`
	OwnerID   string        `json:"owner_id"`
	Mode      WatermarkMode `json:"mode"`
	// ImageID names the stored image (empty: none uploaded); the bytes are at
	// GET /watermarks/{image_id}/image.
	ImageID     string `json:"image_id,omitempty"`
	ContentType string `json:"content_type,omitempty"`
	Position    string `json:"position"`
	OpacityPct  int    `json:"opacity_pct"`
	SizePct     int    `json:"size_pct"`
	UpdatedAt   int64  `json:"updated_at"`
}

// WatermarkRef is what a guide entry carries when a watermark applies to it:
// the resolved image and options, everything the client needs to draw it.
type WatermarkRef struct {
	ImageID    string `json:"image_id"`
	Position   string `json:"position"`
	OpacityPct int    `json:"opacity_pct"`
	SizePct    int    `json:"size_pct"`
}
