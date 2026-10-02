package catalogsync

import (
	"context"
	"fmt"
	"strings"
	"unicode"

	"github.com/rafaelvieiras/playheadtv/backend/internal/storage"
)

// accentFold maps accented Latin letters (Latin-1 Supplement and the common
// Latin Extended-A ones) to their unaccented base. It is applied after
// lowercasing, so only lowercase forms are listed. Kept as a table instead of
// a Unicode decomposition to avoid pulling in golang.org/x/text for this.
var accentFold = strings.NewReplacer(
	"\u00e0", "a", "\u00e1", "a", "\u00e2", "a", "\u00e3", "a", "\u00e4", "a", "\u00e5", "a", "\u0101", "a", "\u0103", "a", "\u0105", "a",
	"\u00e7", "c", "\u0107", "c", "\u010d", "c", "\u0109", "c", "\u010b", "c",
	"\u010f", "d", "\u0111", "d",
	"\u00e8", "e", "\u00e9", "e", "\u00ea", "e", "\u00eb", "e", "\u0113", "e", "\u0115", "e", "\u0117", "e", "\u0119", "e", "\u011b", "e",
	"\u011d", "g", "\u011f", "g", "\u0121", "g", "\u0123", "g",
	"\u0125", "h", "\u0127", "h",
	"\u00ec", "i", "\u00ed", "i", "\u00ee", "i", "\u00ef", "i", "\u0129", "i", "\u012b", "i", "\u012d", "i", "\u012f", "i", "\u0131", "i",
	"\u0135", "j", "\u0137", "k",
	"\u013a", "l", "\u013c", "l", "\u013e", "l", "\u0142", "l",
	"\u00f1", "n", "\u0144", "n", "\u0146", "n", "\u0148", "n",
	"\u00f2", "o", "\u00f3", "o", "\u00f4", "o", "\u00f5", "o", "\u00f6", "o", "\u00f8", "o", "\u014d", "o", "\u014f", "o", "\u0151", "o",
	"\u0155", "r", "\u0157", "r", "\u0159", "r",
	"\u015b", "s", "\u015d", "s", "\u015f", "s", "\u0161", "s", "\u00df", "ss",
	"\u0163", "t", "\u0165", "t", "\u0167", "t",
	"\u00f9", "u", "\u00fa", "u", "\u00fb", "u", "\u00fc", "u", "\u0169", "u", "\u016b", "u", "\u016d", "u", "\u016f", "u", "\u0171", "u", "\u0173", "u",
	"\u0175", "w", "\u00fd", "y", "\u00ff", "y", "\u0177", "y",
	"\u017a", "z", "\u017c", "z", "\u017e", "z",
)

// FoldText lowercases s and strips accents from Latin letters, the single
// normalization behind catalog search: the stored search_text and the query
// are both folded with it, so "episodio" matches "Episódio" and vice versa.
func FoldText(s string) string {
	return accentFold.Replace(strings.Map(unicode.ToLower, s))
}

// SearchText builds the catalog_item.search_text value for a title and
// optional series title. A newline separates them so a query never matches
// across the boundary.
func SearchText(title string, seriesTitle *string) string {
	st := ""
	if seriesTitle != nil {
		st = *seriesTitle
	}
	return FoldText(title) + "\n" + FoldText(st)
}

// SearchClause returns the WHERE fragment (with a leading " AND") and its
// argument matching q against catalog_item.search_text, shared by
// GET /catalog-items and the MCP search_catalog tool.
func SearchClause(q string) (string, any) {
	return " AND search_text LIKE ?", "%" + FoldText(q) + "%"
}

type searchBackfillRow struct {
	ID          string
	Title       string
	SeriesTitle *string
}

// BackfillSearchText fills search_text for catalog_item rows that predate the
// column (search_text = ”). Safe to run on every startup: SearchText never
// returns an empty string, so a filled row is never picked up again.
func BackfillSearchText(ctx context.Context, db storage.DB) error {
	var rows []searchBackfillRow
	if err := db.Query(ctx, &rows, "SELECT id, title, series_title FROM catalog_item WHERE search_text = ''"); err != nil {
		return fmt.Errorf("catalogsync: list rows to backfill: %w", err)
	}
	for _, r := range rows {
		if err := db.Exec(ctx, "UPDATE catalog_item SET search_text = ? WHERE id = ?", SearchText(r.Title, r.SeriesTitle), r.ID); err != nil {
			return fmt.Errorf("catalogsync: backfill search_text for %s: %w", r.ID, err)
		}
	}
	return nil
}
