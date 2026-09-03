package pages

import (
	"fmt"
	"html/template"
	"net/url"
	"strings"
	"time"

	"github.com/ghiac/agentize/debuger"
	"github.com/ghiac/agentize/debuger/data"
	"github.com/ghiac/agentize/debuger/ui"
	"github.com/ghiac/agentize/debuger/ui/components"
	"github.com/ghiac/agentize/model"
	"github.com/sashabaranov/go-openai"
)

const sessionDetailItemsPerPage = 25

// SessionDetailPages keeps every collection on a large session independently
// pageable. Page 1 means newest/current; higher pages walk backward in history.
type SessionDetailPages struct {
	Prompts       int
	Messages      int
	Archived      int
	Summarization int
	ToolCalls     int
	Files         int
}

func detailPageSlice[T any](items []T, page int) []T {
	start, end, _ := components.GetPaginationInfo(page, len(items), sessionDetailItemsPerPage)
	return items[start:end]
}

func detailPagination(sessionID, pageParam string, current int, total int, pages SessionDetailPages) string {
	params := url.Values{
		"prompts_page": {fmt.Sprint(pages.Prompts)}, "messages_page": {fmt.Sprint(pages.Messages)},
		"archived_page": {fmt.Sprint(pages.Archived)}, "summaries_page": {fmt.Sprint(pages.Summarization)},
		"tools_page": {fmt.Sprint(pages.ToolCalls)}, "files_page": {fmt.Sprint(pages.Files)},
	}
	params.Del(pageParam)
	return components.Pagination(components.PaginationConfig{
		CurrentPage: current, TotalItems: total, ItemsPerPage: sessionDetailItemsPerPage,
		BaseURL: "/agentize/debug/sessions/" + url.PathEscape(sessionID), PageParam: pageParam, QueryParams: params,
	})
}

func collapsibleCardStart(title, icon string, count int, open bool) string {
	openAttr := ""
	if open {
		openAttr = " open"
	}
	return fmt.Sprintf(`<details class="card mb-4 debug-section"%s>
<summary class="card-header d-flex align-items-center justify-content-between" style="cursor:pointer;list-style:none">
<h5 class="mb-0"><i class="bi bi-%s me-2"></i>%s <span class="badge bg-secondary">%d</span></h5>
<span class="text-muted small">Expand / collapse</span></summary><div class="card-body">`,
		openAttr, template.HTMLEscapeString(icon), template.HTMLEscapeString(title), count)
}

func collapsibleCardEnd() string { return `</div></details>` }

// RenderSessions generates the sessions list HTML page
func RenderSessions(handler *debuger.DebugHandler, page int) (string, error) {
	dp := data.NewDataProvider(handler.GetStore())

	allSessions, err := dp.GetAllSessionsFlat()
	if err != nil {
		return "", fmt.Errorf("failed to get sessions: %w", err)
	}

	// Pagination
	totalItems := len(allSessions)
	startIdx, endIdx, _ := components.GetPaginationInfo(page, totalItems, components.DefaultItemsPerPage)
	paginatedSessions := allSessions[startIdx:endIdx]

	content := ui.ContainerStart()
	content += ui.CardStartWithCount("All Sessions", "diagram-3-fill", totalItems)

	if len(allSessions) == 0 {
		content += components.InfoAlert("No sessions found.")
	} else {
		// Configure session row display
		rowConfig := components.DefaultSessionRowConfig()
		rowConfig.ShowUser = true
		rowConfig.GetFilesCount = func(sessionID string) int {
			files, _ := handler.GetStore().GetOpenedFilesBySession(sessionID)
			return len(files)
		}

		columns := components.SessionTableColumns(rowConfig)
		content += components.TableStartWithConfig(columns, components.TableConfig{
			Striped:     false,
			Hover:       true,
			Small:       true,
			Responsive:  true,
			AlignMiddle: true,
		})

		for i, session := range paginatedSessions {
			content += components.SessionTableRow(session, rowConfig, i)
		}

		content += components.TableEnd(true)
		content += components.SessionTableScript()
		content += components.PaginationSimple(page, totalItems, components.DefaultItemsPerPage, "/agentize/debug/sessions")
	}

	content += ui.CardEnd()
	content += ui.ContainerEnd()
	return ui.Header("Agentize Debug - Sessions") + ui.NavbarAndBody("/agentize/debug/sessions", content) + ui.Footer(), nil
}

