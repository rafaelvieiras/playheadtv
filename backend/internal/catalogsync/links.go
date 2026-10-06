package catalogsync

import (
	"context"
	"encoding/json"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
	"github.com/rafaelvieiras/playheadtv/backend/internal/storage"
)

// MaxPeople caps the people stored per item (directors and main cast
// together, directors first), keeping the person link table small on large
// libraries. Enforced here so every sync path, push and pull, obeys it.
const MaxPeople = 10

const (
	personKindActor    = "actor"
	personKindDirector = "director"
	langKindAudio      = "audio"
	langKindSubtitle   = "subtitle"
)

// linkSpec describes one dimension table and the link table pointing at it.
// The kind/role/position expressions let one generic diff handle link tables
// that do and don't carry those columns.
type linkSpec struct {
	link, dim, dimCol string
	kinded, ordered   bool
}

var (
	personLinks   = linkSpec{link: "catalog_item_person", dim: "person", dimCol: "person_id", kinded: true, ordered: true}
	studioLinks   = linkSpec{link: "catalog_item_studio", dim: "studio", dimCol: "studio_id"}
	tagLinks      = linkSpec{link: "catalog_item_tag", dim: "tag", dimCol: "tag_id"}
	genreLinks    = linkSpec{link: "catalog_item_genre", dim: "genre", dimCol: "genre_id"}
	languageLinks = linkSpec{link: "catalog_item_language", dim: "language", dimCol: "language_id", kinded: true}
)

// linkEntry is one desired link: a display name (the dimension row is keyed
// by its folded form), plus the optional per-link attributes.
type linkEntry struct {
	name     string
	kind     string
	role     string
	position int64
}

func (e linkEntry) norm() string { return FoldText(e.name) }

func (e linkEntry) key() string { return e.norm() + "\x00" + e.kind }

type existingLink struct {
	NameNorm string
	Kind     string
	Role     *string
	Position int64
}

func marshalOptionalJSON[T any](v []T) (*string, error) {
	if len(v) == 0 {
		return nil, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	s := string(b)
	return &s, nil
}

// maxSegments caps the media segments stored per item.
const maxSegments = 50

// cleanSegments keeps the well-formed segments of an item (known type, start
// inside the item, end after the start), clipped to the duration and ordered by
// start, at most maxSegments. nil stays nil ("not looked up"); an input with
// nothing usable becomes an empty, non-nil slice.
func cleanSegments(in []domain.CatalogSegment, durationMs int64) []domain.CatalogSegment {
	if in == nil {
		return nil
	}
	out := []domain.CatalogSegment{}
	for _, s := range in {
		s.Type = strings.ToLower(strings.TrimSpace(s.Type))
		switch s.Type {
		case domain.SegmentIntro, domain.SegmentOutro, domain.SegmentRecap, domain.SegmentPreview, domain.SegmentCommercial:
		default:
			continue
		}
		if s.StartMs < 0 || s.EndMs <= s.StartMs || (durationMs > 0 && s.StartMs >= durationMs) {
			continue
		}
		if durationMs > 0 && s.EndMs > durationMs {
			s.EndMs = durationMs
		}
		out = append(out, s)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].StartMs < out[j].StartMs })
	if len(out) > maxSegments {
		out = out[:maxSegments]
	}
	return out
}

