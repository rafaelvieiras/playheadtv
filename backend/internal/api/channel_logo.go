package api

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
	"github.com/rafaelvieiras/playheadtv/backend/internal/storage"
)

// Channel logos: a small image per channel, drawn by the playback client in
// place of the channel's name in the guide and the OSD (see
// docs/ARCHITECTURE.md "Channel logo"). The image is uploaded as base64 JSON
// (so the web UI and the MCP tool share one route) and validated by its
// content, with the same rules as a watermark image.
//
// Storage: the bytes live in the channel row (logo_image_id, logo_content_type,
// logo_image) rather than in a table of their own, so a logo can never outlive
// its channel and no join is added to every channel read. A channel's own
// create/update never touches those columns: only the routes below do.
func registerChannelLogos(mux Router, db storage.DB) {
	mux.HandleFunc("GET /channels/{id}/logo", getChannelLogo(db))
	mux.HandleFunc("PUT /channels/{id}/logo", putChannelLogo(db))
	mux.HandleFunc("DELETE /channels/{id}/logo", deleteChannelLogo(db))
	mux.HandleFunc("GET /channel-logos/{image_id}/image", getChannelLogoImage(db))
}

// MaxChannelLogoBytes is the cap of an uploaded logo, kept separate from the
// watermark's only so a future change can move one without the other.
const MaxChannelLogoBytes = MaxImageBytes

// channelLogoMetaColumns deliberately omits logo_image: channels are listed far
// more often than their logo bytes are read.
const channelLogoMetaColumns = "COALESCE(logo_image_id, '') AS image_id, COALESCE(logo_content_type, '') AS content_type, updated_at"

func loadChannelLogo(r *http.Request, db storage.DB, channelID string) (domain.ChannelLogo, error) {
	var logo domain.ChannelLogo
	err := db.Query(r.Context(), &logo,
		"SELECT "+channelLogoMetaColumns+" FROM channel WHERE tenant_id = ? AND id = ?",
		DefaultTenantID, channelID)
	logo.ChannelID = channelID
	return logo, err
}

func getChannelLogo(db storage.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		logo, err := loadChannelLogo(r, db, r.PathValue("id"))
		if err != nil {
			handleDBError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, logo)
	}
}

type channelLogoRequest struct {
	// ImageBase64 is the logo itself: PNG, JPEG or WebP. It is required — a
	// logo upload without an image would only re-stamp the row.
	ImageBase64 string `json:"image_base64"`
}

func putChannelLogo(db storage.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		channelID := r.PathValue("id")
		if _, err := loadChannelLogo(r, db, channelID); err != nil {
			handleDBError(w, err)
			return
		}

		r.Body = http.MaxBytesReader(w, r.Body, bodyLimitForImage)
		var req channelLogoRequest
		if err := decodeJSON(r, &req); err != nil {
			if strings.Contains(err.Error(), "too large") {
				writeError(w, http.StatusRequestEntityTooLarge, errImageTooLarge.Error())
				return
			}
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if req.ImageBase64 == "" {
			writeError(w, http.StatusBadRequest, "image_base64 is required")
			return
		}
		image, contentType, status, err := decodeImageBase64(req.ImageBase64)
		if err != nil {
			writeError(w, status, err.Error())
			return
		}

		if err := db.Exec(r.Context(),
			"UPDATE channel SET logo_image_id = ?, logo_content_type = ?, logo_image = ?, updated_at = ? WHERE tenant_id = ? AND id = ?",
			uuid.NewString(), contentType, image, time.Now().UnixMilli(), DefaultTenantID, channelID); err != nil {
			handleDBError(w, err)
			return
		}
		logo, err := loadChannelLogo(r, db, channelID)
		if err != nil {
			handleDBError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, logo)
	}
}

// deleteChannelLogo removes the image and the id that names it, so the client
// falls back to the channel name and the guide art again.
func deleteChannelLogo(db storage.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		channelID := r.PathValue("id")
		if _, err := loadChannelLogo(r, db, channelID); err != nil {
			handleDBError(w, err)
			return
		}
		if err := db.Exec(r.Context(),
			"UPDATE channel SET logo_image_id = NULL, logo_content_type = NULL, logo_image = NULL, updated_at = ? WHERE tenant_id = ? AND id = ?",
			time.Now().UnixMilli(), DefaultTenantID, channelID); err != nil {
			handleDBError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// channelLogoImageRow is the image row; the SELECT aliases logo_image to
// "image" because storage's scanner maps a field by its own snake_case name
// (see AGENTS.md's alias gotcha).
type channelLogoImageRow struct {
	ContentType string
	Image       []byte
}

// getChannelLogoImage serves the bytes. The id changes on every upload, so the
// response is immutable for the life of the id and a client may cache it
// forever. nosniff and a restrictive CSP keep the response inert even if it is
// opened directly in a browser.
func getChannelLogoImage(db storage.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		imageID := r.PathValue("image_id")
		var rows []channelLogoImageRow
		err := db.Query(r.Context(), &rows,
			"SELECT COALESCE(logo_content_type, '') AS content_type, logo_image AS image FROM channel WHERE tenant_id = ? AND logo_image_id = ?",
			DefaultTenantID, imageID)
		if err != nil {
			handleDBError(w, err)
			return
		}
		if len(rows) == 0 || len(rows[0].Image) == 0 {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		etag := `"` + imageID + `"`
		h := w.Header()
		h.Set("Content-Type", rows[0].ContentType)
		h.Set("Cache-Control", "private, max-age=31536000, immutable")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
		h.Set("ETag", etag)
		if etagMatches(r.Header.Get("If-None-Match"), etag) {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		h.Set("Content-Length", fmt.Sprint(len(rows[0].Image)))
		w.WriteHeader(http.StatusOK)
		w.Write(rows[0].Image)
	}
}
