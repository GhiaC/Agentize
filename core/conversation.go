package core

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ghiac/agentize/engine"
	"github.com/ghiac/agentize/log"
	"github.com/ghiac/agentize/model"
	"github.com/sashabaranov/go-openai"
)

const conversationSummaryPreviewLimit = 240
const conversationMessagePreviewLimit = 180
const conversationRecentMessageLimit = 4

func conversationToolDefs() []openai.Tool {
	return []openai.Tool{
		{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name: "list_conversations",
				Description: "List this user's conversations with each one's model, session, summary, and tags. " +
					"Treat every conversation as a user agent. Use this to decide whether a message belongs to the " +
					"current conversation or another one.",
				Parameters: map[string]interface{}{
					"type":       "object",
					"properties": map[string]interface{}{},
				},
			},
		},
		{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name: "get_conversation",
				Description: "Inspect one conversation like a user agent: model, session, summary, tags, and recent messages. " +
					"Use when matching an incoming message to a specific chat.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"conversation_id": map[string]interface{}{
							"type":        "string",
							"description": "Conversation ID from list_conversations",
						},
					},
					"required": []string{"conversation_id"},
				},
			},
		},
		{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name: "create_conversation",
				Description: "Create a new top-level conversation with its own session, optional model, and memory. " +
					"Makes it current. Use for a new topic that should not mix with existing chats.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"title": map[string]interface{}{"type": "string", "description": "Conversation title"},
						"model": map[string]interface{}{"type": "string", "description": "LLM model for this conversation's session"},
					},
				},
			},
		},
		{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        "select_conversation",
				Description: "Make a conversation current. Use before send_conversation when the message belongs to a different chat.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"conversation_id": map[string]interface{}{
							"type":        "string",
							"description": "Conversation ID from list_conversations",
						},
					},
					"required": []string{"conversation_id"},
				},
			},
		},
		{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name: "send_conversation",
				Description: "Send a message into a conversation's user-agent session. The reply is returned verbatim. " +
					"If conversation_id is omitted, uses the current conversation. If it names a different conversation, " +
					"that chat becomes current first, then the message is delivered.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"conversation_id": map[string]interface{}{
							"type":        "string",
							"description": "Optional; defaults to the current conversation. Passing another id switches current.",
						},
						"message": map[string]interface{}{
							"type":        "string",
							"description": "Message to send into the conversation session",
						},
					},
					"required": []string{"message"},
				},
			},
		},
		{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        "rename_conversation",
				Description: "Change only the conversation title. Does not change the id or model.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"conversation_id": map[string]interface{}{"type": "string"},
						"title":           map[string]interface{}{"type": "string"},
					},
					"required": []string{"conversation_id", "title"},
				},
			},
		},
		{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        "set_conversation_model",
				Description: "Change the LLM model for a conversation and its linked session. Does not change the title or id.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"conversation_id": map[string]interface{}{"type": "string"},
						"model":           map[string]interface{}{"type": "string", "description": "LLM model name for this conversation"},
					},
					"required": []string{"conversation_id", "model"},
				},
			},
		},
		{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        "archive_conversation",
				Description: "Archive or unarchive a conversation. Archived chats stay in the list but should not receive new messages unless the user asks to resume them.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"conversation_id": map[string]interface{}{"type": "string"},
						"archived": map[string]interface{}{
							"type":        "boolean",
							"description": "true to archive, false to restore",
						},
					},
					"required": []string{"conversation_id", "archived"},
				},
			},
		},
		{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        "delete_conversation",
				Description: "Permanently delete a conversation, its main session, and any sub-agent sessions. If it was current, current is cleared.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"conversation_id": map[string]interface{}{"type": "string"},
					},
					"required": []string{"conversation_id"},
				},
			},
		},
	}
}

func (ch *CoreHandler) requireConversationEngine() (*engine.Engine, error) {
	if ch.conversationEngine == nil {
		return nil, fmt.Errorf("conversation engine is not configured")
	}
	return ch.conversationEngine, nil
}

