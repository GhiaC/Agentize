package components

import (
	"fmt"
	"html/template"
	"strings"

	"github.com/ghiac/agentize/debuger"
	"github.com/ghiac/agentize/model"
)

// MessageDisplayConfig holds configuration for message display
type MessageDisplayConfig struct {
	ShowMessageID   bool
	ShowModel       bool
	ShowAgentType   bool
	ShowContentType bool
	ShowToolCalls   bool
	ShowTime        bool
	ContentMaxLen   int
	SessionID       string // Used for tool calls link
}

// DefaultMessageDisplayConfig returns default configuration for message display
func DefaultMessageDisplayConfig() MessageDisplayConfig {
	return MessageDisplayConfig{
		ShowMessageID:   true,
		ShowModel:       true,
		ShowAgentType:   true,
		ShowContentType: true,
		ShowToolCalls:   true,
		ShowTime:        true,
		ContentMaxLen:   200,
	}
}

// MessageCard renders a message as a list-group-item card
func MessageCard(msg *model.Message, config MessageDisplayConfig) string {
	contentDisplay := ExpandableWithPreview(msg.Content, config.ContentMaxLen)

	// Build badges
	var badges string

	// Role badge (always shown)
	badges += RoleBadge(msg.Role)

	// Agent type badge
	if config.ShowAgentType {
		badges += " " + AgentTypeBadgeFromModel(msg.AgentType)
	}

	// Content type badge
	if config.ShowContentType {
		badges += " " + ContentTypeBadgeFromModel(msg.ContentType)
	}

	// Tool calls badge with link
	if config.ShowToolCalls && msg.HasToolCalls {
		if msg.UserID != "" && msg.SessionID != "" {
			badges += fmt.Sprintf(` <a href="%s" class="badge bg-danger text-decoration-none">🔧 Tool Calls</a>`,
				debuger.SessionToolCallsPath(msg.UserID, msg.SessionID))
		} else if config.SessionID != "" {
			badges += fmt.Sprintf(` <a href="/agentize/debug/tool-calls?session=%s" class="badge bg-danger text-decoration-none">🔧 Tool Calls</a>`,
				template.URLQueryEscaper(config.SessionID))
		} else {
			badges += " " + Badge("Has Tool Calls", "danger")
		}
	}

	// Model badge
	modelDisplay := ""
	if config.ShowModel && msg.Model != "" {
		modelDisplay = Badge("Model: "+getModelDisplayShort(msg.Model), "secondary")
	}

	// Time display
	timeDisplay := ""
	if config.ShowTime {
		timeDisplay = formatTimeShort(msg.CreatedAt)
	}

	// Message ID
	messageIDDisplay := ""
	if config.ShowMessageID {
		messageIDDisplay = fmt.Sprintf(`<small class="text-muted">Message ID: %s</small>`, EntityID(msg.MessageID))
	}

	return fmt.Sprintf(`
<div class="list-group-item">
    <div class="d-flex w-100 justify-content-between align-items-start mb-2">
        <div>
            %s
            %s
        </div>
        <small class="text-muted">%s</small>
    </div>
    <p class="mb-2 text-justify">%s</p>
    %s
</div>`,
		badges,
		modelDisplay,
		timeDisplay,
		contentDisplay,
		messageIDDisplay,
	)
}

// MessageListStart starts a message list container
func MessageListStart() string {
	return ListGroupStart()
}

// MessageListEnd ends a message list container
func MessageListEnd() string {
	return ListGroupEnd()
}

// ContentTypeBadgeFromModel returns a badge for content type from model.ContentType
func ContentTypeBadgeFromModel(contentType model.ContentType) string {
	switch contentType {
	case model.ContentTypeText:
		return BadgeWithIcon("Text", "📝", "light text-dark")
	case model.ContentTypeAudio:
		return BadgeWithIcon("Audio", "🎵", "warning text-dark")
	case model.ContentTypeImage:
		return BadgeWithIcon("Image", "🖼️", "info")
	case model.ContentTypePDF:
		return BadgeWithIcon("PDF", "📄", "secondary")
	default:
		if contentType == "" {
			return Badge("-", "secondary")
		}
		return Badge(string(contentType), "secondary")
	}
}