// convertExMsgToMessage converts an openai.ChatCompletionMessage to model.Message for display
func convertExMsgToMessage(chatMsg openai.ChatCompletionMessage, sessionID, userID string, index int, sessionModel string, agentType model.AgentType, createdAt time.Time) *model.Message {
	return &model.Message{
		MessageID:    fmt.Sprintf("exmsg-%s-%d", sessionID, index),
		SeqID:        index,
		AgentType:    agentType,
		ContentType:  model.ContentTypeText,
		UserID:       userID,
		SessionID:    sessionID,
		Role:         chatMsg.Role,
		Content:      chatMsg.Content,
		Model:        sessionModel,
		RequestModel: sessionModel,
		HasToolCalls: len(chatMsg.ToolCalls) > 0,
		CreatedAt:    createdAt,
	}
}

// RenderSessionDetail generates the session detail HTML page
func RenderSessionDetail(handler *debuger.DebugHandler, sessionID string) (string, error) {
	return RenderSessionDetailPage(handler, sessionID, SessionDetailPages{Prompts: 1, Messages: 1, Archived: 1, Summarization: 1, ToolCalls: 1, Files: 1})
}

// RenderSessionDetailPage renders the independently paginated detail view.
func RenderSessionDetailPage(handler *debuger.DebugHandler, sessionID string, pages SessionDetailPages) (string, error) {
	pageValues := []*int{&pages.Prompts, &pages.Messages, &pages.Archived, &pages.Summarization, &pages.ToolCalls, &pages.Files}
	for _, page := range pageValues {
		if *page < 1 {
			*page = 1
		}
	}
	dp := data.NewDataProvider(handler.GetStore())

	session, err := dp.GetSession(sessionID)
	if err != nil {
		return "", fmt.Errorf("failed to get session: %w", err)
	}
	if session == nil {
		return "", fmt.Errorf("session not found: %s", sessionID)
	}

	// Messages for this session: newest first (created_at DESC) for listing
	allMessages, err := dp.GetMessagesBySessionDesc(sessionID)
	if err != nil {
		return "", fmt.Errorf("failed to get messages: %w", err)
	}

	// Filter to only show active (non-archived) messages
	// Active messages count is based on session.Msgs; allMessages is sorted newest-first (DESC)
	// So active messages are the first activeCount (newest) messages
	activeCount := len(session.Msgs)

	var messages []*model.Message
	if activeCount > 0 && len(allMessages) > 0 {
		if activeCount >= len(allMessages) {
			messages = allMessages
		} else {
			messages = allMessages[:activeCount]
		}
	}

	files, err := dp.GetOpenedFilesBySession(sessionID)
	if err != nil {
		return "", fmt.Errorf("failed to get files: %w", err)
	}
	openNodes := files[:0]
	for _, file := range files {
		if file.IsOpen {
			openNodes = append(openNodes, file)
		}
	}
	files = openNodes

	summarizationLogs, _ := dp.GetSummarizationLogsBySession(sessionID)
	dbToolCalls, _ := dp.GetToolCallsBySession(sessionID)
	toolCalls := data.ConvertToolCallsToInfo(dbToolCalls)

	content := ui.ContainerStart()

	// Breadcrumb
	content += components.Breadcrumb([]components.BreadcrumbItem{
		{Label: "Dashboard", URL: "/agentize/debug"},
		{Label: "Users", URL: "/agentize/debug/users"},
		{Label: session.UserID, URL: "/agentize/debug/users/" + template.URLQueryEscaper(session.UserID)},
		{Label: "Session", Active: true},
	})

	// Session info card
	title := session.Title
	if title == "" {
		title = "Untitled Session"
	}

	agentTypeBadge := components.AgentTypeBadge(string(session.AgentType))

	inProgressBadge := ""
	if session.InProgress {
		inProgressBadge = components.Badge("In Progress", "warning") + " "
	}

	// Calculate message counts from session object
	activeMessagesCount := len(session.Msgs)
	archivedMessagesCount := len(session.ArchivedMsgs)
	// If database messages count is higher, use it (messages from DB are more accurate)
	dbMessagesCount := len(messages)
	sessionTotalCount := activeMessagesCount + archivedMessagesCount
	if dbMessagesCount > sessionTotalCount {
		// DB has more messages than session object, adjust active count
		activeMessagesCount = dbMessagesCount - archivedMessagesCount
		if activeMessagesCount < 0 {
			activeMessagesCount = dbMessagesCount
			archivedMessagesCount = 0
		}
	}

	summaryDisplay := "-"
	if len(session.Summary) > 0 {
		var items strings.Builder
		items.WriteString(`<ol class="mb-0 ps-3 summary-entries">`)
		for _, entry := range session.Summary {
			items.WriteString(`<li class="mb-2">`)
			items.WriteString(template.HTMLEscapeString(entry))
			items.WriteString(`</li>`)
		}
		items.WriteString(`</ol>`)
		summaryDisplay = items.String()
	}

	summarizedAtDisplay := "-"
	if !session.SummarizedAt.IsZero() {
		summarizedAtDisplay = debuger.FormatTime(session.SummarizedAt) + " <small>(" + debuger.FormatDuration(session.SummarizedAt) + ")</small>"
	}

	tagsDisplay := "-"
	if len(session.Tags) > 0 {
		tagsDisplay = components.TagBadges(session.Tags)
	}

	content += fmt.Sprintf(`
<div class="card mb-4">
    <div class="card-header">
        <h4 class="mb-0"><i class="bi bi-diagram-3-fill me-2"></i>Session Information</h4>
    </div>
    <div class="card-body">
        <div class="row g-4">
            <div class="col-md-6">
                <div class="mb-3">
                    <strong class="d-block mb-2">Session ID:</strong>
                    %s
                </div>
                <div class="mb-3">
                    <strong class="d-block mb-2">Title:</strong>
                    <div>%s</div>
                </div>
                <div class="mb-3">
                    <strong class="d-block mb-2">Agent Type:</strong>
                    <div>%s%s</div>
                </div>
                <div class="mb-3">
                    <strong class="d-block mb-2">Model:</strong>
                    <div>%s</div>
                </div>
                <div class="mb-3">
                    <strong class="d-block mb-2">User:</strong>
                    %s
                </div>
                <div class="mb-3">
                    <strong class="d-block mb-2">Summary:</strong>
                    <div class="text-justify">%s</div>
                </div>
                <div class="mb-3">
                    <strong class="d-block mb-2">Tags:</strong>
                    <div>%s</div>
                </div>
            </div>
            <div class="col-md-6">
                <div class="mb-3">
                    <strong class="d-block mb-2">Messages:</strong>
                    <div>%s + %s</div>
                </div>
                <div class="mb-3">
					<strong class="d-block mb-2">Opened Nodes:</strong>
                    <div>%s</div>
                </div>
                <div class="mb-3">
                    <strong class="d-block mb-2">Message Seq:</strong>
                    <div>%s</div>
                </div>
                <div class="mb-3">
                    <strong class="d-block mb-2">Tool Seq:</strong>
                    <div>%s</div>
                </div>
            </div>
        </div>
        <div class="row g-4 pt-2 mt-1 border-top">
            <div class="col-md-4">
                <strong class="d-block mb-1">Created At:</strong>
                <div class="text-muted">%s <small>(%s)</small></div>
            </div>
            <div class="col-md-4">
                <strong class="d-block mb-1">Updated At:</strong>
                <div class="text-muted">%s <small>(%s)</small></div>
            </div>
            <div class="col-md-4">
                <strong class="d-block mb-1">Summarized At:</strong>
                <div class="text-muted">%s</div>
            </div>
        </div>
    </div>
</div>`,
		components.EntityIDCodeBlock(session.SessionID),
		template.HTMLEscapeString(title),
		inProgressBadge,
		agentTypeBadge,
		components.InlineCode(debuger.GetModelDisplay(session.Model)),
		components.Link(session.UserID, "/agentize/debug/users/"+template.URLQueryEscaper(session.UserID)),
		summaryDisplay,
		tagsDisplay,
		components.Badge(fmt.Sprintf("%d active", activeMessagesCount), "primary"),
		components.Badge(fmt.Sprintf("%d archived", archivedMessagesCount), "secondary"),
		components.CountBadge(len(files), "info"),
		components.CountBadge(session.MessageSeq, "info"),
		components.CountBadge(session.ToolSeq, "info"),
		debuger.FormatTime(session.CreatedAt),
		debuger.FormatDuration(session.CreatedAt),
		debuger.FormatTime(session.UpdatedAt),
		debuger.FormatDuration(session.UpdatedAt),
		summarizedAtDisplay,
	)

	// Prefer the typed last-assembled prompt array. Legacy rows may still contain
	// system messages in Msgs; use those only as an explicitly-labelled fallback.
	systemPrompts := append([]model.SystemPromptEntry(nil), session.SystemPrompts...)
	legacyPromptFallback := false
	if len(systemPrompts) == 0 {
		for _, msg := range session.Msgs {
			if msg.Role == openai.ChatMessageRoleSystem && msg.Content != "" {
				legacyPromptFallback = true
				systemPrompts = append(systemPrompts, model.SystemPromptEntry{Key: "legacy", Title: "Legacy System Message", Content: msg.Content, Source: "session.Msgs"})
			}
		}
	}
	archivedSystemPromptCount := 0
	for _, msg := range session.ArchivedMsgs {
		if msg.Role == openai.ChatMessageRoleSystem {
			archivedSystemPromptCount++
		}
	}

	currentPromptCount := 0
	for _, prompt := range systemPrompts {
		if excluded, _ := components.ToolRetrievablePrompt(prompt.Key, prompt.Title); !excluded {
			currentPromptCount++
		}
	}
	content += collapsibleCardStart("Current System Prompts", "gear-fill", currentPromptCount, true)
	content += `<p class="text-muted mb-3">Ordered prompt array actually sent to the model. Each row is a separate document you can open on its own. Currently open knowledge nodes appear here with usage instructions. Unopened knowledge, web results, user files, and full position lists stay behind tools.</p>`
	if legacyPromptFallback {
		content += components.WarningAlert("Legacy fallback — this session has no typed prompt snapshot yet; these entries came from transcript system messages.")
	} else if !session.SystemPromptsUpdatedAt.IsZero() {
		content += components.NoteAlert("Last assembled", debuger.FormatTime(session.SystemPromptsUpdatedAt)+" ("+debuger.FormatDuration(session.SystemPromptsUpdatedAt)+")")
	}
	if archivedSystemPromptCount > 0 {
		content += components.NoteAlert("Historical snapshots hidden", fmt.Sprintf("%d stale system-prompt snapshots were archived by an older summarization flow. They are not current prompts and will be purged on the next summary cycle.", archivedSystemPromptCount))
	}
	content += components.RenderPromptArray(components.PromptEntriesFromSnapshot(systemPrompts))
	content += collapsibleCardEnd()

	user, _ := dp.GetUser(session.UserID)
	content += renderUserContextCard(user)
	content += renderSessionContextCard(session, nil)

	// Messages card
	content += collapsibleCardStart("Active Messages", "chat-dots-fill", len(messages), true)

	if len(messages) == 0 {
		content += components.InfoAlert("No messages found for this session.")
	} else {
		rowConfig := components.DefaultMessageRowConfig()
		rowConfig.ShowUser = false    // Already on session page, user is known
		rowConfig.ShowSession = false // Already on session page

		columns := components.MessageTableColumns(rowConfig)
		content += components.TableStartWithConfig(columns, components.TableConfig{
			Striped:     false,
			Hover:       true,
			Small:       true,
			Responsive:  true,
			AlignMiddle: true,
		})

		messagePage := detailPageSlice(messages, pages.Messages)
		for i, msg := range messagePage {
			content += components.MessageTableRow(msg, rowConfig, i)
		}

		content += components.TableEnd(true)
		content += components.MessageTableScript()
		content += detailPagination(sessionID, "messages_page", pages.Messages, len(messages), pages)
	}

	content += collapsibleCardEnd()

	// ArchivedMsgs card (previously ExMsgs)
	archivedMessages := make([]openai.ChatCompletionMessage, 0, len(session.ArchivedMsgs))
	for _, archived := range session.ArchivedMsgs {
		if archived.Role != openai.ChatMessageRoleSystem {
			archivedMessages = append(archivedMessages, archived)
		}
	}
	archivedCount := len(archivedMessages)
	content += collapsibleCardStart("Archived Messages (Debug Only)", "archive-fill", archivedCount, pages.Archived > 1)

	if archivedCount == 0 {
		content += components.InfoAlert("No archived messages found for this session.")
	} else {
		content += components.NoteAlert("Note", "ArchivedMsgs are messages moved from Msgs after summarization. They are only displayed here for debugging purposes and are not used in normal operations.")

		// Page 1 is newest; higher pages walk backward through archived history.
		rowConfig := components.DefaultMessageRowConfig()
		rowConfig.ShowUser = false    // Already on session page, user is known
		rowConfig.ShowSession = false // Already on session page
		rowConfig.BaseURL = "/agentize/debug"

		columns := components.MessageTableColumns(rowConfig)
		content += components.TableStartWithConfig(columns, components.TableConfig{
			Striped:     false,
			Hover:       true,
			Small:       true,
			Responsive:  true,
			AlignMiddle: true,
		})

		archivedPage := detailPageSlice(archivedMessages, pages.Archived)
		for i, chatMsg := range archivedPage {
			originalIndex := len(session.ArchivedMsgs) - ((pages.Archived-1)*sessionDetailItemsPerPage + i) - 1
			msg := convertExMsgToMessage(
				chatMsg,
				sessionID,
				session.UserID,
				originalIndex,
				session.Model,
				session.AgentType,
				session.CreatedAt,
			)
			content += components.MessageTableRow(msg, rowConfig, i)
		}

		content += components.TableEnd(true)
		content += components.MessageTableScript()
		content += detailPagination(sessionID, "archived_page", pages.Archived, archivedCount, pages)
	}

	content += collapsibleCardEnd()

	// Summarization Logs card
	content += collapsibleCardStart("Summarization Cycles", "file-text-fill", len(summarizationLogs), pages.Summarization > 1)

	if len(summarizationLogs) == 0 {
		content += components.InfoAlert("No summarization logs found for this session.")
	} else {
		content += components.ListGroupStart()
		for _, log := range detailPageSlice(summarizationLogs, pages.Summarization) {
			statusBadge := components.StatusBadge(log.Status)

			promptDisplay := components.ExpandableWithPreview(log.PromptSent, 500)
			responseDisplay := components.ExpandableWithPreview(log.ResponseReceived, 500)

			content += fmt.Sprintf(`
<div class="list-group-item">
    <div class="d-flex w-100 justify-content-between align-items-start mb-2">
        <div>
            %s
            %s
            %s
        </div>
        <small class="text-muted">%s</small>
    </div>
    <div class="mb-2">
        <strong>Prompt Sent:</strong>
        <div class="p-2 bg-light rounded mt-1" style="white-space: pre-wrap; word-wrap: break-word; font-size: 0.9em;">%s</div>
    </div>`,
				statusBadge,
				components.Badge("Model: "+log.ModelUsed, "info"),
				components.TokenBadge(log.TotalTokens, log.PromptTokens, log.CompletionTokens),
				debuger.FormatTime(log.CreatedAt),
				promptDisplay,
			)

			if log.Status == "success" && log.ResponseReceived != "" {
				content += fmt.Sprintf(`
    <div class="mb-2">
        <strong>Response Received:</strong>
        <div class="p-2 bg-success bg-opacity-10 rounded mt-1" style="white-space: pre-wrap; word-wrap: break-word; font-size: 0.9em;">%s</div>
    </div>`,
					responseDisplay,
				)
			}

			if log.Status == "failed" && log.ErrorMessage != "" {
				content += fmt.Sprintf(`
    <div class="mb-2">
        <strong>Error:</strong>
        %s
    </div>`,
					components.ExpandablePre(log.ErrorMessage, 200),
				)
			}

			content += fmt.Sprintf(`
    <small class="text-muted">Log ID: %s</small>
</div>`,
				components.EntityID(log.LogID),
			)
		}
		content += components.ListGroupEnd()
		content += detailPagination(sessionID, "summaries_page", pages.Summarization, len(summarizationLogs), pages)
	}

	content += collapsibleCardEnd()

	// Tool Calls card
	content += collapsibleCardStart("Tool Calls", "tools", len(toolCalls), pages.ToolCalls > 1)
	content += fmt.Sprintf(`<div class="d-flex justify-content-end mb-3"><a class="btn btn-sm btn-outline-secondary" href="/agentize/debug/tool-calls?session=%s">Dedicated view</a></div>`, template.URLQueryEscaper(sessionID))

	if len(toolCalls) == 0 {
		content += components.InfoAlert("No tool calls found for this session.")
	} else {
		columns := []components.ColumnConfig{
			{Header: "ID", Center: true, NoWrap: true},
			{Header: "Agent", Center: true, NoWrap: true},
			{Header: "Function", NoWrap: true},
			{Header: "Arguments"},
			{Header: "Result"},
			{Header: "Time", NoWrap: true},
			{Header: "", Center: true, NoWrap: true},
		}
		content += components.TableStartWithConfig(columns, components.TableConfig{
			Striped:     false,
			Hover:       true,
			Small:       true,
			Responsive:  true,
			AlignMiddle: true,
		})

		for _, tc := range detailPageSlice(toolCalls, pages.ToolCalls) {
			argsDisplay := components.ExpandableWithPreview(tc.Arguments, 150)
			resultDisplay := components.ExpandableWithPreview(tc.Result, 150)
			agentBadge := components.AgentTypeBadgeFromString(tc.AgentType)

			content += fmt.Sprintf(`<tr>
                <td class="text-center">%s</td>
                <td class="text-center">%s</td>
                <td class="text-nowrap">%s</td>
                <td><div class="mb-0" style="max-width: 300px; white-space: pre-wrap; word-wrap: break-word;">%s</div></td>
                <td><div class="mb-0" style="max-width: 300px; white-space: pre-wrap; word-wrap: break-word;">%s</div></td>
                <td class="text-nowrap">%s</td>
                <td class="text-center">%s</td>
            </tr>`,
				components.EntityID(tc.ToolID),
				agentBadge,
				components.InlineCode(tc.FunctionName),
				argsDisplay,
				resultDisplay,
				debuger.FormatTime(tc.CreatedAt),
				components.OpenButton("/agentize/debug/tool-calls/"+template.URLQueryEscaper(tc.ToolID)),
			)
		}

		content += components.TableEnd(true)
		content += detailPagination(sessionID, "tools_page", pages.ToolCalls, len(toolCalls), pages)
	}

	content += collapsibleCardEnd()

	// Files card
	content += collapsibleCardStart("Opened Nodes", "folder-fill", len(files), pages.Files > 1)

	if len(files) == 0 {
		content += components.InfoAlert("No opened nodes found for this session.")
	} else {
		columns := []components.ColumnConfig{
			{Header: "Node Path"},
			{Header: "Node Title"},
			{Header: "Status", Center: true, NoWrap: true},
			{Header: "Opened At", NoWrap: true},
			{Header: "Closed At", NoWrap: true},
		}
		content += components.TableStartWithConfig(columns, components.TableConfig{
			Striped:     false,
			Hover:       true,
			Small:       true,
			Responsive:  true,
			AlignMiddle: true,
		})

		for _, f := range detailPageSlice(files, pages.Files) {
			status := components.BadgeWithIcon("Open", "✅", "success")
			if !f.IsOpen {
				status = components.BadgeWithIcon("Closed", "❌", "secondary")
			}
			closedAt := "N/A"
			if !f.ClosedAt.IsZero() {
				closedAt = debuger.FormatTime(f.ClosedAt)
			}

			content += fmt.Sprintf(`<tr>
                <td>%s</td>
                <td>%s</td>
                <td class="text-center">%s</td>
                <td class="text-nowrap">%s</td>
                <td class="text-nowrap">%s</td>
            </tr>`,
				components.InlineCode(template.HTMLEscapeString(f.FilePath)),
				template.HTMLEscapeString(f.FileName),
				status,
				debuger.FormatTime(f.OpenedAt),
				closedAt,
			)
		}

		content += components.TableEnd(true)
		content += detailPagination(sessionID, "files_page", pages.Files, len(files), pages)
	}

	content += collapsibleCardEnd()
	content += renderOpenedToolsCard(handler, session, files)
	content += ui.ContainerEnd()
	return ui.Header("Agentize Debug - Session: "+template.HTMLEscapeString(model.DisplayID(sessionID))) + ui.NavbarAndBody("/agentize/debug", content) + ui.Footer(), nil
}
