package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/rafaelvieiras/playheadtv/backend/internal/catalogsync"
	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
	"github.com/rafaelvieiras/playheadtv/backend/internal/materializer"
	"github.com/rafaelvieiras/playheadtv/backend/internal/storage"
)

// mat is only needed because a channel's own ScheduleID assignment affects
// EPG materialization (create/update trigger it) — issue #7's
// Schedule/Playout split. Nothing else about a channel does.
func registerChannels(mux Router, db storage.DB, mat *materializer.Materializer) {
	mux.HandleFunc("POST /channels", createChannel(db, mat))
	mux.HandleFunc("GET /channels", listChannels(db))
	mux.HandleFunc("GET /channels/{id}", getChannel(db))
	mux.HandleFunc("PUT /channels/{id}", updateChannel(db, mat))
	mux.HandleFunc("DELETE /channels/{id}", deleteChannel(db))
	mux.HandleFunc("POST /channels/{id}/reset-epg", resetChannelEPG(db, mat))
}

const channelColumns = "id, tenant_id, number, name, guide_art_url, logo_image_id, timezone, schedule_id, base_schedule_id, created_at, updated_at"

type channelRequest struct {
	Number      int     `json:"number"`
	Name        string  `json:"name"`
	GuideArtURL *string `json:"guide_art_url"`
	Timezone    string  `json:"timezone"`
	// ScheduleID assigns a Schedule to this channel (issue #7); nil/omitted
	// means no schedule (e.g. a pure dynamic-filler-pool channel).
	ScheduleID *string `json:"schedule_id"`
	// BaseScheduleID is the optional second Schedule that plays real
	// programming in every gap ScheduleID leaves (see
	// domain.Channel.BaseScheduleID). It must differ from ScheduleID.
	BaseScheduleID *string `json:"base_schedule_id"`
	// Loop is the one-call shortcut for the most common channel of all: a
	// collection looped 24/7 (issue #103). The backend creates the supporting
	// Schedule and its continuous collection item, so the author writes one
	// object instead of four and graduates to a full schedule later by editing
	// the ids the response returns. Mutually exclusive with ScheduleID.
	Loop *channelLoopRequest `json:"loop"`
}

// channelLoopRequest is a channel that airs one collection, looped.
type channelLoopRequest struct {
	CollectionID  string `json:"collection_id"`
	PlaybackOrder string `json:"playback_order"`
}

// ChannelLoop is channelLoopRequest for the MCP layer, which shares the same
// creation path instead of duplicating it.
type ChannelLoop = channelLoopRequest

// channelLoopResponse is the response of a write that carried a loop: the
// channel plus the objects created for it, so nothing is hidden.
type channelLoopResponse struct {
	Channel        domain.Channel `json:"channel"`
	ScheduleID     string         `json:"schedule_id"`
	ScheduleItemID string         `json:"schedule_item_id"`
}

// loopScheduleName is the name of the Schedule a loop creates: derived from the
// channel, so a later read tells the author where it came from.
func loopScheduleName(channelName string) string {
	return "Loop · " + channelName
}

// CreateLoop creates the schedule and its continuous collection item a loop
// channel airs, and returns their ids.
func CreateLoop(ctx context.Context, db storage.DB, req channelLoopRequest, channelName string, now int64) (scheduleID, itemID string, err error) {
	if req.CollectionID == "" {
		return "", "", errors.New("loop.collection_id is required")
	}
	if !domain.ValidCriteriaOrder(req.PlaybackOrder) {
		return "", "", errors.New("loop.playback_order must be one of title, release_date, random, shuffle, interleaved")
	}
	if _, err := catalogsync.LoadCollectionQuery(ctx, db, DefaultTenantID, req.CollectionID); err != nil {
		return "", "", errors.New("loop.collection_id does not match a collection")
	}
	scheduleID, itemID = uuid.NewString(), uuid.NewString()
	if err := db.Exec(ctx,
		"INSERT INTO schedule (id, tenant_id, name, description, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)",
		scheduleID, DefaultTenantID, loopScheduleName(channelName), "created with the channel's loop", now, now,
	); err != nil {
		return "", "", err
	}
	if err := db.Exec(ctx,
		`INSERT INTO schedule_item (id, tenant_id, schedule_id, block_id, collection_id, playback_order, recurrence_type, active, created_at, updated_at)
		 VALUES (?, ?, ?, NULL, ?, ?, 'continuous', 1, ?, ?)`,
		itemID, DefaultTenantID, scheduleID, req.CollectionID, req.PlaybackOrder, now, now,
	); err != nil {
		return "", "", err
	}
	return scheduleID, itemID, nil
}

