package engine

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ghiac/agentize/model"
	"github.com/sashabaranov/go-openai"
)

// ManageContextToolDefinition exposes bounded, append-only memory operations.
// Identity is injected by the engine and never accepted from model arguments.
func ManageContextToolDefinition() openai.Tool {
	return openai.Tool{Type: openai.ToolTypeFunction, Function: &openai.FunctionDefinition{
		Name:        "manage_context",
		Description: "Read or append durable memory. scope=user is cross-conversation memory; scope=session is the current conversation title/summary/tags. Prefer session scope for task-specific facts and user scope only for stable facts useful in future conversations.",
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"action":  map[string]interface{}{"type": "string", "enum": []string{"get", "add_summary", "add_tags"}},
				"scope":   map[string]interface{}{"type": "string", "enum": []string{"user", "session"}},
				"entries": map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}, "description": "New append-only summary facts."},
				"tags":    map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}, "description": "New tags."},
			},
			"required": []string{"action", "scope"},
		},
	}}
}

func (e *Engine) RegisterManageContextTool() {
	if e.Functions == nil || e.Sessions == nil {
		return
	}
	_ = e.Functions.RegisterOrReplace("manage_context", "Manage Context", e.manageContextFunction())
}

func (e *Engine) manageContextFunction() model.ToolFunction {
	return func(args map[string]interface{}) (string, error) {
		userID, _ := args["__user_id__"].(string)
		sessionID, _ := args["__session_id__"].(string)
		if userID == "" || sessionID == "" {
			return "", fmt.Errorf("context identity is unavailable")
		}
		action, scope := stringArg(args, "action"), stringArg(args, "scope")
		if scope == "user" {
			user, err := e.Sessions.GetOrCreateUser(userID)
			if err != nil {
				return "", err
			}
			switch action {
			case "get":
				return contextJSON(user.ContextSummary, user.ContextTags, ""), nil
			case "add_summary":
				user.ContextSummary = model.AppendSummaryEntries(user.ContextSummary, stringSliceArg(args, "entries")...)
			case "add_tags":
				user.ContextTags = model.AppendTags(user.ContextTags, stringSliceArg(args, "tags"), 20)
			default:
				return "", fmt.Errorf("unsupported context action %q", action)
			}
			if err := e.Sessions.PutUser(user); err != nil {
				return "", err
			}
			return contextJSON(user.ContextSummary, user.ContextTags, ""), nil
		}
		if scope != "session" {
			return "", fmt.Errorf("unsupported context scope %q", scope)
		}
		session, err := e.Sessions.Get(sessionID)
		if err != nil {
			return "", err
		}
		if session.UserID != userID {
			return "", fmt.Errorf("session not found")
		}
		switch action {
		case "get":
			return contextJSON(session.Summary, session.Tags, session.Title), nil
		case "add_summary":
			session.Summary = model.AppendSummaryEntries(session.Summary, stringSliceArg(args, "entries")...)
			session.SummaryInitialized = true
		case "add_tags":
			session.Tags = model.AppendTags(session.Tags, stringSliceArg(args, "tags"), 20)
		default:
			return "", fmt.Errorf("unsupported context action %q", action)
		}
		if err := e.Sessions.Put(session); err != nil {
			return "", err
		}
		return contextJSON(session.Summary, session.Tags, session.Title), nil
	}
}

func stringSliceArg(args map[string]interface{}, key string) []string {
	raw, ok := args[key].([]interface{})
	if !ok {
		if values, ok := args[key].([]string); ok {
			return values
		}
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, value := range raw {
		if s, ok := value.(string); ok && strings.TrimSpace(s) != "" {
			out = append(out, s)
		}
	}
	return out
}

func contextJSON(summary model.SummaryEntries, tags []string, title string) string {
	payload := map[string]interface{}{"summary": []string(summary), "tags": tags}
	if title != "" {
		payload["title"] = title
	}
	b, _ := json.Marshal(payload)
	return string(b)
}
