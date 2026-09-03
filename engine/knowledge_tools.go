package engine

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/ghiac/agentize/model"
	"github.com/sashabaranov/go-openai"
)

const maxKnowledgeResultChars = 12000

func ManageKnowledgeToolDefinition() openai.Tool {
	return openai.Tool{Type: openai.ToolTypeFunction, Function: &openai.FunctionDefinition{
		Name:        "manage_knowledge",
		Description: "Discover and read the knowledge tree on demand. list/search return metadata, get reads one node without activating it, open reads it and adds that node's tools to any already-open nodes (it does not close previous nodes), and close deactivates one node. A compact usage catalog of currently open nodes is kept in the system prompt. Full node content is returned by get/open, not copied into the prompt. This is not the user's product memory or trade journal.",
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"action": map[string]interface{}{"type": "string", "enum": []string{"list", "search", "get", "open", "close"}},
				"path":   map[string]interface{}{"type": "string", "description": "Exact node path; list defaults to root."},
				"query":  map[string]interface{}{"type": "string", "description": "Case-insensitive search text for path/title/description/summary."},
				"limit":  map[string]interface{}{"type": "integer", "minimum": 1, "maximum": 50},
				"offset": map[string]interface{}{"type": "integer", "minimum": 0},
			},
			"required": []string{"action"},
		},
	}}
}

func (e *Engine) RegisterManageKnowledgeTool() {
	if e.Functions == nil || e.Repo == nil {
		return
	}
	_ = e.Functions.RegisterOrReplace("manage_knowledge", "Manage Knowledge", e.manageKnowledgeFunction())
}

func (e *Engine) manageKnowledgeFunction() model.ToolFunction {
	return func(args map[string]interface{}) (string, error) {
		action := stringArg(args, "action")
		path := strings.TrimSpace(stringArg(args, "path"))
		if path == "" {
			path = "root"
		}
		sessionID, _ := args["__session_id__"].(string)
		userID, _ := args["__user_id__"].(string)
		switch action {
		case "get", "open":
			node, err := e.Repo.LoadNode(path)
			if err != nil {
				return "", err
			}
			if action == "open" {
				if sessionID == "" {
					return "", fmt.Errorf("session identity is unavailable")
				}
				if _, err := e.openFile(userID, sessionID, path); err != nil {
					return "", err
				}
			}
			payload := map[string]interface{}{"path": node.Path, "title": node.Title, "description": node.Description, "summary": node.Summary, "content": node.Content}
			if action == "open" {
				payload["opened"] = true
				payload["activated_tools"] = activeNodeToolNames(node)
				payload["closed_previous"] = false
				if session, getErr := e.loadOwnedSession(userID, sessionID); getErr == nil && session != nil {
					openPaths := make([]string, 0, len(session.NodeDigests))
					for _, digest := range session.NodeDigests {
						if digest.Path != "" {
							openPaths = append(openPaths, digest.Path)
						}
					}
					payload["open_nodes"] = openPaths
				}
			}
			return boundedKnowledgeJSON(payload), nil
		case "close":
			if sessionID == "" {
				return "", fmt.Errorf("session identity is unavailable")
			}
			if err := e.closeFile(userID, sessionID, path); err != nil {
				return "", err
			}
			return boundedKnowledgeJSON(map[string]interface{}{"path": path, "opened": false}), nil
		case "list", "search":
			query := strings.ToLower(strings.TrimSpace(stringArg(args, "query")))
			if action == "search" && query == "" {
				return "", fmt.Errorf("query is required for search")
			}
			var nodes []map[string]interface{}
			e.walkKnowledge(path, func(node *model.Node) {
				haystack := strings.ToLower(strings.Join([]string{node.Path, node.Title, node.Description, node.Summary}, " "))
				if action == "list" || strings.Contains(haystack, query) {
					nodes = append(nodes, map[string]interface{}{"path": node.Path, "title": node.Title, "description": node.Description, "summary": node.Summary, "tools": activeNodeToolNames(node)})
				}
			})
			sort.Slice(nodes, func(i, j int) bool { return nodes[i]["path"].(string) < nodes[j]["path"].(string) })
			offset, limit := intArg(args, "offset"), intArg(args, "limit")
			if limit == 0 {
				limit = 25
			}
			if limit < 1 {
				limit = 25
			}
			if limit > 50 {
				limit = 50
			}
			if offset < 0 {
				offset = 0
			}
			if offset > len(nodes) {
				offset = len(nodes)
			}
			end := offset + limit
			if end > len(nodes) {
				end = len(nodes)
			}
			return boundedKnowledgeJSON(map[string]interface{}{"items": nodes[offset:end], "offset": offset, "limit": limit, "total": len(nodes)}), nil
		default:
			return "", fmt.Errorf("unsupported knowledge action %q", action)
		}
	}
}