func (ch *CoreHandler) listConversationsTool(userID string) (string, error) {
	eng, err := ch.requireConversationEngine()
	if err != nil {
		return "", err
	}
	list, err := eng.ListConversations(userID)
	if err != nil {
		return "", err
	}
	activeID := ch.getActiveConversationID(userID)
	var sb strings.Builder
	sb.WriteString("## Conversations (last used first)\n\n")
	sb.WriteString("Each conversation is a user agent with its own model, session, and memory. ")
	sb.WriteString("Your Core session is separate and stays yours.\n\n")
	if activeID != "" {
		fmt.Fprintf(&sb, "Current: `%s`\n\n", activeID)
	} else {
		sb.WriteString("Current: none\n\n")
	}
	if len(list) == 0 {
		sb.WriteString("No conversations.\n")
		return sb.String(), nil
	}
	for i, c := range list {
		ch.writeConversationListEntry(&sb, eng, i+1, c, activeID)
	}
	return sb.String(), nil
}

func (ch *CoreHandler) writeConversationListEntry(sb *strings.Builder, eng *engine.Engine, index int, c *model.Conversation, activeID string) {
	title := conversationTitle(c)
	marker := ""
	if c.ConversationID == activeID {
		marker = " [CURRENT]"
	}
	archived := ""
	if c.Archived {
		archived = " archived"
	}
	fmt.Fprintf(sb, "%d. `%s`%s \"%s\" model=%s session=%s%s\n",
		index, c.ConversationID, marker, title, conversationModel(c), c.SessionID, archived)
	session := conversationSession(eng, c)
	if session == nil {
		return
	}
	if len(session.Summary) > 0 {
		fmt.Fprintf(sb, "   Summary: %s\n", truncateRunes(session.Summary.Text(), conversationSummaryPreviewLimit))
	}
	if len(session.Tags) > 0 {
		fmt.Fprintf(sb, "   Tags: %s\n", strings.Join(session.Tags, ", "))
	}
	fmt.Fprintf(sb, "   Messages: %d active, %d archived · Last: %s\n",
		len(session.Msgs), len(session.ArchivedMsgs), formatConversationTimeAgo(session.UpdatedAt))
}

func (ch *CoreHandler) getConversationTool(_ context.Context, userID string, args map[string]interface{}) (string, error) {
	eng, err := ch.requireConversationEngine()
	if err != nil {
		return "", err
	}
	conversationID, err := requireStringArg(args, "conversation_id")
	if err != nil {
		return "", err
	}
	conv, err := eng.GetConversation(userID, conversationID)
	if err != nil {
		return "", fmt.Errorf("conversation not found: %s", conversationID)
	}
	return ch.formatConversationDetail(eng, userID, conv), nil
}

func (ch *CoreHandler) formatConversationDetail(eng *engine.Engine, userID string, conv *model.Conversation) string {
	var sb strings.Builder
	current := conv.ConversationID == ch.getActiveConversationID(userID)
	fmt.Fprintf(&sb, "## Conversation `%s`\n\n", conv.ConversationID)
	fmt.Fprintf(&sb, "- Title: %s\n", conversationTitle(conv))
	fmt.Fprintf(&sb, "- Model: %s\n", conversationModel(conv))
	fmt.Fprintf(&sb, "- Session: %s\n", conv.SessionID)
	fmt.Fprintf(&sb, "- Current: %t\n", current)
	fmt.Fprintf(&sb, "- Archived: %t\n", conv.Archived)
	session := conversationSession(eng, conv)
	if session == nil {
		sb.WriteString("- Session details: unavailable\n")
		return sb.String()
	}
	if len(session.Summary) > 0 {
		fmt.Fprintf(&sb, "- Summary: %s\n", session.Summary.Text())
	}
	if len(session.Tags) > 0 {
		fmt.Fprintf(&sb, "- Tags: %s\n", strings.Join(session.Tags, ", "))
	}
	fmt.Fprintf(&sb, "- Messages: %d active, %d archived\n", len(session.Msgs), len(session.ArchivedMsgs))
	fmt.Fprintf(&sb, "- Last activity: %s\n", formatConversationTimeAgo(session.UpdatedAt))
	if preview := conversationRecentMessages(session, conversationRecentMessageLimit); preview != "" {
		sb.WriteString("\n### Recent messages\n")
		sb.WriteString(preview)
	}
	return sb.String()
}

func (ch *CoreHandler) createConversationTool(_ context.Context, userID string, args map[string]interface{}) (string, error) {
	eng, err := ch.requireConversationEngine()
	if err != nil {
		return "", err
	}
	title, _ := args["title"].(string)
	modelName, _ := args["model"].(string)
	conv, err := eng.CreateConversation(engine.CreateConversationInput{
		UserID: userID,
		Title:  title,
		Model:  modelName,
	})
	if err != nil {
		return "", err
	}
	if err := ch.setActiveConversationID(userID, conv.ConversationID); err != nil {
		log.Log.Warnf("[CoreHandler] ⚠️  Failed to set active conversation | Error: %v", err)
	}
	ch.invalidateSystemPrompt(userID)
	return fmt.Sprintf("Created conversation %s and set it current. model=%s session=%s",
		conv.ConversationID, conversationModel(conv), conv.SessionID), nil
}

