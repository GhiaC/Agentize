package pages

import (
	"fmt"
	"html/template"
	"net/url"
	"sort"
	"time"

	"github.com/ghiac/agentize/debuger"
	"github.com/ghiac/agentize/debuger/data"
	"github.com/ghiac/agentize/debuger/ui"
	"github.com/ghiac/agentize/debuger/ui/components"
	"github.com/ghiac/agentize/model"
)

// userLastActivity returns the most recent activity time for a user: the newest
// of the user's own UpdatedAt and their latest session's activity, falling back
// to CreatedAt. sessions is expected sorted newest-first (GetAllSessionsSorted).
func userLastActivity(user *model.User, sessions []*model.Session) time.Time {
	last := user.UpdatedAt
	if len(sessions) > 0 {
		st := sessions[0].UpdatedAt
		if st.IsZero() {
			st = sessions[0].CreatedAt
		}
		if st.After(last) {
			last = st
		}
	}
	if last.IsZero() {
		last = user.CreatedAt
	}
	return last
}

// relativeTimeCell renders a time as a relative "X ago" label with the absolute
// timestamp as a hover tooltip. muted dims the text (used for secondary columns).
func relativeTimeCell(t time.Time, muted bool) string {
	if t.IsZero() {
		return `<span class="text-muted">Never</span>`
	}
	cls := ""
	if muted {
		cls = ` class="text-muted"`
	}
	return fmt.Sprintf(`<span%s title="%s">%s</span>`, cls, debuger.FormatTime(t), debuger.FormatTimeAgo(t))
}

// RenderUsers generates the users list HTML page
func RenderUsers(handler *debuger.DebugHandler, page int) (string, error) {
	dp := data.NewDataProvider(handler.GetStore())

	users, err := dp.GetAllUsers()
	if err != nil {
		return "", fmt.Errorf("failed to get users: %w", err)
	}

	sessionsByUser, err := dp.GetAllSessionsSorted()
	if err != nil {
		return "", fmt.Errorf("failed to get sessions: %w", err)
	}

	// Sort users by Last Activity (most recently active first) so the list
	// surfaces the users worth looking at; ties fall back to UserID for stability.
	sort.SliceStable(users, func(i, j int) bool {
		ai := userLastActivity(users[i], sessionsByUser[users[i].UserID])
		aj := userLastActivity(users[j], sessionsByUser[users[j].UserID])
		if ai.Equal(aj) {
			return users[i].UserID < users[j].UserID
		}
		return ai.After(aj)
	})

	// Pagination
	totalItems := len(users)
	startIdx, endIdx, _ := components.GetPaginationInfo(page, totalItems, components.DefaultItemsPerPage)
	paginatedUsers := users[startIdx:endIdx]

	content := ui.ContainerStart()
	content += ui.CardStartWithCount("All Users", "people-fill", totalItems)

	if len(users) == 0 {
		content += components.InfoAlert("No users found.")
	} else {
		columns := []components.ColumnConfig{
			{Header: "User ID", NoWrap: true},
			{Header: "User", NoWrap: true},
			{Header: "Sessions", Center: true, NoWrap: true},
			{Header: "Last Activity", NoWrap: true},
			{Header: "Created", NoWrap: true},
			{Header: "Status", Center: true, NoWrap: true},
			{Header: "Actions", Center: true, NoWrap: true},
		}
		content += components.TableStartWithConfig(columns, components.DefaultTableConfig())

		for _, user := range paginatedUsers {
			userSessions := sessionsByUser[user.UserID]
			sessionCount := len(userSessions)

			banStatus := components.BadgeWithIcon("Active", "✅", "success")
			if user.IsCurrentlyBanned() {
				banText := "Banned"
				if !user.BanUntil.IsZero() {
					banText += fmt.Sprintf(" (until %s)", debuger.FormatTime(user.BanUntil))
				} else {
					banText += " (permanent)"
				}
				banStatus = components.BadgeWithIcon(banText, "🚫", "danger")
			}

			// Combined identity: name on top, @username muted below. Falls back
			// to whichever is present, or "-" when the user has neither.
			userCell := `<span class="text-muted">-</span>`
			switch {
			case user.Name != "" && user.Username != "":
				userCell = fmt.Sprintf(`%s<br><small class="text-muted">@%s</small>`,
					template.HTMLEscapeString(user.Name), template.HTMLEscapeString(user.Username))
			case user.Name != "":
				userCell = template.HTMLEscapeString(user.Name)
			case user.Username != "":
				userCell = fmt.Sprintf(`<span class="text-muted">@%s</span>`, template.HTMLEscapeString(user.Username))
			}

			// Session count is a compact operational indicator.
			sessionsCell := `<span class="text-muted">0</span>`
			if sessionCount > 0 {
				sessionsCell = components.CountBadge(sessionCount, "primary")
			}
			content += fmt.Sprintf(`<tr>
                <td>%s</td>
                <td>%s</td>
                <td class="text-center">%s</td>
                <td class="text-nowrap">%s</td>
                <td class="text-nowrap">%s</td>
				<td class="text-center">%s</td>
                <td class="text-center">%s</td>
            </tr>`,
				components.InlineCode(template.HTMLEscapeString(user.UserID)),
				userCell,
				sessionsCell,
				relativeTimeCell(userLastActivity(user, userSessions), false),
				relativeTimeCell(user.CreatedAt, true),
				banStatus,
				components.ViewDetailsButton("/agentize/debug/users/"+template.URLQueryEscaper(user.UserID)),
			)
		}

		content += components.TableEnd(true)
		content += components.PaginationSimple(page, totalItems, components.DefaultItemsPerPage, "/agentize/debug/users")
	}

	content += ui.CardEnd()
	content += ui.ContainerEnd()
	return ui.Header("Agentize Debug - Users") + ui.NavbarAndBody("/agentize/debug/users", content) + ui.Footer(), nil
}

