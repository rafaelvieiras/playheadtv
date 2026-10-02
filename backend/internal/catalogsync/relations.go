package catalogsync

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
	"github.com/rafaelvieiras/playheadtv/backend/internal/storage"
)

// ListColumns are the catalog_item columns GET /catalog-items and the MCP
// search tool read: every scalar column, no JSON columns.
const ListColumns = "id, tenant_id, media_source_id, external_id, kind, title, series_title, duration_ms, source_modified_at, synced_at, genres, production_year," +
	" season_number, episode_number, episode_number_end, series_external_id, season_name, original_title, sort_title, premiere_date, date_added," +
	" overview, tagline, content_rating, community_rating, critic_rating, imdb_id, tmdb_id, tvdb_id," +
	" width, height, video_codec, container, bitrate," +
	" image_primary_tag, image_backdrop_tag, image_thumb_tag, series_image_primary_tag"

// DetailColumns adds the two JSON columns (aliased to the *_json fields of
// domain.CatalogItem) for the single-item endpoint.
const DetailColumns = ListColumns + ", production_locations AS locations_json, chapters AS chapters_json"

type relationRow struct {
	CatalogItemID string
	Name          string
	Kind          string
	Role          *string
	Position      int64
}

// LoadRelations fills the list fields of items from the link tables with one
// query per link table for the whole slice (not per item). With detail=false
// only the compact string lists (studios, tags, audio/subtitle languages)
// are loaded. detail=true additionally loads cast and directors and decodes
// production locations and chapters, which are only on the single-item
// endpoint to keep list payloads small.
func LoadRelations(ctx context.Context, db storage.DB, tenantID string, items []domain.CatalogItem, detail bool) error {
	if len(items) == 0 {
		return nil
	}
	index := make(map[string]int, len(items))
	ph := make([]string, len(items))
	args := []any{tenantID}
	for i, it := range items {
		index[it.ID] = i
		ph[i] = "?"
		args = append(args, it.ID)
	}
	in := strings.Join(ph, ",")

	query := func(spec linkSpec, kindExpr, extra, order string) ([]relationRow, error) {
		var rows []relationRow
		err := db.Query(ctx, &rows,
			"SELECT l.catalog_item_id AS catalog_item_id, d.name AS name, "+kindExpr+" AS kind"+extra+
				" FROM "+spec.link+" l JOIN "+spec.dim+" d ON d.tenant_id = l.tenant_id AND d.id = l."+spec.dimCol+
				" WHERE l.tenant_id = ? AND l.catalog_item_id IN ("+in+") ORDER BY "+order,
			args...)
		if err != nil {
			return nil, fmt.Errorf("catalogsync: load %s: %w", spec.link, err)
		}
		return rows, nil
	}

	rows, err := query(studioLinks, "''", "", "d.name_norm")
	if err != nil {
		return err
	}
	for _, r := range rows {
		it := &items[index[r.CatalogItemID]]
		it.Studios = append(it.Studios, r.Name)
	}
	if rows, err = query(tagLinks, "''", "", "d.name_norm"); err != nil {
		return err
	}
	for _, r := range rows {
		it := &items[index[r.CatalogItemID]]
		it.Tags = append(it.Tags, r.Name)
	}
	if rows, err = query(languageLinks, "l.kind", "", "d.name_norm"); err != nil {
		return err
	}
	for _, r := range rows {
		it := &items[index[r.CatalogItemID]]
		if r.Kind == langKindAudio {
			it.AudioLanguages = append(it.AudioLanguages, r.Name)
		} else {
			it.SubtitleLanguages = append(it.SubtitleLanguages, r.Name)
		}
	}
	if !detail {
		return nil
	}

	if rows, err = query(personLinks, "l.kind", ", l.role AS role, l.position AS position", "l.position"); err != nil {
		return err
	}
	for _, r := range rows {
		it := &items[index[r.CatalogItemID]]
		if r.Kind == personKindDirector {
			it.Directors = append(it.Directors, domain.CatalogPerson{Name: r.Name, Type: "Director"})
			continue
		}
		p := domain.CatalogPerson{Name: r.Name, Type: "Actor"}
		if r.Role != nil {
			p.Role = *r.Role
		}
		it.Cast = append(it.Cast, p)
	}
	for i := range items {
		it := &items[i]
		if it.LocationsJSON != nil {
			if err := json.Unmarshal([]byte(*it.LocationsJSON), &it.Locations); err != nil {
				return fmt.Errorf("catalogsync: decode production_locations of %s: %w", it.ID, err)
			}
		}
		if it.ChaptersJSON != nil {
			if err := json.Unmarshal([]byte(*it.ChaptersJSON), &it.Chapters); err != nil {
				return fmt.Errorf("catalogsync: decode chapters of %s: %w", it.ID, err)
			}
		}
	}
	return nil
}
