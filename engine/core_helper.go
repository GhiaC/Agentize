package engine

import (
	"context"
	"fmt"
	"strings"

	"github.com/ghiac/agentize/log"
	"github.com/ghiac/agentize/model"
	"github.com/sashabaranov/go-openai"
)

// Default max runes for the initial web search message shown to the user.
const webSearchInitialMaxRunes = 1024

// FormatWebSearchInitialMessage builds a short, user-facing "initial result" message from a full search result.
// If maxRunes <= 0, webSearchInitialMaxRunes is used. Truncation prefers word/sentence boundaries and appends "…" when truncated.
func FormatWebSearchInitialMessage(result string, maxRunes int) string {
	const header = "🔍 نتیجه اولیه جستجو\n\n"
	trimmed := strings.TrimSpace(result)
	if trimmed == "" {
		return header + "—"
	}
	if maxRunes <= 0 {
		maxRunes = webSearchInitialMaxRunes
	}
	runes := []rune(trimmed)
	if len(runes) <= maxRunes {
		return header + trimmed
	}
	// Prefer cut at last space before limit; otherwise hard cut
	cut := maxRunes
	for i := cut - 1; i >= 0 && i < len(runes); i-- {
		if runes[i] == ' ' || runes[i] == '\n' || runes[i] == '.' || runes[i] == '۔' {
			cut = i + 1
			break
		}
	}
	return header + string(runes[:cut]) + "…"
}

// Search model names for web search capability
const (
	DefaultSearchModel            = "openai/gpt-4o-mini-search-preview"
	SearchModelTongyiDeepResearch = "alibaba/tongyi-deepresearch-30b-a3b"
)

// PerformWebSearch performs a web search using the default search-enabled model.
func PerformWebSearch(
	ctx context.Context,
	llmClient *openai.Client,
	llmConfig LLMConfig,
	query string,
	userID string,
) (string, error) {
	return PerformWebSearchWithModel(ctx, llmClient, llmConfig, query, userID, DefaultSearchModel)
}

// PerformWebSearchWithModel performs a web search using the given search-enabled model.
// Models: gpt-4o-search-preview, gpt-4o-mini-search-preview, or alibaba/tongyi-deepresearch-30b-a3b (etc.)
func PerformWebSearchWithModel(
	ctx context.Context,
	llmClient *openai.Client,
	llmConfig LLMConfig,
	query string,
	userID string,
	searchModel string,
) (string, error) {
	if searchModel == "" {
		searchModel = DefaultSearchModel
	}
	// Ensure userID is in context
	if userID != "" {
		ctx = model.WithUserID(ctx, userID)
	}

	log.Log.Infof("[WebSearch] 🔍 Performing web search | UserID: %s | Query: %s | Model: %s", userID, query, searchModel)

	request := openai.ChatCompletionRequest{
		Model: searchModel,
		Messages: []openai.ChatCompletionMessage{
			{
				Role:    openai.ChatMessageRoleUser,
				Content: query,
			},
		},
	}

	resp, err := llmClient.CreateChatCompletion(ctx, request)
	if err != nil {
		log.Log.Errorf("[WebSearch] ❌ Web search failed | UserID: %s | Error: %v", userID, err)
		return "", fmt.Errorf("web search failed: %w", err)
	}

	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("no response from web search")
	}

	result := resp.Choices[0].Message.Content
	log.Log.Infof("[WebSearch] ✅ Web search completed | UserID: %s | Result length: %d chars", userID, len(result))
	return result, nil
}