func (e *Engine) walkKnowledge(path string, visit func(*model.Node)) {
	node, err := e.Repo.LoadNode(path)
	if err != nil {
		return
	}
	visit(node)
	children, err := e.Repo.GetChildren(path)
	if err != nil {
		return
	}
	for _, child := range children {
		e.walkKnowledge(child, visit)
	}
}

func activeNodeToolNames(node *model.Node) []string {
	var names []string
	for _, tool := range node.Tools {
		if tool.Status == model.ToolStatusActive {
			names = append(names, tool.Name)
		}
	}
	sort.Strings(names)
	return names
}

func boundedKnowledgeJSON(value interface{}) string {
	b, _ := json.Marshal(value)
	if len(b) <= maxKnowledgeResultChars {
		return string(b)
	}
	return string(b[:maxKnowledgeResultChars]) + "..."
}

type knowledgeToolHit struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Path        string `json:"path"`
}

func (e *Engine) searchKnowledgeToolCatalog(query string, limit int) []knowledgeToolHit {
	query = strings.ToLower(strings.TrimSpace(query))
	if limit <= 0 {
		limit = toolCatalogSearchLimit
	}
	if query == "" || e == nil || e.Repo == nil {
		return nil
	}
	tokens := tokenizeQuery(query)
	type scored struct {
		hit   knowledgeToolHit
		score int
	}
	var ranked []scored
	e.walkKnowledge("root", func(node *model.Node) {
		for _, tool := range node.Tools {
			if tool.Status != model.ToolStatusActive || platformKnowledgeToolName(tool.Name) {
				continue
			}
			hay := strings.ToLower(tool.Name + " " + tool.Description + " " + node.Path + " " + node.Title)
			score := 0
			if strings.Contains(hay, query) {
				score += 8
			}
			for _, tok := range tokens {
				if strings.Contains(strings.ToLower(tool.Name), tok) {
					score += 4
				} else if strings.Contains(hay, tok) {
					score += 1
				}
			}
			if score == 0 {
				continue
			}
			ranked = append(ranked, scored{hit: knowledgeToolHit{Name: tool.Name, Description: tool.Description, Path: node.Path}, score: score})
		}
	})
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].score != ranked[j].score {
			return ranked[i].score > ranked[j].score
		}
		if ranked[i].hit.Path != ranked[j].hit.Path {
			return ranked[i].hit.Path < ranked[j].hit.Path
		}
		return ranked[i].hit.Name < ranked[j].hit.Name
	})
	if len(ranked) > limit {
		ranked = ranked[:limit]
	}
	hits := make([]knowledgeToolHit, len(ranked))
	for i, item := range ranked {
		hits[i] = item.hit
	}
	return hits
}

func formatKnowledgeSearchToolsResult(hits []knowledgeToolHit) string {
	payload := map[string]interface{}{
		"matches": hits,
		"hint":    "Open the listed path with open_node (or manage_knowledge action=open) to activate those tools. They are not loaded until that node is opened.",
	}
	if len(hits) == 0 {
		payload["hint"] = "No matching knowledge-tree tools. Try a shorter capability word such as chart, order, news, or browser."
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return fmt.Sprintf(`{"matches":[],"error":%q}`, err.Error())
	}
	return string(raw)
}