// RenderUserDetail generates the user detail HTML page.
// If showDeletedSuccess is true, shows a success alert for "data deleted".
func RenderUserDetail(handler *debuger.DebugHandler, userID string, showDeletedSuccess ...bool) (string, error) {
	showDeleted := len(showDeletedSuccess) > 0 && showDeletedSuccess[0]
	return RenderUserDetailPage(handler, userID, showDeleted, 1)
}

// RenderUserDetailPage renders the user view with independently pageable conversations.
func RenderUserDetailPage(handler *debuger.DebugHandler, userID string, showDeleted bool, conversationsPage int) (string, error) {
	dp := data.NewDataProvider(handler.GetStore())

	user, err := dp.GetUser(userID)
	if err != nil {
		return "", fmt.Errorf("failed to get user: %w", err)
	}
	if user == nil {
		return "", fmt.Errorf("user not found: %s", userID)
	}

	sessionsByUser, err := dp.GetAllSessionsSorted()
	if err != nil {
		return "", fmt.Errorf("failed to get sessions: %w", err)
	}
	userSessions := sessionsByUser[userID]

	messages, err := dp.GetMessagesByUser(userID)
	if err != nil {
		return "", fmt.Errorf("failed to get messages: %w", err)
	}

	userDocs, err := dp.GetUserFilesByUser(userID)
	if err != nil {
		return "", fmt.Errorf("failed to get documents: %w", err)
	}

	content := ui.ContainerStart()

	// Breadcrumb
	content += components.Breadcrumb([]components.BreadcrumbItem{
		{Label: "Dashboard", URL: "/agentize/debug"},
		{Label: "Users", URL: "/agentize/debug/users"},
		{Label: userID, Active: true},
	})

	if showDeleted {
		content += components.SuccessAlert("All Agentize data for this user has been deleted, including sessions, conversations, messages, summarization logs, user context, files, traces, workflows, schedules, reviews, and billing/quota when configured.")
	}

	// User info card
	banStatus := "✅ Active"
	if user.IsCurrentlyBanned() {
		banStatus = "🚫 Banned"
		if !user.BanUntil.IsZero() {
			banStatus += fmt.Sprintf(" (until %s)", debuger.FormatTime(user.BanUntil))
		} else {
			banStatus += " (permanent)"
		}
		if user.BanMessage != "" {
			banStatus += ": " + template.HTMLEscapeString(user.BanMessage)
		}
	}

	nameDisplay := "-"
	if user.Name != "" {
		nameDisplay = template.HTMLEscapeString(user.Name)
	}
	usernameDisplay := "-"
	if user.Username != "" {
		usernameDisplay = template.HTMLEscapeString(user.Username)
	}

	// Calculate total MessageSeq and ToolSeq from all user sessions
	totalMessageSeq := 0
	totalToolSeq := 0
	for _, session := range userSessions {
		totalMessageSeq += session.MessageSeq
		totalToolSeq += session.ToolSeq
	}

	// Build ban details display
	isBannedDisplay := "No"
	if user.IsBanned {
		isBannedDisplay = "Yes"
	}
	banUntilDisplay := "-"
	if !user.BanUntil.IsZero() {
		banUntilDisplay = debuger.FormatTime(user.BanUntil)
	} else if user.IsBanned {
		banUntilDisplay = "Permanent"
	}
	banMessageDisplay := "-"
	if user.BanMessage != "" {
		banMessageDisplay = template.HTMLEscapeString(user.BanMessage)
	}

	// Delete user data button (form with confirmation). The ?confirm=<userID>
	// param satisfies the server-side typed-confirmation guard on the endpoint.
	deleteFormAction := "/agentize/debug/users/" + url.PathEscape(userID) + "/delete-data?confirm=" + url.QueryEscape(userID)
	deleteConfirm := fmt.Sprintf("Delete ALL Agentize data for user %q?\n\nThis cannot be undone. It permanently removes:\n"+
		"- sessions and conversations (including session summaries)\n"+
		"- messages and tool calls\n"+
		"- summarization logs\n"+
		"- user context (cross-conversation summary and tags)\n"+
		"- files, route traces, workflows, schedules, and reviews\n"+
		"- billing, quota, consumption, and invoices (when configured)\n\n"+
		"The user account is kept. Counters and memory are reset.", userID)

	content += fmt.Sprintf(`
<div class="card mb-4">
    <div class="card-header d-flex justify-content-between align-items-center">
        <h4 class="mb-0"><i class="bi bi-person-fill me-2"></i>User Information</h4>
        <form method="POST" action="%s" onsubmit="return confirm('%s');" class="d-inline">
            <button type="submit" class="btn btn-sm btn-outline-danger py-0 px-2" style="font-size:0.75rem" title="Delete all Agentize data for this user"><i class="bi bi-trash me-1"></i>Delete all</button>
        </form>
    </div>
    <div class="card-body p-0">
        <div class="row g-0">
            <div class="col-md-6">
                <table class="table table-sm table-borderless mb-0">
                    <tbody>
                        <tr>
                            <td class="text-end fw-bold" style="width: 140px; padding: 0.5rem 1rem;">User ID:</td>
                            <td style="padding: 0.5rem 1rem;">%s</td>
                        </tr>
                        <tr>
                            <td class="text-end fw-bold" style="padding: 0.5rem 1rem;">Name:</td>
                            <td style="padding: 0.5rem 1rem;">%s</td>
                        </tr>
                        <tr>
                            <td class="text-end fw-bold" style="padding: 0.5rem 1rem;">Username:</td>
                            <td style="padding: 0.5rem 1rem;">%s</td>
                        </tr>
                        <tr>
                            <td class="text-end fw-bold" style="padding: 0.5rem 1rem;">Status:</td>
                            <td style="padding: 0.5rem 1rem;">%s</td>
                        </tr>
                        <tr>
                            <td class="text-end fw-bold" style="padding: 0.5rem 1rem;">Is Banned:</td>
                            <td style="padding: 0.5rem 1rem;">%s</td>
                        </tr>
                        <tr>
                            <td class="text-end fw-bold" style="padding: 0.5rem 1rem;">Ban Until:</td>
                            <td style="padding: 0.5rem 1rem;" class="text-muted">%s</td>
                        </tr>
                        <tr>
                            <td class="text-end fw-bold align-top" style="padding: 0.5rem 1rem;">Ban Message:</td>
                            <td style="padding: 0.5rem 1rem;" class="text-muted">%s</td>
                        </tr>
                    </tbody>
                </table>
            </div>
            <div class="col-md-6">
                <table class="table table-sm table-borderless mb-0">
                    <tbody>
                        <tr>
                            <td class="text-end fw-bold" style="width: 140px; padding: 0.5rem 1rem;">Created At:</td>
                            <td style="padding: 0.5rem 1rem;" class="text-muted">%s</td>
                        </tr>
                        <tr>
                            <td class="text-end fw-bold" style="padding: 0.5rem 1rem;">Updated At:</td>
                            <td style="padding: 0.5rem 1rem;" class="text-muted">%s</td>
                        </tr>
                        <tr>
                            <td class="text-end fw-bold" style="padding: 0.5rem 1rem;">Total Message Seq:</td>
                            <td style="padding: 0.5rem 1rem;">%s</td>
                        </tr>
                        <tr>
                            <td class="text-end fw-bold" style="padding: 0.5rem 1rem;">Total Tool Seq:</td>
                            <td style="padding: 0.5rem 1rem;">%s</td>
                        </tr>
                    </tbody>
                </table>
            </div>
        </div>
    </div>
</div>`,
		deleteFormAction,
		template.JSEscapeString(deleteConfirm),
		components.CodeBlock(template.HTMLEscapeString(user.UserID)),
		nameDisplay,
		usernameDisplay,
		banStatus,
		isBannedDisplay,
		banUntilDisplay,
		banMessageDisplay,
		debuger.FormatTime(user.CreatedAt),
		debuger.FormatTime(user.UpdatedAt),
		components.CountBadge(totalMessageSeq, "info"),
		components.CountBadge(totalToolSeq, "info"),
	)

	// Optional billing/credit summary (when provider is set by the application)
	if billingHTML, err := handler.GetUserBillingHTML(userID); err == nil && billingHTML != "" {
		content += billingHTML
	}

	content += renderUserContextCard(user)
	activeConversation, activeSession := activeConversationContext(handler.GetStore(), userID)
	content += renderSessionContextCard(activeSession, activeConversation)

	conversations, err := handler.GetStore().ListConversations(userID)
	if err != nil {
		return "", fmt.Errorf("failed to get conversations: %w", err)
	}
	if conversationsPage < 1 {
		conversationsPage = 1
	}
	convStart, convEnd, _ := components.GetPaginationInfo(conversationsPage, len(conversations), components.DefaultItemsPerPage)
	content += components.CollapsibleCardStartWithCount("Conversations", "chat-left-text", len(conversations), true)
	if len(conversations) == 0 {
		content += components.InfoAlert("No conversations found for this user.")
	} else {
		columns := []components.ColumnConfig{{Header: "When", NoWrap: true}, {Header: "Conversation", NoWrap: true}, {Header: "Title"}, {Header: "Model", NoWrap: true}, {Header: "Session", NoWrap: true}, {Header: "Archived", NoWrap: true}}
		content += components.TableStartWithConfig(columns, components.TableConfig{Hover: true, Small: true, Responsive: true, AlignMiddle: true})
		for _, conv := range conversations[convStart:convEnd] {
			content += userConversationRow(conv)
		}
		content += components.TableEnd(true)
		content += components.Pagination(components.PaginationConfig{CurrentPage: conversationsPage, TotalItems: len(conversations), ItemsPerPage: components.DefaultItemsPerPage, BaseURL: "/agentize/debug/users/" + url.PathEscape(userID), PageParam: "conversations_page"})
	}
	content += components.CollapsibleCardEnd()

	// Core System Prompt card: the live (or store-preview) array of system
	// messages the Core assembles to route this user. Collapsed by default; each
	// section is its own collapsible box.
	promptSections, promptErr := handler.GetCoreSystemPrompt(userID)
	content += renderCoreSystemPromptCard(promptSections, handler.CoreSystemPromptIsPreview(), promptErr)

	// Messages card
	content += components.CollapsibleCardStartWithCount("Messages", "chat-dots-fill", len(messages), false)

	if len(messages) == 0 {
		content += components.InfoAlert("No messages found for this user.")
	} else {
		content += fmt.Sprintf(`<div class="mb-3 text-end"><a href="%s" class="btn btn-sm btn-light">View All</a></div>`,
			"/agentize/debug/messages?user="+template.URLQueryEscaper(userID))

		rowConfig := components.DefaultMessageRowConfig()
		rowConfig.ShowUser = false // Already on user page
		rowConfig.ShowSession = true

		columns := components.MessageTableColumns(rowConfig)
		content += components.TableStartWithConfig(columns, components.TableConfig{
			Striped:     false,
			Hover:       true,
			Small:       true,
			Responsive:  true,
			AlignMiddle: true,
		})

		// Show first 10 messages (already sorted by CreatedAt DESC - newest first)
		displayCount := debuger.Min(len(messages), 10)
		for i := 0; i < displayCount; i++ {
			content += components.MessageTableRow(messages[i], rowConfig, i)
		}

		content += components.TableEnd(true)
		content += components.MessageTableScript()
	}

	content += components.CollapsibleCardEnd()

	// Documents card (real user files: uploaded or generated)
	content += components.CollapsibleCardStartWithCount("Documents", "file-earmark-text-fill", len(userDocs), false)

	if len(userDocs) == 0 {
		content += components.InfoAlert("No documents found for this user.")
	} else {
		columns := []components.ColumnConfig{
			{Header: "Name"},
			{Header: "Source", Center: true, NoWrap: true},
			{Header: "Type", NoWrap: true},
			{Header: "Size", NoWrap: true},
			{Header: "Created At", NoWrap: true},
			{Header: "Session", NoWrap: true},
		}
		content += components.TableStartWithConfig(columns, components.TableConfig{
			Striped:     false,
			Hover:       true,
			Small:       true,
			Responsive:  true,
			AlignMiddle: true,
		})

		for _, f := range userDocs {
			content += fmt.Sprintf(`<tr>
                <td>%s</td>
                <td class="text-center">%s</td>
                <td class="text-nowrap">%s</td>
                <td class="text-nowrap">%s</td>
                <td class="text-nowrap">%s</td>
                <td class="text-nowrap">%s</td>
            </tr>`,
				template.HTMLEscapeString(f.Name),
				fileSourceBadge(f.Source),
				components.InlineCode(template.HTMLEscapeString(f.MIMEType)),
				formatBytes(f.Size),
				debuger.FormatTime(f.CreatedAt),
				components.EntityIDLink(f.SessionID, debuger.SessionPath(f.UserID, f.SessionID)),
			)
		}

		content += components.TableEnd(true)
	}

	content += components.CollapsibleCardEnd()
	content += ui.ContainerEnd()
	return ui.Header("Agentize Debug - User: "+template.HTMLEscapeString(userID)) + ui.NavbarAndBody("/agentize/debug/users", content) + ui.Footer(), nil
}