// existingLoop returns the channel with this number and, when it is a loop
// channel created for the same collection and order, the ids of its schedule
// and item: a repeated identical create is then a no-op instead of an error.
func existingLoop(ctx context.Context, db storage.DB, number int, req channelLoopRequest) (*domain.Channel, string, string, error) {
	ch, err := fetchChannelByNumber(ctx, db, number)
	if err != nil || ch == nil {
		return ch, "", "", err
	}
	if ch.ScheduleID == nil {
		return ch, "", "", nil
	}
	type loopItem struct {
		ID            string
		CollectionID  string
		PlaybackOrder string
	}
	var items []loopItem
	if err := db.Query(ctx, &items,
		"SELECT id, COALESCE(collection_id, '') AS collection_id, COALESCE(playback_order, '') AS playback_order FROM schedule_item WHERE tenant_id = ? AND schedule_id = ? AND recurrence_type = 'continuous'",
		DefaultTenantID, *ch.ScheduleID,
	); err != nil {
		return ch, "", "", err
	}
	if len(items) == 1 && items[0].CollectionID == req.CollectionID && items[0].PlaybackOrder == req.PlaybackOrder {
		return ch, *ch.ScheduleID, items[0].ID, nil
	}
	return ch, "", "", nil
}

// fetchChannelByNumber returns the channel with this number, or nil.
func fetchChannelByNumber(ctx context.Context, db storage.DB, number int) (*domain.Channel, error) {
	var chs []domain.Channel
	if err := db.Query(ctx, &chs,
		"SELECT "+channelColumns+" FROM channel WHERE tenant_id = ? AND number = ?",
		DefaultTenantID, number,
	); err != nil {
		return nil, err
	}
	if len(chs) == 0 {
		return nil, nil
	}
	return &chs[0], nil
}

// ValidateChannelSchedules checks a channel's two schedule references: the
// base schedule must exist for the tenant and must not be the pinned one.
// The message of the returned error is meant for the client.
func ValidateChannelSchedules(ctx context.Context, db storage.DB, scheduleID, baseScheduleID *string) error {
	if baseScheduleID == nil {
		return nil
	}
	if scheduleID != nil && *scheduleID == *baseScheduleID {
		return errors.New("base_schedule_id must be a different schedule than schedule_id")
	}
	var ids []string
	if err := db.Query(ctx, &ids, "SELECT id FROM schedule WHERE tenant_id = ? AND id = ?", DefaultTenantID, *baseScheduleID); err != nil {
		return err
	}
	if len(ids) == 0 {
		return errors.New("base_schedule_id does not reference an existing schedule")
	}
	return nil
}

func fetchChannel(db storage.DB, r *http.Request, id string) (domain.Channel, error) {
	var ch domain.Channel
	err := db.Query(r.Context(), &ch,
		"SELECT "+channelColumns+" FROM channel WHERE tenant_id = ? AND id = ?",
		DefaultTenantID, id,
	)
	return ch, err
}

