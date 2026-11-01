package domain

// ChannelLogo is the stored logo of one channel: its id, its type and when it
// was last replaced. The bytes themselves are never served in this shape —
// GET /channel-logos/{image_id}/image serves them.
//
// ImageID is the whole cache story: it changes on every upload, so the image
// URL changes with it and a client-cached copy can never go stale. It is also
// what GET /channels exposes (Channel.LogoImageID), which is how the client
// builds the URL.
type ChannelLogo struct {
	ChannelID   string `json:"channel_id"`
	ImageID     string `json:"image_id"`
	ContentType string `json:"content_type"`
	UpdatedAt   int64  `json:"updated_at"`
}
