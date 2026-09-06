package model

import (
	"encoding/json"
	"time"

	"github.com/sashabaranov/go-openai"
)

// ContentType represents the type of content in the message
type ContentType string

const (
	ContentTypeText   ContentType = "text"
	ContentTypeAudio  ContentType = "audio"
	ContentTypeImage  ContentType = "image"
	ContentTypePDF    ContentType = "pdf"
	ContentTypeWidget ContentType = "widget"
)

// Message represents a stored message with LLM usage information
type Message struct {
	// MessageID is a unique identifier for this message
	MessageID string

	// SeqID is the sequential number of this message within the session
	SeqID int

	// AgentType is the durable lane for this message: core, schedule, or alert.
	// Leftover high/low/conv values still load and CanonicalAgentType maps them.
	AgentType AgentType

	// ContentType indicates the type of content (text, audio, image, pdf)
	ContentType ContentType

	// UserID identifies the user who sent/received this message
	UserID string

	// SessionID identifies the session this message belongs to
	SessionID string

	// Role is the message role (user, assistant, system, tool)
	Role string

	// Content is the message content
	Content string

	// Model is the LLM model used for this message
	Model string

	// Token usage information
	PromptTokens     int     // Tokens used in the prompt
	CompletionTokens int     // Tokens used in the completion
	TotalTokens      int     // Total tokens used
	CostCredits      float64 // Billed credit cost for this LLM message
	DurationMs       int64   // Wall time to produce this assistant message

	// Request information
	RequestModel string  // Model requested (may differ from actual model used)
	MaxTokens    int     // Max tokens requested
	Temperature  float64 // Temperature used
	HasToolCalls bool    // Whether this message had tool calls

	// Response information
	FinishReason string // Finish reason from LLM (stop, tool_calls, length, etc.)

	// Metadata is the durable, host-visible payload for compact/widget
	// messages (schedule, alert, chart, position, ...). Content stays a
	// short summary; extra fields grow here without a column per widget.
	Metadata map[string]any `json:"metadata,omitempty"`

	CreatedAt time.Time
}

// NewMessage creates a new message from an OpenAI response
func NewMessage(
	messageID string,
	seqID int,
	userID string,
	sessionID string,
	role string,
	content string,
	agentType AgentType,
	contentType ContentType,
	request openai.ChatCompletionRequest,
	response openai.ChatCompletionResponse,
	choice openai.ChatCompletionChoice,
) *Message {
	now := time.Now()
	var temperature float64
	if request.Temperature > 0 {
		temperature = float64(request.Temperature)
	}

	msg := &Message{
		MessageID:        messageID,
		SeqID:            seqID,
		AgentType:        agentType,
		ContentType:      contentType,
		UserID:           userID,
		SessionID:        sessionID,
		Role:             role,
		Content:          content,
		Model:            response.Model,
		RequestModel:     request.Model,
		PromptTokens:     response.Usage.PromptTokens,
		CompletionTokens: response.Usage.CompletionTokens,
		TotalTokens:      response.Usage.TotalTokens,
		MaxTokens:        request.MaxTokens,
		Temperature:      temperature,
		HasToolCalls:     len(choice.Message.ToolCalls) > 0,
		FinishReason:     string(choice.FinishReason),
		CreatedAt:        now,
	}

	msg.HydrateUsageMeta()
	return msg
}

const (
	messageMetaCostCredits = "cost_credits"
	messageMetaDurationMs  = "duration_ms"
)

// HydrateUsageMeta copies cost/duration into Metadata so they persist without
// a new messages-table column, and reads them back after load.
func (m *Message) HydrateUsageMeta() {
	if m == nil {
		return
	}
	if m.Metadata == nil {
		m.Metadata = map[string]any{}
	}
	if m.CostCredits != 0 {
		m.Metadata[messageMetaCostCredits] = m.CostCredits
	} else if v, ok := metadataFloat(m.Metadata, messageMetaCostCredits); ok {
		m.CostCredits = v
	}
	if m.DurationMs != 0 {
		m.Metadata[messageMetaDurationMs] = m.DurationMs
	} else if v, ok := metadataFloat(m.Metadata, messageMetaDurationMs); ok {
		m.DurationMs = int64(v)
	}
	if len(m.Metadata) == 0 {
		m.Metadata = nil
	}
}

func metadataFloat(meta map[string]any, key string) (float64, bool) {
	if meta == nil {
		return 0, false
	}
	switch v := meta[key].(type) {
	case float64:
		return v, true
	case float32:
		return float64(v), true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case json.Number:
		n, err := v.Float64()
		return n, err == nil
	default:
		return 0, false
	}
}

// NewUserMessage creates a message for a user input (no LLM response)
func NewUserMessage(messageID string, seqID int, userID string, sessionID string, content string, contentType ContentType) *Message {
	now := time.Now()
	return &Message{
		MessageID:   messageID,
		SeqID:       seqID,
		AgentType:   AgentTypeUser,
		ContentType: contentType,
		UserID:      userID,
		SessionID:   sessionID,
		Role:        openai.ChatMessageRoleUser,
		Content:     content,
		CreatedAt:   now,
	}
}