// marshalSegments encodes an item's media segments: nil is NULL ("not looked
// up"), an empty non-nil slice is '[]' ("looked up, none").
func marshalSegments(v []domain.CatalogSegment) (*string, error) {
	if v == nil {
		return nil, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	s := string(b)
	return &s, nil
}

// plainEntries trims, drops blanks and dedupes (by folded name) a list of
// names for an unkinded link table.
func plainEntries(names []string, kind string) []linkEntry {
	var out []linkEntry
	for _, n := range names {
		n = strings.TrimSpace(n)
		if n == "" {
			continue
		}
		out = append(out, linkEntry{name: n, kind: kind})
	}
	return out
}

// splitGenres turns the comma-separated genres column value into names.
func splitGenres(genres string) []string {
	if genres == "" {
		return nil
	}
	return strings.Split(genres, ",")
}

// peopleEntries applies MaxPeople: directors first, then main cast in the
// order given, until the cap is reached.
func peopleEntries(directors, cast []domain.CatalogPerson) []linkEntry {
	var out []linkEntry
	add := func(people []domain.CatalogPerson, kind string) {
		for _, p := range people {
			if len(out) >= MaxPeople {
				return
			}
			name := strings.TrimSpace(p.Name)
			if name == "" {
				continue
			}
			role := ""
			if kind == personKindActor {
				role = strings.TrimSpace(p.Role)
			}
			out = append(out, linkEntry{name: name, kind: kind, role: role})
		}
	}
	add(directors, personKindDirector)
	add(cast, personKindActor)
	return out
}

func syncLinks(ctx context.Context, db storage.DB, tenantID, itemID string, item Item) error {
	var langs []linkEntry
	langs = append(langs, plainEntries(item.AudioLanguages, langKindAudio)...)
	langs = append(langs, plainEntries(item.SubtitleLanguages, langKindSubtitle)...)
	jobs := []struct {
		spec    linkSpec
		entries []linkEntry
	}{
		{personLinks, peopleEntries(item.Directors, item.Cast)},
		{studioLinks, plainEntries(item.Studios, "")},
		{tagLinks, plainEntries(item.Tags, "")},
		{genreLinks, plainEntries(splitGenres(item.Genres), "")},
		{languageLinks, langs},
	}
	for _, j := range jobs {
		if err := syncOneLink(ctx, db, tenantID, itemID, j.spec, j.entries); err != nil {
			return err
		}
	}
	return nil
}

// syncOneLink diffs the item's existing links in one link table against the
// desired entries: stale links are deleted, new ones inserted (creating the
// dimension row on first sight) and changed role/position updated. Unchanged
// links cost no write.
func syncOneLink(ctx context.Context, db storage.DB, tenantID, itemID string, spec linkSpec, entries []linkEntry) error {
	desired := make(map[string]linkEntry, len(entries))
	var order []string
	for _, e := range entries {
		if e.norm() == "" {
			continue
		}
		if _, dup := desired[e.key()]; dup {
			continue
		}
		e.position = int64(len(order))
		desired[e.key()] = e
		order = append(order, e.key())
	}

	kindExpr, roleExpr, posExpr := "''", "NULL", "0"
	if spec.kinded {
		kindExpr = "l.kind"
	}
	if spec.ordered {
		roleExpr, posExpr = "l.role", "l.position"
	}
	var existing []existingLink
	if err := db.Query(ctx, &existing,
		"SELECT d.name_norm AS name_norm, "+kindExpr+" AS kind, "+roleExpr+" AS role, "+posExpr+" AS position"+
			" FROM "+spec.link+" l JOIN "+spec.dim+" d ON d.tenant_id = l.tenant_id AND d.id = l."+spec.dimCol+
			" WHERE l.tenant_id = ? AND l.catalog_item_id = ?",
		tenantID, itemID,
	); err != nil {
		return err
	}
	have := make(map[string]existingLink, len(existing))
	for _, ex := range existing {
		k := ex.NameNorm + "\x00" + ex.Kind
		have[k] = ex
		if _, keep := desired[k]; keep {
			continue
		}
		q := "DELETE FROM " + spec.link + " WHERE tenant_id = ? AND catalog_item_id = ? AND " + spec.dimCol +
			" = (SELECT id FROM " + spec.dim + " WHERE tenant_id = ? AND name_norm = ?)"
		args := []any{tenantID, itemID, tenantID, ex.NameNorm}
		if spec.kinded {
			q += " AND kind = ?"
			args = append(args, ex.Kind)
		}
		if err := db.Exec(ctx, q, args...); err != nil {
			return err
		}
	}

	for _, k := range order {
		e := desired[k]
		if ex, ok := have[k]; ok {
			if !spec.ordered {
				continue
			}
			exRole := ""
			if ex.Role != nil {
				exRole = *ex.Role
			}
			if exRole == e.role && ex.Position == e.position {
				continue
			}
			if err := db.Exec(ctx,
				"UPDATE "+spec.link+" SET role = ?, position = ? WHERE tenant_id = ? AND catalog_item_id = ? AND kind = ? AND "+spec.dimCol+
					" = (SELECT id FROM "+spec.dim+" WHERE tenant_id = ? AND name_norm = ?)",
				nullIfEmpty(e.role), e.position, tenantID, itemID, e.kind, tenantID, e.norm(),
			); err != nil {
				return err
			}
			continue
		}
		if err := db.Exec(ctx,
			"INSERT INTO "+spec.dim+" (tenant_id, id, name, name_norm) VALUES (?, ?, ?, ?) ON CONFLICT (tenant_id, name_norm) DO NOTHING",
			tenantID, uuid.NewString(), e.name, e.norm(),
		); err != nil {
			return err
		}
		cols := "tenant_id, catalog_item_id, " + spec.dimCol
		sel := "?, ?, id"
		args := []any{tenantID, itemID}
		if spec.kinded {
			cols += ", kind"
			sel += ", ?"
			args = append(args, e.kind)
		}
		if spec.ordered {
			cols += ", role, position"
			sel += ", ?, ?"
			args = append(args, nullIfEmpty(e.role), e.position)
		}
		args = append(args, tenantID, e.norm())
		if err := db.Exec(ctx,
			"INSERT INTO "+spec.link+" ("+cols+") SELECT "+sel+" FROM "+spec.dim+" WHERE tenant_id = ? AND name_norm = ?",
			args...,
		); err != nil {
			return err
		}
	}
	return nil
}

func nullIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
