package mcpserver

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rafaelvieiras/playheadtv/backend/internal/cutruleset"
	"github.com/rafaelvieiras/playheadtv/backend/internal/domain"
	"github.com/rafaelvieiras/playheadtv/backend/internal/storage"
)

// registerCutRuleTools exposes the cut-point time model: create/list rule sets
// and preview where the cuts of a sample item fall. Rule sets are stored and
// validated but not yet consumed by the EPG generation.
func registerCutRuleTools(s *mcp.Server, db storage.DB) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "create_cut_rule_set",
		Description: "Attach a cut rule set to a channel or block: an ordered list of rules (at most 20) that decide " +
			"where inside an item a break goes. The first rule whose match (kinds, duration range, has_chapters) fits " +
			"an item wins; its strategy is none, interval (every_ms), percentage (percentages), offsets (offsets_ms), " +
			"phased (phases, e.g. every 40 minutes until the last 20, then every 5) or chapter (roles middle/after_intro, " +
			"with an optional fallback used when the item has no chapters). Guard rails (minimum segment, distance from " +
			"start and end, max breaks) apply to every rule. Stored and validated; not yet applied to the guide. " +
			"Mirrors POST /cut-rule-sets.",
	}, createCutRuleSetHandler(db))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_cut_rule_sets",
		Description: "List every cut rule set for this tenant. Mirrors GET /cut-rule-sets.",
	}, listCutRuleSetsHandler(db))

	mcp.AddTool(s, &mcp.Tool{
		Name: "preview_cut_plan",
		Description: "Show where the cuts would fall for a sample item (kind, duration_ms, optional chapters) under a " +
			"list of cut rules, without storing anything. Mirrors POST /cut-rule-sets/preview.",
	}, previewCutPlanHandler())
}

func createCutRuleSetHandler(db storage.DB) mcp.ToolHandlerFor[cutruleset.Input, domain.CutRuleSet] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in cutruleset.Input) (*mcp.CallToolResult, domain.CutRuleSet, error) {
		set, err := cutruleset.Create(ctx, db, defaultTenantID, in)
		if err != nil {
			return nil, domain.CutRuleSet{}, err
		}
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