func userConversationRow(conv *model.Conversation) string {
	title := conv.Title
	if title == "" {
		title = "Untitled"
	}
	archived := ""
	if conv.Archived {
		archived = components.Badge("yes", "secondary")
	}
	return fmt.Sprintf(`<tr><td class="text-nowrap">%s</td><td>%s</td><td>%s</td><td>%s</td><td><a href="%s">%s</a></td><td>%s</td></tr>`,
		template.HTMLEscapeString(formatTimeAgo(conv.UpdatedAt)), components.EntityID(conv.ConversationID), template.HTMLEscapeString(title), template.HTMLEscapeString(conv.Model), debuger.SessionPath(conv.UserID, conv.SessionID), components.EntityID(conv.SessionID), archived)
}

// renderCoreSystemPromptCard renders the "Core System Prompt" card: the ordered
// array of system messages the Core assembles to route this user, one collapsible
// box per section (all collapsed by default). isPreview marks a store-only
// reconstruction (no live Core wired); err is a build failure, if any.
func renderCoreSystemPromptCard(sections []model.PromptSection, isPreview bool, err error) string {
	totalBytes := 0
	dropped := 0
	for _, s := range sections {
		totalBytes += s.Bytes
		if !s.Included && s.Content != "" {
			dropped++ // present but cut by the size budget
		}
	}

	meta := components.Badge(fmt.Sprintf("%d sections", len(sections)), "secondary") +
		" " + components.Badge(formatBytes(int64(totalBytes)), "info")
	if dropped > 0 {
		meta += " " + components.Badge(fmt.Sprintf("%d dropped", dropped), "warning text-dark")
	}
	if isPreview {
		meta += " " + components.Badge("PREVIEW", "warning text-dark")
	}

	out := components.CollapsibleCardStart("Core System Prompt", "braces-asterisk", meta, true)

	if isPreview {
		out += components.WarningAlert("Store-only preview — no live Core is wired. It uses the same controller, persisted user context, and active conversation context as the live prompt builder.")
	} else {
		out += `<p class="text-muted mb-3">The ordered array of system messages the Core sends for this user. Each prompt is a separate document. Knowledge, web results, files, and full position lists stay behind tools.</p>`
	}

	switch {
	case err != nil:
		out += components.DangerAlert("Failed to build the system prompt: " + err.Error())
	case len(sections) == 0:
		out += components.InfoAlert("No system-prompt sections to show.")
	default:
		out += components.RenderPromptArray(components.PromptEntriesFromSections(sections))
	}

	out += components.CollapsibleCardEnd()
	return out
}