// ContentTypeBadgeFromString returns a badge for content type from string
func ContentTypeBadgeFromString(contentType string) string {
	return ContentTypeBadgeFromModel(model.ContentType(contentType))
}

// getModelDisplayShort returns a shortened model display name
func getModelDisplayShort(model string) string {
	if model == "" {
		return "-"
	}
	// Truncate long model names (rune-safe)
	return debuger.TruncateString(model, 20)
}

// formatTimeShort formats time for message display
func formatTimeShort(t interface{ Format(string) string }) string {
	return t.Format("2006-01-02 15:04:05")
}

// MessageRowConfig holds configuration for message table row display
type MessageRowConfig struct {
	ShowUser         bool              // Show user column with link
	ShowSession      bool              // Show session column with link
	BaseURL          string            // Base URL for links
	RouteByMessageID map[string]string // messageID -> DAG href
	Users            map[string]*model.User
}

// DefaultMessageRowConfig returns default configuration for message row
func DefaultMessageRowConfig() MessageRowConfig {
	return MessageRowConfig{
		ShowUser:    true,
		ShowSession: true,
		BaseURL:     "/agentize/debug",
	}
}

// MessageTableColumns returns the column configuration for message table
func MessageTableColumns(config MessageRowConfig) []ColumnConfig {
	columns := []ColumnConfig{
		{Header: "", Center: true, NoWrap: true}, // Expand button
		{Header: "ID", Center: true, NoWrap: true},
		{Header: "Time", NoWrap: true},
		{Header: "Agent", Center: true, NoWrap: true},
		{Header: "Type", Center: true, NoWrap: true},
		{Header: "Role", Center: true, NoWrap: true},
		{Header: "Content"},
		{Header: "Model", Center: true, NoWrap: true},
	}
	if config.ShowUser {
		columns = append(columns, ColumnConfig{Header: "User", NoWrap: true})
	}
	columns = append(columns, ColumnConfig{Header: "Tools", Center: true, NoWrap: true})
	return columns
}

