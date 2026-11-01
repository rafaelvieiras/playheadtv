package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
	"github.com/rafaelvieiras/playheadtv/backend/internal/storage"
)

// Watermark limits. The image is uploaded inside a JSON body as base64 (so the
// web UI and the MCP tool share one route) and validated by its content, never
// by a file extension or the declared type — see SniffImage, shared with the
// channel logo.
const (
	// MaxWatermarkImageBytes caps the decoded image.
	MaxWatermarkImageBytes = MaxImageBytes
	// watermarkBodyLimit caps the JSON request body (the base64 of the cap
	// plus headroom for the other fields).
	watermarkBodyLimit = bodyLimitForImage
)

func registerWatermarks(mux Router, db storage.DB) {
	mux.HandleFunc("GET /channels/{id}/watermark", getWatermark(db, "channel"))
	mux.HandleFunc("PUT /channels/{id}/watermark", putWatermark(db, "channel"))
	mux.HandleFunc("DELETE /channels/{id}/watermark", deleteWatermark(db, "channel"))
	mux.HandleFunc("GET /blocks/{id}/watermark", getWatermark(db, "block"))
	mux.HandleFunc("PUT /blocks/{id}/watermark", putWatermark(db, "block"))
	mux.HandleFunc("DELETE /blocks/{id}/watermark", deleteWatermark(db, "block"))
	mux.HandleFunc("GET /watermarks/{image_id}/image", getWatermarkImage(db))
}

const watermarkMetaColumns = "owner_type, owner_id, mode, COALESCE(image_id, '') AS image_id, COALESCE(content_type, '') AS content_type, position, opacity_pct, size_pct, updated_at"

// ownerExists adds the owner's existence check to the handlers: a channel or
// block of another tenant (or none) is a 404, like on its own resource.
func ownerExists(ctx context.Context, db storage.DB, ownerType, id string) (bool, error) {
	var ids []string
	table := ownerType // "channel" and "block" are the table names
	if err := db.Query(ctx, &ids, "SELECT id FROM "+table+" WHERE tenant_id = ? AND id = ?", DefaultTenantID, id); err != nil {
		return false, err
	}
	return len(ids) > 0, nil
}

func loadWatermark(ctx context.Context, db storage.DB, ownerType, id string) (domain.Watermark, bool, error) {
	var rows []domain.Watermark
	if err := db.Query(ctx, &rows,
		"SELECT "+watermarkMetaColumns+" FROM watermark WHERE tenant_id = ? AND owner_type = ? AND owner_id = ?",
		DefaultTenantID, ownerType, id); err != nil {
		return domain.Watermark{}, false, err
	}
	if len(rows) == 0 {
		// No row: the default of the level (a block inherits, a channel has none).
		mode := domain.WatermarkInherit
		if ownerType == "channel" {
			mode = domain.WatermarkDisable
		}
		return domain.Watermark{
			OwnerType: ownerType, OwnerID: id, Mode: mode,
			Position: domain.WatermarkDefaultPosition, OpacityPct: domain.WatermarkDefaultOpacity, SizePct: domain.WatermarkDefaultSize,
		}, false, nil
	}
	return rows[0], true, nil
}

func getWatermark(db storage.DB, ownerType string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		ok, err := ownerExists(r.Context(), db, ownerType, id)
		if err != nil {
			handleDBError(w, err)
			return
		}
		if !ok {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		wm, _, err := loadWatermark(r.Context(), db, ownerType, id)
		if err != nil {
			handleDBError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, wm)
	}
}

type watermarkRequest struct {
	Mode string `json:"mode"`
	// Position, OpacityPct and SizePct keep the stored value (the default on
	// the first write) when omitted.
	Position   *string `json:"position"`
	OpacityPct *int    `json:"opacity_pct"`
	SizePct    *int    `json:"size_pct"`
	// ImageBase64 replaces the image when present; omitted keeps the stored one.
	ImageBase64 *string `json:"image_base64"`
}

