package materializer

import (
	"context"
	"encoding/json"
	"log"

	"github.com/rafaelvieiras/playheadtv/backend/internal/cutplan"
	"github.com/rafaelvieiras/playheadtv/backend/internal/cutruleset"
	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
	"github.com/rafaelvieiras/playheadtv/backend/internal/storage"
)

// passCutPlanner is an optional extension of CutPlanner: a planner that holds
// state worth loading once per pass (rule sets read from storage) hands the
// materializer a snapshot for each channel it is about to expand, instead of
// being queried per item. nil means no cuts for that tenant.
type passCutPlanner interface {
	BeginPass(ctx context.Context, tenantID string) CutPlanner
}

// RuleSetCuts returns the planner backed by the stored cut rule sets
// (cut_rule_set): the set governing an airing is the block's own, else its
// channel's (cutplan.PickSet), and its rules decide the offsets
// (cutplan.Planner). An item that no set governs costs nothing; a set that can
// use chapters or segments makes one indexed read per distinct item. A failing lookup is
// logged and means no cuts for that item, never a failed materialization.
func RuleSetCuts(db storage.DB) CutPlanner { return ruleSetCuts{db: db} }

type ruleSetCuts struct{ db storage.DB }

// PlanCuts on the bare planner never cuts: it has no tenant or snapshot. The
// materializer always goes through BeginPass.
func (ruleSetCuts) PlanCuts(CutItem) []int64 { return nil }

// BeginPass loads the tenant's rule sets once and returns the snapshot that
// answers PlanCuts for the channel being expanded.
func (r ruleSetCuts) BeginPass(ctx context.Context, tenantID string) CutPlanner {
	sets, err := cutruleset.List(ctx, r.db, tenantID)
	if err != nil {
		log.Printf("materializer: tenant %s: load cut rule sets: %v (no cuts this pass)", tenantID, err)
		return nil
	}
	if len(sets) == 0 {
		return nil
	}
	return &ruleSetSnapshot{ctx: ctx, db: r.db, sets: sets, items: map[string]cutItemInfo{}}
}

type cutItemInfo struct {
	ok       bool
	kind     domain.CatalogItemKind
	chapters []domain.CatalogChapter
	segments []domain.CatalogSegment
}

type ruleSetSnapshot struct {
	ctx   context.Context
	db    storage.DB
	sets  []domain.CutRuleSet
	items map[string]cutItemInfo
}

func (s *ruleSetSnapshot) PlanCuts(it CutItem) []int64 {
	set := cutplan.PickSet(s.sets, it.ChannelID, it.BlockID)
	if set == nil {
		return nil
	}
	info := s.item(it.CatalogItemID, usesChapters(set.CutConfig), cutplan.UsesSegments(set.CutConfig))
	if !info.ok {
		return nil
	}
	return cutplan.NewPlanner(*set).Plan(cutplan.Item{Kind: info.kind, DurationMs: it.DurationMs, Chapters: info.chapters, Segments: info.segments}).CutsMs
}

// item reads the kind (and the chapters and media segments, when wanted) of one
// catalog item, memoized for the snapshot. A set that never looks at chapters or
// segments skips those columns.
func (s *ruleSetSnapshot) item(id string, withChapters, withSegments bool) cutItemInfo {
	key := id
	if withChapters {
		key += "#c"
	}
	if withSegments {
		key += "#s"
	}
	if info, ok := s.items[key]; ok {
		return info
	}
	var info cutItemInfo
	cols := "kind"
	if withChapters {
		cols += ", chapters AS chapters_json"
	}
	if withSegments {
		cols += ", media_segments AS segments_json"
	}
	var rows []struct {
		Kind         string
		ChaptersJSON *string
		SegmentsJSON *string
	}
	if err := s.db.Query(s.ctx, &rows, "SELECT "+cols+" FROM catalog_item WHERE id = ?", id); err != nil {
		log.Printf("materializer: cut planner: load catalog_item %s: %v (item not cut)", id, err)
	} else if len(rows) > 0 {
		info.ok, info.kind = true, domain.CatalogItemKind(rows[0].Kind)
		if rows[0].ChaptersJSON != nil {
			if err := json.Unmarshal([]byte(*rows[0].ChaptersJSON), &info.chapters); err != nil {
				log.Printf("materializer: cut planner: decode chapters of %s: %v (planned without chapters)", id, err)
				info.chapters = nil
			}
		}
		if rows[0].SegmentsJSON != nil {
			if err := json.Unmarshal([]byte(*rows[0].SegmentsJSON), &info.segments); err != nil {
				log.Printf("materializer: cut planner: decode media segments of %s: %v (planned without segments)", id, err)
				info.segments = nil
			}
		}
	}
	s.items[key] = info
	return info
}

// usesChapters reports whether any rule of cfg can read an item's chapters.
func usesChapters(cfg domain.CutConfig) bool {
	chapterStrategy := func(s *domain.CutStrategy) bool { return s != nil && s.Type == domain.CutChapter }
	if chapterStrategy(cfg.Default) {
		return true
	}
	for i := range cfg.Rules {
		r := &cfg.Rules[i]
		if r.Match.HasChapters != nil || r.SnapToleranceMs > 0 || chapterStrategy(&r.Strategy) {
			return true
		}
	}
	return false
}