// MessageTableRow renders a message as an expandable table row
// Returns the collapsed row + expanded row (hidden by default)
func MessageTableRow(msg *model.Message, config MessageRowConfig, rowIndex int) string {
	contentPreview := TruncatedText(msg.Content, 100)
	agentBadge := AgentTypeBadgeFromModel(msg.AgentType)
	contentTypeBadge := ContentTypeBadgeFromModel(msg.ContentType)
	roleBadge := RoleBadge(msg.Role)

	// Model display
	modelDisplay := "-"
	if msg.Model != "" {
		modelDisplay = getModelDisplayShort(msg.Model)
	}

	// Tool calls / DAG badges
	toolCallDisplay := Badge("-", "secondary")
	if msg.HasToolCalls {
		toolCallDisplay = fmt.Sprintf(`<a href="%s" class="btn btn-sm btn-outline-warning">🔧 View</a>`,
			debuger.SessionToolCallsPath(msg.UserID, msg.SessionID))
	}
	if config.RouteByMessageID != nil {
		if u := config.RouteByMessageID[msg.MessageID]; u != "" {
			if toolCallDisplay == Badge("-", "secondary") {
				toolCallDisplay = ""
			}
			toolCallDisplay += fmt.Sprintf(` <a href="%s" class="btn btn-sm btn-outline-info">DAG</a>`, u)
		}
	}

	// Format time as "ago"
	timeAgo := debuger.FormatTimeAgo(msg.CreatedAt)

	// Build the collapsed row
	rowID := fmt.Sprintf("msg-row-%d", rowIndex)
	expandBtnID := fmt.Sprintf("msg-expand-%d", rowIndex)

	html := fmt.Sprintf(`<tr id="%s">
		<td class="text-center">
			<button class="btn btn-sm btn-outline-secondary msg-expand-btn" type="button" onclick="toggleMessageRow('%s', '%s')" id="%s">
				<i class="bi bi-chevron-down"></i>
			</button>
		</td>
		<td class="text-center">%s</td>
		<td class="text-nowrap">%s</td>
		<td class="text-center">%s</td>
		<td class="text-center">%s</td>
		<td class="text-center">%s</td>
		<td class="text-break" style="max-width: 300px;">%s</td>
		<td class="text-center">%s</td>`,
		rowID,
		rowID, expandBtnID, expandBtnID,
		EntityID(msg.MessageID),
		timeAgo,
		agentBadge,
		contentTypeBadge,
		roleBadge,
		contentPreview,
		InlineCode(modelDisplay),
	)

	// Add user column if configured
	if config.ShowUser {
		html += fmt.Sprintf(`<td class="text-nowrap">%s</td>`,
			messageUserLabel(msg.UserID, config))
	}

	html += fmt.Sprintf(`<td class="text-center">%s</td></tr>`, toolCallDisplay)

	colSpan := 9
	if config.ShowUser {
		colSpan++
	}

	userLabel := messageUserLabel(msg.UserID, config)
	dagHref := ""
	if config.RouteByMessageID != nil {
		dagHref = config.RouteByMessageID[msg.MessageID]
	}
	dagCell := `<span class="text-muted">—</span>`
	if dagHref != "" {
		dagCell = fmt.Sprintf(`<a href="%s" class="btn btn-sm btn-outline-info">Load DAG</a>`, dagHref)
	} else if msg.HasToolCalls && msg.UserID != "" && msg.SessionID != "" {
		dagCell = fmt.Sprintf(`<a href="%s" class="btn btn-sm btn-outline-secondary">Session tools</a>`,
			debuger.SessionToolCallsPath(msg.UserID, msg.SessionID))
	}
	costDisplay := sessionCostDisplay(msg.CostCredits)
	durationDisplay := debuger.FormatDurationMs(msg.DurationMs)
	if msg.DurationMs <= 0 {
		durationDisplay = "—"
	}

	html += fmt.Sprintf(`<tr id="%s-details" style="display: none;" class="msg-expand-details">
		<td colspan="%d">
			<div class="p-3">
				<div class="row">
					<div class="col-md-6">
						<table class="table table-sm table-borderless mb-0">
							<tr><th class="text-muted" style="width: 140px;">Message ID</th><td>%s</td></tr>
							<tr><th class="text-muted">Seq ID</th><td>%d</td></tr>
							<tr><th class="text-muted">Session ID</th><td>%s</td></tr>
							<tr><th class="text-muted">User</th><td>%s</td></tr>
							<tr><th class="text-muted">Created At</th><td>%s</td></tr>
							<tr><th class="text-muted">Kind</th><td>%s</td></tr>
							<tr><th class="text-muted">Content Type</th><td>%s</td></tr>
							<tr><th class="text-muted">Role</th><td>%s</td></tr>
						</table>
					</div>
					<div class="col-md-6">
						<table class="table table-sm table-borderless mb-0">
							<tr><th class="text-muted" style="width: 140px;">Model</th><td>%s</td></tr>
							<tr><th class="text-muted">Request Model</th><td>%s</td></tr>
							<tr><th class="text-muted">Prompt Tokens</th><td>%d</td></tr>
							<tr><th class="text-muted">Completion Tokens</th><td>%d</td></tr>
							<tr><th class="text-muted">Total Tokens</th><td>%d</td></tr>
							<tr><th class="text-muted">Cost</th><td>%s</td></tr>
							<tr><th class="text-muted">Reply duration</th><td>%s</td></tr>
							<tr><th class="text-muted">Max Tokens</th><td>%d</td></tr>
							<tr><th class="text-muted">Temperature</th><td>%.2f</td></tr>
							<tr><th class="text-muted">Finish Reason</th><td>%s</td></tr>
							<tr><th class="text-muted">Has Tool Calls</th><td>%s</td></tr>
							<tr><th class="text-muted">Turn DAG</th><td>%s</td></tr>
						</table>
					</div>
				</div>
				<div class="mt-3">
					<strong class="text-muted">Full Content:</strong>
					<pre class="bg-white border rounded p-2 mt-1" style="white-space: pre-wrap; word-wrap: break-word; max-height: 400px; overflow-y: auto;">%s</pre>
				</div>
			</div>
		</td>
	</tr>`,
		rowID, colSpan,
		EntityID(msg.MessageID),
		msg.SeqID,
		EntityIDLink(msg.SessionID, debuger.SessionPath(msg.UserID, msg.SessionID)),
		userLabel,
		msg.CreatedAt.Format("2006-01-02 15:04:05"),
		agentBadge,
		contentTypeBadge,
		roleBadge,
		InlineCode(msg.Model),
		InlineCode(msg.RequestModel),
		msg.PromptTokens,
		msg.CompletionTokens,
		msg.TotalTokens,
		costDisplay,
		template.HTMLEscapeString(durationDisplay),
		msg.MaxTokens,
		msg.Temperature,
		getFinishReasonDisplay(msg.FinishReason),
		getBoolBadge(msg.HasToolCalls),
		dagCell,
		template.HTMLEscapeString(msg.Content),
	)

	return html
}

