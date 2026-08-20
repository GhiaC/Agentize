package core

import (
	"context"
	"fmt"
	"strings"

	"github.com/ghiac/agentize/engine"
	"github.com/ghiac/agentize/log"
	"github.com/sashabaranov/go-openai"
)

func conversationToolDefs() []openai.Tool {
	return []openai.Tool{
		{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        "list_conversations",
				Description: "List this user's conversations, most recently used first. Use before select_conversation or send_conversation.",
				Parameters: map[string]interface{}{
					"type":       "object",
					"properties": map[string]interface{}{},
				},
			},
		},
		{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        "create_conversation",
				Description: "Create a new top-level conversation with its own session and optional model. Does not route through low/high agents.",
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
				Description: "Make a conversation the active one for later send_conversation calls.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"conversation_id": map[string]interface{}{"type": "string", "description": "Conversation ID from list_conversations"},
					},
					"required": []string{"conversation_id"},
				},
			},
		},
		{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        "send_conversation",
				Description: "Send a message into a conversation's session. The session reply is returned verbatim.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"conversation_id": map[string]interface{}{"type": "string", "description": "Optional; defaults to the active conversation"},
						"message":         map[string]interface{}{"type": "string", "description": "Message to send into the conversation session"},
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
	var sb strings.Builder
	sb.WriteString("## Conversations (last used first)\n\n")
	if len(list) == 0 {
		sb.WriteString("No conversations.\n")
		return sb.String(), nil
	}
	for i, c := range list {
		title := c.Title
		if title == "" {
			title = "Untitled"
		}
		archived := ""
		if c.Archived {
			archived = " archived"
		}
		fmt.Fprintf(&sb, "%d. `%s` \"%s\" model=%s session=%s%s\n",
			i+1, c.ConversationID, title, c.Model, c.SessionID, archived)
	}
	return sb.String(), nil
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
	return fmt.Sprintf("Created conversation %s and set it active.", conv.ConversationID), nil
}

func (ch *CoreHandler) selectConversationTool(_ context.Context, userID string, args map[string]interface{}) (string, error) {
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
	if err := ch.setActiveConversationID(userID, conv.ConversationID); err != nil {
		return "", err
	}
	ch.invalidateSystemPrompt(userID)
	title := conv.Title
	if title == "" {
		title = "Untitled"
	}
	return fmt.Sprintf("Selected conversation: %s (%s)", title, conv.ConversationID), nil
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
		return "", fmt.Errorf("no active conversation; pass conversation_id or call select_conversation")
	}
	reply, _, err := eng.ProcessConversation(ctx, userID, conversationID, message)
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
