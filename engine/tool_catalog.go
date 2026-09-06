package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/ghiac/agentize/model"
	"github.com/sashabaranov/go-openai"
)

// ToolCatalogMode controls which tool schemas are sent to the LLM on each
// request. Historical tool_call / tool_result messages stay in the transcript
// either way; this only changes the top-level `tools` array for the current
// turn.
type ToolCatalogMode int

const (
	// ToolCatalogFull sends every registered schema on every LLM call.
	ToolCatalogFull ToolCatalogMode = iota
	// ToolCatalogAuto uses Full when the catalog is small and DeferredSearch
	// when it would otherwise drown the prompt (the 2026 default for large
	// tool libraries: Anthropic/OpenAI tool-search / defer_loading).
	ToolCatalogAuto
	// ToolCatalogDeferredSearch sends a small always-on set plus search_tools.
	// Matching schemas are loaded into later requests of the SAME user turn.
	ToolCatalogDeferredSearch
)

const (
	searchToolsName          = "search_tools"
	toolCatalogAutoThreshold = 12
	toolCatalogSearchLimit   = 8
)

// SetToolCatalogMode selects how tool schemas are exposed to the LLM.
func (e *Engine) SetToolCatalogMode(mode ToolCatalogMode) {
	if e == nil {
		return
	}
	e.toolCatalogMode = mode
}

func (e *Engine) effectiveToolCatalogMode(catalogSize int) ToolCatalogMode {
	if e == nil {
		return ToolCatalogFull
	}
	switch e.toolCatalogMode {
	case ToolCatalogDeferredSearch:
		return ToolCatalogDeferredSearch
	case ToolCatalogAuto:
		if catalogSize > toolCatalogAutoThreshold {
			return ToolCatalogDeferredSearch
		}
		return ToolCatalogFull
	default:
		return ToolCatalogFull
	}
}

func searchToolsDefinition() openai.Tool {
	return openai.Tool{
		Type: openai.ToolTypeFunction,
		Function: &openai.FunctionDefinition{
			Name: searchToolsName,
			Description: "Search the knowledge tree for tools that are not in the current list. " +
				"Results include the node path; call open_node (or manage_knowledge action=open) to activate those tools. " +
				"This does not search the user's product memory/journal.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"query": map[string]interface{}{
						"type":        "string",
						"description": "Short search query, e.g. a capability or tool name (price, chart, order, news).",
					},
				},
				"required": []string{"query"},
			},
		},
	}
}

func alwaysOnToolNames() map[string]bool {
	return map[string]bool{
		searchToolsName:    true,
		"collect_result":   true,
		"inspect_result":   true,
		"manage_files":     true,
		"manage_schedules": true,
		"open_node":        true,
		"close_node":       true,
		"manage_knowledge": true,
	}
}

func platformKnowledgeToolName(name string) bool {
	switch strings.TrimSpace(name) {
	case "open_file", "close_file", "open_node", "close_node", "manage_knowledge":
		return true
	default:
		return false
	}
}

func knowledgeCapabilityTool(name string) bool {
	return platformKnowledgeToolName(name)
}

func appendOpenAITool(tools []openai.Tool, extra openai.Tool) []openai.Tool {
	name := toolName(extra)
	if name == "" {
		return append(tools, extra)
	}
	for _, tool := range tools {
		if toolName(tool) == name {
			return tools
		}
	}
	return append(tools, extra)
}

func toolsForLLMRequest(all []openai.Tool, mode ToolCatalogMode, discovered []string) []openai.Tool {
	if mode != ToolCatalogDeferredSearch {
		return all
	}
	// GetTools is already session-scoped to opened nodes plus platform tools.
	// Keep that catalog visible and add search_tools so unopened capabilities
	// can be found by node path. discovered is unused: activating a node
	// refreshes GetTools instead of injecting foreign schemas.
	_ = discovered
	out := make([]openai.Tool, 0, len(all)+1)
	haveSearch := false
	seen := map[string]bool{}
	for _, tool := range all {
		name := toolName(tool)
		if name == searchToolsName {
			haveSearch = true
		}
		if name != "" && seen[name] {
			continue
		}
		if name != "" {
			seen[name] = true
		}
		out = append(out, tool)
	}
	if !haveSearch {
		out = append(out, searchToolsDefinition())
	}
	return out
}