func (ch *CoreHandler) selectConversationTool(_ context.Context, userID string, args map[string]interface{}) (string, error) {
	conversationID, err := requireStringArg(args, "conversation_id")
	if err != nil {
		return "", err
	}
	conv, err := ch.activateConversation(userID, conversationID)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("Current conversation is now %s (%s). model=%s session=%s",
		conversationTitle(conv), conv.ConversationID, conversationModel(conv), conv.SessionID), nil
}

func (ch *CoreHandler) sendConversationTool(ctx context.Context, userID string, args map[string]interface{}) (string, error) {
	eng, err := ch.requireConversationEngine()
	if err != nil {
		return "", err
	}
	message, err := requireStringArg(args, "message")
	if err != nil {
		return "", err
	}
	conversationID, _ := args["conversation_id"].(string)
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		conversationID = ch.getActiveConversationID(userID)
	}
	if conversationID == "" {
		return "", fmt.Errorf("no current conversation; pass conversation_id or call select_conversation / create_conversation")
	}
	conv, err := ch.activateConversation(userID, conversationID)
	if err != nil {
		return "", err
	}
	reply, _, err := eng.ProcessConversation(ctx, userID, conv.ConversationID, message)
	if err != nil {
		return "", err
	}
	return reply, nil
}

func (ch *CoreHandler) renameConversationTool(_ context.Context, userID string, args map[string]interface{}) (string, error) {
	eng, err := ch.requireConversationEngine()
	if err != nil {
		return "", err
	}
	conversationID, err := requireStringArg(args, "conversation_id")
	if err != nil {
		return "", err
	}
	title, err := requireStringArg(args, "title")
	if err != nil {
		return "", err
	}
	if err := eng.RenameConversation(userID, conversationID, title); err != nil {
		return "", err
	}
	ch.invalidateSystemPrompt(userID)
	return fmt.Sprintf("Renamed conversation %s", conversationID), nil
}

func (ch *CoreHandler) setConversationModelTool(_ context.Context, userID string, args map[string]interface{}) (string, error) {
	eng, err := ch.requireConversationEngine()
	if err != nil {
		return "", err
	}
	conversationID, err := requireStringArg(args, "conversation_id")
	if err != nil {
		return "", err
	}
	modelName, err := requireStringArg(args, "model")
	if err != nil {
		return "", err
	}
	if err := eng.SetConversationModel(userID, conversationID, modelName); err != nil {
		return "", err
	}
	ch.invalidateSystemPrompt(userID)
	return fmt.Sprintf("Set conversation %s model to %s", conversationID, modelName), nil
}

func (ch *CoreHandler) archiveConversationTool(_ context.Context, userID string, args map[string]interface{}) (string, error) {
	eng, err := ch.requireConversationEngine()
	if err != nil {
		return "", err
	}
	conversationID, err := requireStringArg(args, "conversation_id")
	if err != nil {
		return "", err
	}
	archived, err := requireBoolArg(args, "archived")
	if err != nil {
		return "", err
	}
	if _, err := eng.GetConversation(userID, conversationID); err != nil {
		return "", fmt.Errorf("conversation not found: %s", conversationID)
	}
	if err := eng.SetConversationArchived(userID, conversationID, archived); err != nil {
		return "", err
	}
	ch.invalidateSystemPrompt(userID)
	if archived {
		return fmt.Sprintf("Archived conversation %s", conversationID), nil
	}
	return fmt.Sprintf("Unarchived conversation %s", conversationID), nil
}

func (ch *CoreHandler) deleteConversationTool(_ context.Context, userID string, args map[string]interface{}) (string, error) {
	eng, err := ch.requireConversationEngine()
	if err != nil {
		return "", err
	}
	conversationID, err := requireStringArg(args, "conversation_id")
	if err != nil {
		return "", err
	}
	if _, err := eng.GetConversation(userID, conversationID); err != nil {
		return "", fmt.Errorf("conversation not found: %s", conversationID)
	}
	if err := eng.DeleteConversation(userID, conversationID); err != nil {
		return "", err
	}
	if ch.getActiveConversationID(userID) == conversationID {
		if err := ch.setActiveConversationID(userID, ""); err != nil {
			log.Log.Warnf("[CoreHandler] ⚠️  Failed to clear current conversation | Error: %v", err)
		}
	}
	ch.invalidateSystemPrompt(userID)
	return fmt.Sprintf("Deleted conversation %s", conversationID), nil
}

