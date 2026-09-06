package engine

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ghiac/agentize/model"
	"github.com/sashabaranov/go-openai"
)

// ManageContextToolDefinition exposes bounded durable-memory operations.
// Identity is injected by the engine and never accepted from model arguments.
func ManageContextToolDefinition() openai.Tool {
	return openai.Tool{Type: openai.ToolTypeFunction, Function: &openai.FunctionDefinition{
		Name:        "manage_context",
		Description: "Read or update durable memory. scope=user is cross-conversation memory; scope=session is this conversation's title/summary/tags. Prefer updating or deleting existing facts over adding near-duplicates. Summary is capped at 20 facts.",
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"action": map[string]interface{}{
					"type": "string",
					"enum": []string{"get", "add_summary", "set_summary", "remove_summary", "add_tags", "set_tags", "remove_tag", "edit_tag"},
				},
				"scope":     map[string]interface{}{"type": "string", "enum": []string{"user", "session"}},
				"entries":   map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}, "description": "Summary facts for add_summary or the complete list for set_summary (max 20)."},
				"index":     map[string]interface{}{"type": "integer", "description": "0-based summary index for remove_summary."},
				"tags":      map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}, "description": "Tags for add_tags or the complete list for set_tags."},
				"tag":       map[string]interface{}{"type": "string", "description": "Tag to remove."},
				"old_tag":   map[string]interface{}{"type": "string", "description": "Existing tag to rename (edit_tag)."},
				"new_tag":   map[string]interface{}{"type": "string", "description": "Replacement tag (edit_tag)."},
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
			if err := applyUserContextAction(user, action, args); err != nil {
				return "", err
			}
			if action != "get" {
				if err := e.Sessions.PutUser(user); err != nil {
					return "", err
				}
			}
			return contextJSON(user.ContextSummary, user.ContextTags, ""), nil
		}
		if scope != "session" {
			return "", fmt.Errorf("unsupported context scope %q", scope)
		}
		session, err := e.loadOwnedSession(userID, sessionID)
		if err != nil {
			return "", err
		}
		if session.UserID != userID {
			return "", fmt.Errorf("session not found")
		}
		if err := applySessionContextAction(session, action, args); err != nil {
			return "", err
		}
		if action != "get" {
			if err := e.Sessions.Put(session); err != nil {
				return "", err
			}
		}
		return contextJSON(session.Summary, session.Tags, session.Title), nil
	}
}

func applyUserContextAction(user *model.User, action string, args map[string]interface{}) error {
	return ApplyUserContextAction(user, action, args)
}

func applySessionContextAction(session *model.Session, action string, args map[string]interface{}) error {
	return ApplySessionContextAction(session, action, args)
}

// ApplyUserContextAction mutates user context for manage_context.
func ApplyUserContextAction(user *model.User, action string, args map[string]interface{}) error {
	switch action {
	case "get":
		return nil
	case "add_summary":
		user.ContextSummary = model.AppendSummaryEntries(user.ContextSummary, stringSliceArg(args, "entries")...)
	case "set_summary":
		user.ContextSummary = model.ReplaceSummaryEntries(stringSliceArg(args, "entries"))
	case "remove_summary":
		user.ContextSummary = model.RemoveSummaryEntry(user.ContextSummary, contextIntArg(args, "index"))
	case "add_tags":
		user.ContextTags = model.AppendTags(user.ContextTags, stringSliceArg(args, "tags"), model.MaxUserTags)
	case "set_tags":
		user.ContextTags = model.ReplaceTags(stringSliceArg(args, "tags"), model.MaxUserTags)
	case "remove_tag":
		user.ContextTags = model.RemoveTag(user.ContextTags, stringArg(args, "tag"))
	case "edit_tag":
		user.ContextTags = model.EditTag(user.ContextTags, stringArg(args, "old_tag"), stringArg(args, "new_tag"), model.MaxUserTags)
	default:
		return fmt.Errorf("unsupported context action %q", action)
	}
	return nil
}

// ApplySessionContextAction mutates session context for manage_context.
func ApplySessionContextAction(session *model.Session, action string, args map[string]interface{}) error {
	switch action {
	case "get":
		return nil
	case "add_summary":
		session.Summary = model.AppendSummaryEntries(session.Summary, stringSliceArg(args, "entries")...)
		session.SummaryInitialized = true
	case "set_summary":
		session.Summary = model.ReplaceSummaryEntries(stringSliceArg(args, "entries"))
		session.SummaryInitialized = true
	case "remove_summary":
		session.Summary = model.RemoveSummaryEntry(session.Summary, contextIntArg(args, "index"))
	case "add_tags":
		session.Tags = model.AppendTags(session.Tags, stringSliceArg(args, "tags"), model.MaxSessionTags)
	case "set_tags":
		session.Tags = model.ReplaceTags(stringSliceArg(args, "tags"), model.MaxSessionTags)
	case "remove_tag":
		session.Tags = model.RemoveTag(session.Tags, stringArg(args, "tag"))
	case "edit_tag":
		session.Tags = model.EditTag(session.Tags, stringArg(args, "old_tag"), stringArg(args, "new_tag"), model.MaxSessionTags)
	default:
		return fmt.Errorf("unsupported context action %q", action)
	}
	return nil
}

func contextIntArg(args map[string]interface{}, key string) int {
	switch v := args[key].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case json.Number:
		n, _ := v.Int64()
		return int(n)
	default:
		return -1
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
