package mcpserver

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rafaelvieiras/playheadtv/backend/internal/cutruleset"
	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
	"github.com/rafaelvieiras/playheadtv/backend/internal/materializer"
	"github.com/rafaelvieiras/playheadtv/backend/internal/storage"
)

// registerCutRuleTools exposes the cut-point time model: create/list rule sets
// and preview where the cuts of a sample item fall. The EPG generation cuts
// items by the stored sets, so a write retriggers the materializer.
func registerCutRuleTools(s *mcp.Server, db storage.DB, mat *materializer.Materializer) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "create_cut_rule_set",
		Description: "Attach a cut rule set to a channel or block: an ordered list of rules (at most 20) that decide " +
			"where inside an item a break goes. The first rule whose match (kinds, duration range, has_chapters) fits " +
			"an item wins; its strategy is none, interval (every_ms), percentage (percentages), offsets (offsets_ms), " +
			"phased (phases, e.g. every 40 minutes until the last 20, then every 5) or chapter (roles middle/after_intro; " +
			"after_intro is decided by the end of the item's Jellyfin Intro media segment when it has one, else by the chapter " +
			"heuristic, else by the rule's fallback, and the plan's source says which, " +
			"with an optional fallback used when the item has no segment or chapters; intro segments are read from Jellyfin by " +
			"the pull sync, from the first sync after a set uses after_intro). Guard rails (minimum segment, distance from " +
			"start and end, max breaks) apply to every rule. Triggers EPG regeneration. " +
			"Mirrors POST /cut-rule-sets.",
	}, createCutRuleSetHandler(db, mat))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_cut_rule_sets",
		Description: "List every cut rule set for this tenant. Mirrors GET /cut-rule-sets.",
	}, listCutRuleSetsHandler(db))

	mcp.AddTool(s, &mcp.Tool{
		Name: "preview_cut_plan",
		Description: "Show where the cuts would fall for a sample item (kind, duration_ms, optional chapters and media segments) under a " +
			"list of cut rules, without storing anything. An intro segment takes priority over chapters for after_intro; the " +
			"result's source says what decided (rule, default, segment, chapter, fallback, builtin or none). Mirrors POST /cut-rule-sets/preview.",
	}, previewCutPlanHandler())
}

func createCutRuleSetHandler(db storage.DB, mat *materializer.Materializer) mcp.ToolHandlerFor[cutruleset.Input, domain.CutRuleSet] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in cutruleset.Input) (*mcp.CallToolResult, domain.CutRuleSet, error) {
		set, err := cutruleset.Create(ctx, db, defaultTenantID, in)
		if err != nil {
			return nil, domain.CutRuleSet{}, err
		}
		mat.Trigger()
		return nil, set, nil
	}
}

type listCutRuleSetsInput struct{}

type listCutRuleSetsOutput struct {
	RuleSets []domain.CutRuleSet `json:"rule_sets"`
}

func listCutRuleSetsHandler(db storage.DB) mcp.ToolHandlerFor[listCutRuleSetsInput, listCutRuleSetsOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, _ listCutRuleSetsInput) (*mcp.CallToolResult, listCutRuleSetsOutput, error) {
		sets, err := cutruleset.List(ctx, db, defaultTenantID)
		if err != nil {
			return nil, listCutRuleSetsOutput{}, err
		}
		return nil, listCutRuleSetsOutput{RuleSets: sets}, nil
	}
}

func previewCutPlanHandler() mcp.ToolHandlerFor[cutruleset.PreviewInput, cutruleset.PreviewResult] {
	return func(_ context.Context, _ *mcp.CallToolRequest, in cutruleset.PreviewInput) (*mcp.CallToolResult, cutruleset.PreviewResult, error) {
		res, err := cutruleset.Preview(in)
		if err != nil {
			return nil, cutruleset.PreviewResult{}, err
		}
		return nil, res, nil
	}
}