func putWatermark(db storage.DB, ownerType string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		ok, err := ownerExists(r.Context(), db, ownerType, id)
		if err != nil {
			handleDBError(w, err)
			return
		}
		if !ok {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, watermarkBodyLimit)
		var req watermarkRequest
		if err := decodeJSON(r, &req); err != nil {
			if strings.Contains(err.Error(), "too large") {
				writeError(w, http.StatusRequestEntityTooLarge, errImageTooLarge.Error())
				return
			}
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		mode := domain.WatermarkMode(req.Mode)
		switch mode {
		case domain.WatermarkInherit, domain.WatermarkDisable, domain.WatermarkOverride:
		default:
			writeError(w, http.StatusBadRequest, "mode must be one of: inherit, disable, override")
			return
		}

		cur, exists, err := loadWatermark(r.Context(), db, ownerType, id)
		if err != nil {
			handleDBError(w, err)
			return
		}
		if req.Position != nil {
			switch *req.Position {
			case domain.WatermarkTopLeft, domain.WatermarkTopRight, domain.WatermarkBottomLeft, domain.WatermarkBottomRight:
				cur.Position = *req.Position
			default:
				writeError(w, http.StatusBadRequest, "position must be one of: top_left, top_right, bottom_left, bottom_right")
				return
			}
		}
		if req.OpacityPct != nil {
			if *req.OpacityPct < 0 || *req.OpacityPct > 100 {
				writeError(w, http.StatusBadRequest, "opacity_pct must be between 0 and 100")
				return
			}
			cur.OpacityPct = *req.OpacityPct
		}
		if req.SizePct != nil {
			if *req.SizePct < domain.WatermarkMinSize || *req.SizePct > domain.WatermarkMaxSize {
				writeError(w, http.StatusBadRequest, fmt.Sprintf("size_pct must be between %d and %d", domain.WatermarkMinSize, domain.WatermarkMaxSize))
				return
			}
			cur.SizePct = *req.SizePct
		}

		now := time.Now().UnixMilli()
		var image []byte
		replaceImage := false
		if req.ImageBase64 != nil {
			var status int
			var ct string
			image, ct, status, err = decodeImageBase64(*req.ImageBase64)
			if err != nil {
				writeError(w, status, err.Error())
				return
			}
			replaceImage = true
			cur.ContentType = ct
			cur.ImageID = uuid.NewString()
		}

		switch {
		case !exists:
			err = db.Exec(r.Context(),
				`INSERT INTO watermark (tenant_id, owner_type, owner_id, mode, image_id, content_type, image, position, opacity_pct, size_pct, created_at, updated_at)
				 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				DefaultTenantID, ownerType, id, string(mode), nullIfEmpty(cur.ImageID), nullIfEmpty(cur.ContentType), imageOrNil(image),
				cur.Position, cur.OpacityPct, cur.SizePct, now, now)
		case replaceImage:
			err = db.Exec(r.Context(),
				`UPDATE watermark SET mode = ?, image_id = ?, content_type = ?, image = ?, position = ?, opacity_pct = ?, size_pct = ?, updated_at = ?
				 WHERE tenant_id = ? AND owner_type = ? AND owner_id = ?`,
				string(mode), cur.ImageID, cur.ContentType, image, cur.Position, cur.OpacityPct, cur.SizePct, now,
				DefaultTenantID, ownerType, id)
		default:
			err = db.Exec(r.Context(),
				`UPDATE watermark SET mode = ?, position = ?, opacity_pct = ?, size_pct = ?, updated_at = ?
				 WHERE tenant_id = ? AND owner_type = ? AND owner_id = ?`,
				string(mode), cur.Position, cur.OpacityPct, cur.SizePct, now,
				DefaultTenantID, ownerType, id)
		}
		if err != nil {
			handleDBError(w, err)
			return
		}
		out, _, err := loadWatermark(r.Context(), db, ownerType, id)
		if err != nil {
			handleDBError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	}
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func imageOrNil(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return b
}

// deleteWatermark clears an owner's watermark entirely: the row, its image
// and its options go, and the owner is back to the default of its level.
func deleteWatermark(db storage.DB, ownerType string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		ok, err := ownerExists(r.Context(), db, ownerType, id)
		if err != nil {
			handleDBError(w, err)
			return
		}
		if !ok {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		if err := deleteWatermarkOf(r.Context(), db, ownerType, id); err != nil {
			handleDBError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// deleteWatermarkOf removes an owner's row; the delete handlers of a channel
// and a block call it so no image outlives its owner.
func deleteWatermarkOf(ctx context.Context, db storage.DB, ownerType, id string) error {
	return db.Exec(ctx, "DELETE FROM watermark WHERE tenant_id = ? AND owner_type = ? AND owner_id = ?", DefaultTenantID, ownerType, id)
}

type watermarkImageRow struct {
	ContentType string
	Image       []byte
}

// getWatermarkImage serves the bytes. The id changes on every upload, so the
// response is immutable for the life of the id. nosniff and a restrictive CSP
// keep the response inert even if opened directly in a browser.
func getWatermarkImage(db storage.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var rows []watermarkImageRow
		err := db.Query(r.Context(), &rows,
			"SELECT content_type, image FROM watermark WHERE tenant_id = ? AND image_id = ?",
			DefaultTenantID, r.PathValue("image_id"))
		if err != nil {
			handleDBError(w, err)
			return
		}
		if len(rows) == 0 || len(rows[0].Image) == 0 {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		h := w.Header()
		h.Set("Content-Type", rows[0].ContentType)
		h.Set("Cache-Control", "private, max-age=31536000, immutable")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
		h.Set("ETag", `"`+r.PathValue("image_id")+`"`)
		if etagMatches(r.Header.Get("If-None-Match"), `"`+r.PathValue("image_id")+`"`) {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		h.Set("Content-Length", fmt.Sprint(len(rows[0].Image)))
		w.WriteHeader(http.StatusOK)
		w.Write(rows[0].Image)
	}
}