func createChannel(db storage.DB, mat *materializer.Materializer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req channelRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if req.Name == "" {
			writeError(w, http.StatusBadRequest, "name is required")
			return
		}
		if req.Timezone == "" {
			req.Timezone = "UTC"
		}
		if err := ValidateChannelSchedules(r.Context(), db, req.ScheduleID, req.BaseScheduleID); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if req.Loop != nil && req.ScheduleID != nil {
			writeError(w, http.StatusBadRequest, "loop and schedule_id are mutually exclusive: the loop creates its own schedule")
			return
		}

		now := time.Now().UnixMilli()
		var loopScheduleID, loopItemID string
		if req.Loop != nil {
			// A repeated identical create is a no-op, never a duplicate channel.
			if existing, sched, item, err := existingLoop(r.Context(), db, req.Number, *req.Loop); err != nil {
				handleDBError(w, err)
				return
			} else if existing != nil {
				if sched == "" {
					writeError(w, http.StatusConflict, "a channel with this number already exists and does not loop this collection")
					return
				}
				writeJSON(w, http.StatusOK, channelLoopResponse{Channel: *existing, ScheduleID: sched, ScheduleItemID: item})
				return
			}
			sched, item, err := CreateLoop(r.Context(), db, *req.Loop, req.Name, now)
			if err != nil {
				writeLoopError(w, err)
				return
			}
			loopScheduleID, loopItemID = sched, item
			req.ScheduleID = &loopScheduleID
		}
		ch := domain.Channel{
			ID:             uuid.NewString(),
			TenantID:       DefaultTenantID,
			Number:         req.Number,
			Name:           req.Name,
			GuideArtURL:    req.GuideArtURL,
			Timezone:       req.Timezone,
			ScheduleID:     req.ScheduleID,
			BaseScheduleID: req.BaseScheduleID,
			CreatedAt:      now,
			UpdatedAt:      now,
		}
		err := db.Exec(r.Context(),
			`INSERT INTO channel (id, tenant_id, number, name, guide_art_url, timezone, schedule_id, base_schedule_id, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			ch.ID, ch.TenantID, ch.Number, ch.Name, ch.GuideArtURL, ch.Timezone, ch.ScheduleID, ch.BaseScheduleID, ch.CreatedAt, ch.UpdatedAt,
		)
		if err != nil {
			handleDBError(w, err)
			return
		}
		if ch.ScheduleID != nil || ch.BaseScheduleID != nil {
			mat.ResetAfterChannelEdit(r.Context(), DefaultTenantID, ch)
		}
		if loopItemID != "" {
			writeJSON(w, http.StatusCreated, channelLoopResponse{Channel: ch, ScheduleID: loopScheduleID, ScheduleItemID: loopItemID})
			return
		}
		writeJSON(w, http.StatusCreated, ch)
	}
}

func listChannels(db storage.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var channels []domain.Channel
		err := db.Query(r.Context(), &channels,
			"SELECT "+channelColumns+" FROM channel WHERE tenant_id = ? ORDER BY number",
			DefaultTenantID,
		)
		if err != nil {
			handleDBError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, channels)
	}
}

func getChannel(db storage.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ch, err := fetchChannel(db, r, r.PathValue("id"))
		if err != nil {
			handleDBError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, ch)
	}
}

func updateChannel(db storage.DB, mat *materializer.Materializer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		before, err := fetchChannel(db, r, id)
		if err != nil {
			handleDBError(w, err)
			return
		}

		var req channelRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if req.Name == "" {
			writeError(w, http.StatusBadRequest, "name is required")
			return
		}
		if req.Timezone == "" {
			req.Timezone = "UTC"
		}
		if err := ValidateChannelSchedules(r.Context(), db, req.ScheduleID, req.BaseScheduleID); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if req.Loop != nil && req.ScheduleID != nil {
			writeError(w, http.StatusBadRequest, "loop and schedule_id are mutually exclusive: the loop creates its own schedule")
			return
		}

		now := time.Now().UnixMilli()
		var loopScheduleID, loopItemID string
		if req.Loop != nil {
			// A loop only fills a channel that has no schedule of its own: an
			// existing schedule is programming the author wrote, and replacing
			// it silently would be a trap.
			if before.ScheduleID != nil {
				writeError(w, http.StatusConflict, "this channel already has a schedule; edit it in the schedule editor instead of adding a loop")
				return
			}
			sched, item, err := CreateLoop(r.Context(), db, *req.Loop, req.Name, now)
			if err != nil {
				writeLoopError(w, err)
				return
			}
			loopScheduleID, loopItemID = sched, item
			req.ScheduleID = &loopScheduleID
		}

		err = db.Exec(r.Context(),
			`UPDATE channel SET number = ?, name = ?, guide_art_url = ?, timezone = ?, schedule_id = ?, base_schedule_id = ?, updated_at = ?
			 WHERE tenant_id = ? AND id = ?`,
			req.Number, req.Name, req.GuideArtURL, req.Timezone, req.ScheduleID, req.BaseScheduleID, now, DefaultTenantID, id,
		)
		if err != nil {
			handleDBError(w, err)
			return
		}

		ch, err := fetchChannel(db, r, id)
		if err != nil {
			handleDBError(w, err)
			return
		}
		// PUT is a full replace (same semantics createChannel/updateBlock
		// already have) — a schedule_id change either way (assigned, cleared,
		// or swapped) needs a regeneration; comparing before/after avoids
		// triggering one on every unrelated field edit (e.g. renaming a
		// channel that has no schedule at all).
		scheduleChanged := !sameStringPtr(before.ScheduleID, ch.ScheduleID) ||
			!sameStringPtr(before.BaseScheduleID, ch.BaseScheduleID)
		if scheduleChanged {
			mat.ResetAfterChannelEdit(r.Context(), DefaultTenantID, ch)
		}
		if loopItemID != "" {
			writeJSON(w, http.StatusOK, channelLoopResponse{Channel: ch, ScheduleID: loopScheduleID, ScheduleItemID: loopItemID})
			return
		}
		writeJSON(w, http.StatusOK, ch)
	}
}

func deleteChannel(db storage.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if _, err := fetchChannel(db, r, id); err != nil {
			handleDBError(w, err)
			return
		}
		if err := db.Exec(r.Context(), "DELETE FROM channel WHERE tenant_id = ? AND id = ?", DefaultTenantID, id); err != nil {
			handleDBError(w, err)
			return
		}
		if err := deleteWatermarkOf(r.Context(), db, "channel", id); err != nil {
			handleDBError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// resetChannelEPG force-resets one channel's grid: the materialized future and
// the program currently on the air are discarded, continuous rotations restart
// from the beginning, and the channel is regenerated before the response is
// sent. Viewers switch program immediately.
func resetChannelEPG(db storage.DB, mat *materializer.Materializer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ch, err := fetchChannel(db, r, r.PathValue("id"))
		if err != nil {
			handleDBError(w, err)
			return
		}
		if err := mat.ResetChannel(r.Context(), DefaultTenantID, ch); err != nil {
			writeError(w, http.StatusInternalServerError, "could not reset the channel grid")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func sameStringPtr(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// writeLoopError answers a failed createLoop: 400 with the message, since every
// error it returns is the author's input.
func writeLoopError(w http.ResponseWriter, err error) {
	writeError(w, http.StatusBadRequest, err.Error())
}