func toolName(tool openai.Tool) string {
	if tool.Function == nil {
		return ""
	}
	return strings.TrimSpace(tool.Function.Name)
}

type toolCatalogHit struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

func searchToolCatalog(all []openai.Tool, query string, limit int) []toolCatalogHit {
	query = strings.ToLower(strings.TrimSpace(query))
	if limit <= 0 {
		limit = toolCatalogSearchLimit
	}
	if query == "" {
		return nil
	}
	tokens := tokenizeQuery(query)
	type scored struct {
		hit   toolCatalogHit
		score int
	}
	var ranked []scored
	for _, tool := range all {
		name := toolName(tool)
		if name == "" || name == searchToolsName {
			continue
		}
		desc := ""
		if tool.Function != nil {
			desc = tool.Function.Description
		}
		hay := strings.ToLower(name + " " + desc)
		score := 0
		if strings.Contains(hay, query) {
			score += 8
		}
		for _, tok := range tokens {
			if strings.Contains(strings.ToLower(name), tok) {
				score += 4
			} else if strings.Contains(hay, tok) {
				score += 1
			}
		}
		if score == 0 {
			continue
		}
		ranked = append(ranked, scored{hit: toolCatalogHit{Name: name, Description: desc}, score: score})
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].score != ranked[j].score {
			return ranked[i].score > ranked[j].score
		}
		return ranked[i].hit.Name < ranked[j].hit.Name
	})
	if len(ranked) > limit {
		ranked = ranked[:limit]
	}
	hits := make([]toolCatalogHit, len(ranked))
	for i, item := range ranked {
		hits[i] = item.hit
	}
	return hits
}

func tokenizeQuery(query string) []string {
	fields := strings.FieldsFunc(query, func(r rune) bool {
		return unicode.IsSpace(r) || r == ',' || r == ';' || r == '/'
	})
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		f = strings.ToLower(strings.TrimSpace(f))
		if f != "" {
			out = append(out, f)
		}
	}
	return out
}

func formatSearchToolsResult(hits []toolCatalogHit) string {
	payload := map[string]interface{}{
		"loaded": hits,
		"hint":   "These tools are now available for the rest of this user message. Call them directly.",
	}
	if len(hits) == 0 {
		payload["hint"] = "No matching tools. Try a shorter capability word such as chart, order, news, or browser."
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return fmt.Sprintf(`{"loaded":[],"error":%q}`, err.Error())
	}
	return string(raw)
}

func (e *Engine) executeSearchTools(all []openai.Tool, arguments string, discovered []string) (string, []string) {
	var args map[string]interface{}
	query := ""
	if json.Unmarshal([]byte(arguments), &args) == nil {
		query, _ = args["query"].(string)
	}
	if e != nil && e.Repo != nil {
		hits := e.searchKnowledgeToolCatalog(query, toolCatalogSearchLimit)
		return formatKnowledgeSearchToolsResult(hits), discovered
	}
	hits := searchToolCatalog(all, query, toolCatalogSearchLimit)
	for _, hit := range hits {
		discovered = appendUniqueTool(discovered, hit.Name)
	}
	return formatSearchToolsResult(hits), discovered
}

func (e *Engine) recordSearchToolsOnTurn(ctx context.Context, session *model.Session, messageID string, toolCall openai.ToolCall, result string) {
	persister := NewToolCallPersister(e.Sessions, "Engine")
	toolID := persister.SaveForTurn(session, messageID, UserMessageIDFrom(ctx), toolCall, "Search tools")
	persister.Update(session, messageID, toolID, result, nil)
	rec := turnRecorderFrom(ctx)
	rec.Tool(model.RouteNodeToolCall, searchToolsName, "Search tools", toolCall.Function.Arguments, model.RouteStatusOK, 0, toolID, toolCall.ID)
}

func appendUniqueTool(names []string, name string) []string {
	name = strings.TrimSpace(name)
	if name == "" {
		return names
	}
	for _, existing := range names {
		if existing == name {
			return names
		}
	}
	return append(names, name)
}