func messageUserLabel(userID string, config MessageRowConfig) string {
	userID = strings.TrimSpace(userID)
	href := config.BaseURL + "/users/" + template.URLQueryEscaper(userID)
	label := UserDisplayLabel(nil, userID)
	if config.Users != nil {
		label = ListUserLabel(config.Users[userID], userID)
	}
	if label == "" {
		return `<span class="text-muted">—</span>`
	}
	return TruncatedLink(label, href, 28)
}

// UserDisplayLabel prefers name, then @username, then the raw id.
func UserDisplayLabel(user *model.User, userID string) string {
	if user != nil {
		if label := user.DisplayLabel(); label != "" {
			return label
		}
	}
	return strings.TrimSpace(userID)
}

// ListUserLabel is the short identity for tables: @username, then name, then id.
func ListUserLabel(user *model.User, userID string) string {
	if user != nil {
		if username := strings.TrimSpace(user.Username); username != "" {
			return "@" + username
		}
		if name := strings.TrimSpace(user.Name); name != "" {
			return name
		}
		if id := strings.TrimSpace(user.UserID); id != "" {
			return id
		}
	}
	return strings.TrimSpace(userID)
}

// UserDebugLink renders a username (preferred) linking to the user detail page.
func UserDebugLink(user *model.User, userID string) string {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return `<span class="text-muted">-</span>`
	}
	label := ListUserLabel(user, userID)
	if label == "" {
		return `<span class="text-muted">-</span>`
	}
	return TruncatedLink(label, "/agentize/debug/users/"+template.URLQueryEscaper(userID), 28)
}

// MessageTableScript returns the JavaScript needed for expandable rows
func MessageTableScript() string {
	return `
<script>
function toggleMessageRow(rowId, btnId) {
    var detailsRow = document.getElementById(rowId + '-details');
    var btn = document.getElementById(btnId);
    if (detailsRow.style.display === 'none') {
        detailsRow.style.display = 'table-row';
        btn.innerHTML = '<i class="bi bi-chevron-up"></i>';
    } else {
        detailsRow.style.display = 'none';
        btn.innerHTML = '<i class="bi bi-chevron-down"></i>';
    }
}
</script>
`
}

// Helper to format bool as badge
func getBoolBadge(val bool) string {
	if val {
		return BadgeWithIcon("Yes", "✅", "success")
	}
	return Badge("No", "secondary")
}

// Helper to display finish reason
func getFinishReasonDisplay(reason string) string {
	if reason == "" {
		return Badge("-", "secondary")
	}
	switch reason {
	case "stop":
		return Badge("stop", "success")
	case "tool_calls":
		return Badge("tool_calls", "warning text-dark")
	case "length":
		return Badge("length", "danger")
	default:
		return Badge(reason, "secondary")
	}
}
