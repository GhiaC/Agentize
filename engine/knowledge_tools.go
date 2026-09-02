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
		Description: "Discover and read the knowledge tree on demand. list/search return metadata, get reads one node without activating it, open reads it and activates only that node's tools for this session, and close deactivates them. Knowledge is never preloaded into the system prompt.",
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
				if _, err := e.OpenFile(sessionID, path); err != nil {
					return "", err
				}
			}
			payload := map[string]interface{}{"path": node.Path, "title": node.Title, "description": node.Description, "summary": node.Summary, "content": node.Content}
			if action == "open" {
				payload["opened"] = true
				payload["activated_tools"] = activeNodeToolNames(node)
			}
			return boundedKnowledgeJSON(payload), nil
		case "close":
			if sessionID == "" {
				return "", fmt.Errorf("session identity is unavailable")
			}
			if err := e.CloseFile(sessionID, path); err != nil {
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
