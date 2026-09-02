package engine

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/sashabaranov/go-openai"
)

func testTool(name, desc string) openai.Tool {
	return openai.Tool{
		Type: openai.ToolTypeFunction,
		Function: &openai.FunctionDefinition{
			Name:        name,
			Description: desc,
		},
	}
}

func TestToolsForLLMRequest_DeferredKeepsSessionCatalogAndSearch(t *testing.T) {
	all := []openai.Tool{
		testTool("get_price_history", "Price candles"),
		testTool("open_trade", "Open a position"),
		testTool("search_news", "Search news"),
		testTool("update_status", "Progress"),
		testTool("collect_result", "Read a buffered result"),
		testTool("open_node", "Open a knowledge node"),
	}
	got := toolsForLLMRequest(all, ToolCatalogDeferredSearch, nil)
	names := map[string]bool{}
	for _, tool := range got {
		names[toolName(tool)] = true
	}
	if !names["search_tools"] || !names["open_node"] || !names["open_trade"] || !names["get_price_history"] {
		t.Fatalf("deferred catalog missing opened/platform tools: %v", names)
	}
}

func TestAlwaysOnIncludesKnowledgeTreeTools(t *testing.T) {
	allow := alwaysOnToolNames()
	for _, name := range []string{"open_node", "close_node", "manage_knowledge", "search_tools"} {
		if !allow[name] {
			t.Errorf("always-on missing %s", name)
		}
	}
}

func TestToolsForLLMRequest_FullKeepsEverything(t *testing.T) {
	all := []openai.Tool{testTool("a", "A"), testTool("b", "B")}
	got := toolsForLLMRequest(all, ToolCatalogFull, nil)
	if len(got) != 2 {
		t.Fatalf("full catalog = %d, want 2", len(got))
	}
}

func TestSearchToolCatalog_RanksNameMatches(t *testing.T) {
	all := []openai.Tool{
		testTool("get_live_chart", "OHLCV candles for a symbol"),
		testTool("draw_chart_shape", "Persist a chart shape"),
		testTool("open_trade", "Open a leveraged position"),
		testTool("search_news", "Headline search"),
	}
	hits := searchToolCatalog(all, "chart", 8)
	if len(hits) < 2 {
		t.Fatalf("hits = %d, want at least the two chart tools", len(hits))
	}
	if hits[0].Name != "draw_chart_shape" && hits[0].Name != "get_live_chart" {
		t.Errorf("top hit = %s, want a chart tool", hits[0].Name)
	}
	for _, hit := range hits {
		if hit.Name == "open_trade" {
			t.Fatal("unrelated tool should not match chart")
		}
	}
}

func TestExecuteSearchTools_MergesDiscoveredNames(t *testing.T) {
	e := &Engine{}
	all := []openai.Tool{
		testTool("get_price_history", "Price history & chart candles"),
		testTool("open_trade", "Open a trade"),
	}
	result, discovered := e.executeSearchTools(all, `{"query":"price"}`, []string{"update_status"})
	if !strings.Contains(result, "get_price_history") {
		t.Fatalf("result missing match: %s", result)
	}
	var payload struct {
		Loaded []toolCatalogHit `json:"loaded"`
	}
	if err := json.Unmarshal([]byte(result), &payload); err != nil {
		t.Fatalf("result json: %v", err)
	}
	if len(payload.Loaded) == 0 {
		t.Fatal("expected loaded tools")
	}
	found := false
	for _, name := range discovered {
		if name == "get_price_history" {
			found = true
		}
	}
	if !found {
		t.Fatalf("discovered = %v, want get_price_history", discovered)
	}
}

func TestEffectiveToolCatalogMode_AutoThreshold(t *testing.T) {
	e := &Engine{toolCatalogMode: ToolCatalogAuto}
	if got := e.effectiveToolCatalogMode(toolCatalogAutoThreshold); got != ToolCatalogFull {
		t.Errorf("size=%d mode=%v, want Full", toolCatalogAutoThreshold, got)
	}
	if got := e.effectiveToolCatalogMode(toolCatalogAutoThreshold + 1); got != ToolCatalogDeferredSearch {
		t.Errorf("size=%d mode=%v, want DeferredSearch", toolCatalogAutoThreshold+1, got)
	}
}
