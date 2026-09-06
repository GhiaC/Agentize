package pages

import (
	"fmt"
	"html"
	"time"

	"github.com/ghiac/agentize/debuger"
	"github.com/ghiac/agentize/debuger/ui"
	"github.com/ghiac/agentize/debuger/ui/components"
	"github.com/ghiac/agentize/model"
)

// RenderConversations lists user-facing Conversation rows (not raw sessions).
func RenderConversations(handler *debuger.DebugHandler, page int) (string, error) {
	conversations, err := handler.GetStore().ListAllConversations()
	if err != nil {
		return "", fmt.Errorf("failed to get conversations: %w", err)
	}

	totalItems := len(conversations)
	startIdx, endIdx, _ := components.GetPaginationInfo(page, totalItems, components.DefaultItemsPerPage)
	pageItems := conversations[startIdx:endIdx]

	content := ui.ContainerStart()
	content += ui.CardStartWithCount("Conversations", "chat-left-text", totalItems)
	content += `<p class="text-muted small mb-3">Top-level chats. Each row points at a main session; sub-agents belong to that session and are not listed here.</p>`

	if totalItems == 0 {
		content += components.InfoAlert("No conversations found.")
	} else {
		columns := []components.ColumnConfig{
			{Header: "When", NoWrap: true},
			{Header: "User", NoWrap: true},
			{Header: "Conversation", NoWrap: true},
			{Header: "Title"},
			{Header: "Model", NoWrap: true},
			{Header: "Session", NoWrap: true},
			{Header: "Archived", NoWrap: true},
		}
		content += components.TableStartWithConfig(columns, components.TableConfig{
			Striped: false, Hover: true, Small: true, Responsive: true, AlignMiddle: true,
		})
		users := usersByID(handler)
		for _, conv := range pageItems {
			content += conversationRow(conv, users)
		}
		content += components.TableEnd(true)
		content += components.PaginationSimple(page, totalItems, components.DefaultItemsPerPage, "/agentize/debug/conversations")
	}

	content += ui.CardEnd()
	content += ui.ContainerEnd()
	return ui.Header("Agentize Debug - Conversations") + ui.NavbarAndBody("/agentize/debug/conversations", content) + ui.Footer(), nil
}

func conversationRow(conv *model.Conversation, users map[string]*model.User) string {
	title := conv.Title
	if title == "" {
		title = "Untitled"
	}
	archived := ""
	if conv.Archived {
		archived = components.Badge("yes", "secondary")
	}
	sessionLink := debuger.SessionPath(conv.UserID, conv.SessionID)
	return fmt.Sprintf(
		`<tr>
			<td class="text-nowrap">%s</td>
			<td class="text-nowrap">%s</td>
			<td class="text-nowrap">%s</td>
			<td>%s</td>
			<td class="text-nowrap">%s</td>
			<td class="text-nowrap"><a href="%s">%s</a></td>
			<td>%s</td>
		</tr>`,
		html.EscapeString(formatTimeAgo(conv.UpdatedAt)),
		userLink(users, conv.UserID),
		components.EntityID(conv.ConversationID),
		html.EscapeString(title),
		html.EscapeString(conv.Model),
		sessionLink,
		components.EntityID(conv.SessionID),
		archived,
	)
}

func formatTimeAgo(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	return t.UTC().Format("2006-01-02 15:04")
}