func (ch *CoreHandler) activateConversation(userID, conversationID string) (*model.Conversation, error) {
	eng, err := ch.requireConversationEngine()
	if err != nil {
		return nil, err
	}
	conv, err := eng.GetConversation(userID, conversationID)
	if err != nil {
		return nil, fmt.Errorf("conversation not found: %s", conversationID)
	}
	if ch.getActiveConversationID(userID) != conv.ConversationID {
		if err := ch.setActiveConversationID(userID, conv.ConversationID); err != nil {
			return nil, err
		}
		ch.invalidateSystemPrompt(userID)
	}
	return conv, nil
}

func (ch *CoreHandler) getActiveConversationID(userID string) string {
	user, err := ch.getOrCreateUser(userID)
	if err != nil || user == nil {
		return ""
	}
	return user.ActiveConversationID
}

func (ch *CoreHandler) setActiveConversationID(userID, conversationID string) error {
	user, err := ch.getOrCreateUser(userID)
	if err != nil {
		return err
	}
	user.ActiveConversationID = conversationID
	return ch.saveUser(user)
}

func (ch *CoreHandler) buildConversationsPrompt(userID string) string {
	if ch.conversationEngine == nil {
		return ""
	}
	text, err := ch.listConversationsTool(userID)
	if err != nil {
		return ""
	}
	return text
}

func conversationSession(eng *engine.Engine, conv *model.Conversation) *model.Session {
	if eng == nil || eng.Sessions == nil || conv == nil || conv.SessionID == "" {
		return nil
	}
	session, err := eng.Sessions.Get(conv.SessionID)
	if err != nil {
		return nil
	}
	return session
}

func conversationTitle(c *model.Conversation) string {
	if c == nil || strings.TrimSpace(c.Title) == "" {
		return "Untitled"
	}
	return c.Title
}

func conversationModel(c *model.Conversation) string {
	if c == nil || strings.TrimSpace(c.Model) == "" {
		return "(engine default)"
	}
	return c.Model
}

func conversationRecentMessages(session *model.Session, limit int) string {
	if session == nil || limit <= 0 {
		return ""
	}
	var lines []string
	for i := len(session.Msgs) - 1; i >= 0 && len(lines) < limit; i-- {
		msg := session.Msgs[i]
		switch msg.Role {
		case openai.ChatMessageRoleUser, openai.ChatMessageRoleAssistant:
			content := strings.TrimSpace(msg.Content)
			if content == "" {
				continue
			}
			lines = append(lines, fmt.Sprintf("- %s: %s", msg.Role, truncateRunes(content, conversationMessagePreviewLimit)))
		}
	}
	if len(lines) == 0 {
		return ""
	}
	for i, j := 0, len(lines)-1; i < j; i, j = i+1, j-1 {
		lines[i], lines[j] = lines[j], lines[i]
	}
	return strings.Join(lines, "\n") + "\n"
}

func truncateRunes(s string, max int) string {
	s = strings.TrimSpace(s)
	if max <= 0 || utf8.RuneCountInString(s) <= max {
		return s
	}
	runes := []rune(s)
	return string(runes[:max]) + "…"
}

func formatConversationTimeAgo(t time.Time) string {
	if t.IsZero() {
		return "unknown"
	}
	duration := time.Since(t)
	switch {
	case duration < time.Minute:
		return "just now"
	case duration < time.Hour:
		mins := int(duration.Minutes())
		if mins <= 1 {
			return "1m ago"
		}
		return fmt.Sprintf("%dm ago", mins)
	case duration < 24*time.Hour:
		hours := int(duration.Hours())
		if hours <= 1 {
			return "1h ago"
		}
		return fmt.Sprintf("%dh ago", hours)
	default:
		days := int(duration.Hours() / 24)
		if days <= 1 {
			return "1d ago"
		}
		return fmt.Sprintf("%dd ago", days)
	}
}

func requireBoolArg(args map[string]interface{}, key string) (bool, error) {
	v, ok := args[key]
	if !ok {
		return false, fmt.Errorf("%s is required and must be a boolean", key)
	}
	b, ok := v.(bool)
	if !ok {
		return false, fmt.Errorf("%s is required and must be a boolean", key)
	}
	return b, nil
}
