package pages

import (
	"fmt"
	"html/template"
	"net/url"
	"strconv"
	"strings"

	"github.com/ghiac/agentize/debuger"
	"github.com/ghiac/agentize/debuger/data"
	"github.com/ghiac/agentize/debuger/ui/components"
	"github.com/ghiac/agentize/model"
)

type memoryEditor struct {
	SummaryDeleteURL func(index int) string
	TagDeleteURL     string
	TagEditURL       string
}

func userMemoryEditor(userID string) memoryEditor {
	base := "/agentize/debug/users/" + url.PathEscape(userID) + "/context"
	confirm := "?confirm=" + url.QueryEscape(userID)
	return memoryEditor{
		SummaryDeleteURL: func(index int) string {
			return fmt.Sprintf("%s/summary/%d/delete%s", base, index, confirm)
		},
		TagDeleteURL: base + "/tag/delete" + confirm,
		TagEditURL:   base + "/tag/edit" + confirm,
	}
}

func sessionMemoryEditor(userID, sessionID string) memoryEditor {
	base := "/agentize/debug/users/" + url.PathEscape(userID) + "/sessions/" + url.PathEscape(sessionID) + "/context"
	confirm := "?confirm=" + url.QueryEscape(sessionID)
	return memoryEditor{
		SummaryDeleteURL: func(index int) string {
			return fmt.Sprintf("%s/summary/%d/delete%s", base, index, confirm)
		},
		TagDeleteURL: base + "/tag/delete" + confirm,
		TagEditURL:   base + "/tag/edit" + confirm,
	}
}

func renderUserContextCard(user *model.User) string {
	count := 0
	if user != nil {
		count = len(user.ContextSummary) + len(user.ContextTags)
	}
	out := components.CollapsibleCardStartWithCount("User Context", "person-lines-fill", count, true)
	out += `<p class="text-muted mb-3">Durable facts that must stay true across conversations. At most 20. Prefer updating an existing line over adding a similar one.</p>`
	if user == nil || (len(user.ContextSummary) == 0 && len(user.ContextTags) == 0) {
		out += components.InfoAlert("No cross-conversation facts yet. Entries appear here after conversations are summarized.")
		out += components.CollapsibleCardEnd()
		return out
	}
	editor := userMemoryEditor(user.UserID)
	if len(user.ContextSummary) > 0 {
		out += `<h6>Facts</h6>` + renderEditableSummary(user.ContextSummary, editor)
	}
	if len(user.ContextTags) > 0 {
		out += `<h6>Tags</h6>` + renderEditableTags(user.ContextTags, editor)
	}
	out += components.CollapsibleCardEnd()
	return out
}

func renderSessionContextCard(session *model.Session, conversation *model.Conversation) string {
	count := 0
	if session != nil {
		if session.Title != "" {
			count++
		}
		count += len(session.Summary) + len(session.Tags)
	}
	out := components.CollapsibleCardStartWithCount("Session Context", "journal-text", count, true)
	out += `<p class="text-muted mb-3">Specific facts for this conversation (max 20). Not a recap — only information that must persist in this session.</p>`
	if conversation != nil {
		out += fmt.Sprintf(`<p class="mb-2"><strong>Conversation:</strong> %s</p>`, components.EntityID(conversation.ConversationID))
	}
	if session == nil || (session.Title == "" && len(session.Summary) == 0 && len(session.Tags) == 0) {
		out += components.InfoAlert("No title, facts, or tags on the active conversation yet.")
		out += components.CollapsibleCardEnd()
		return out
	}
	title := session.Title
	if title == "" {
		title = "Untitled"
	}
	out += fmt.Sprintf(`<p class="mb-2"><strong>Title:</strong> %s</p>`, template.HTMLEscapeString(title))
	editor := sessionMemoryEditor(session.UserID, session.SessionID)
	if len(session.Summary) > 0 {
		out += `<h6>Facts</h6>` + renderEditableSummary(session.Summary, editor)
	}
	if len(session.Tags) > 0 {
		out += `<h6>Tags</h6>` + renderEditableTags(session.Tags, editor)
	}
	out += components.CollapsibleCardEnd()
	return out
}

func renderEditableSummary(entries model.SummaryEntries, editor memoryEditor) string {
	if len(entries) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(`<ol class="mb-3 ps-3">`)
	for i, entry := range entries {
		b.WriteString(`<li class="mb-2 d-flex gap-2 align-items-start">`)
		b.WriteString(`<span class="flex-grow-1">`)
		b.WriteString(template.HTMLEscapeString(entry))
		b.WriteString(`</span>`)
		if editor.SummaryDeleteURL != nil {
			b.WriteString(fmt.Sprintf(
				`<form method="POST" action="%s" class="d-inline" onsubmit="return confirm('Delete this fact?');"><button type="submit" class="btn btn-sm btn-outline-danger py-0 px-1" title="Delete">×</button></form>`,
				template.HTMLEscapeString(editor.SummaryDeleteURL(i)),
			))
		}
		b.WriteString(`</li>`)
	}
	b.WriteString(`</ol>`)
	return b.String()
}

func renderEditableTags(tags []string, editor memoryEditor) string {
	if len(tags) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(`<div class="mb-3"><div class="tag-badges">`)
	for _, tag := range tags {
		esc := template.HTMLEscapeString(tag)
		b.WriteString(`<span class="badge bg-info text-dark me-2 mb-2 d-inline-flex align-items-center gap-1">`)
		if editor.TagEditURL != "" {
			b.WriteString(fmt.Sprintf(
				`<form method="POST" action="%s" class="d-inline-flex align-items-center gap-1 mb-0">`+
					`<input type="hidden" name="old_tag" value="%s">`+
					`<input type="text" name="new_tag" value="%s" class="form-control form-control-sm" style="width:9rem;display:inline-block">`+
					`<button type="submit" class="btn btn-sm btn-light py-0 px-1" title="Save">Save</button></form>`,
				template.HTMLEscapeString(editor.TagEditURL), esc, esc,
			))
		} else {
			b.WriteString(esc)
		}
		if editor.TagDeleteURL != "" {
			b.WriteString(fmt.Sprintf(
				`<form method="POST" action="%s" class="d-inline mb-0" onsubmit="return confirm('Delete tag %s?');">`+
					`<input type="hidden" name="tag" value="%s">`+
					`<button type="submit" class="btn btn-sm btn-outline-light py-0 px-1" title="Delete">×</button></form>`,
				template.HTMLEscapeString(editor.TagDeleteURL), strconv.Quote(tag), esc,
			))
		}
		b.WriteString(`</span>`)
	}
	b.WriteString(`</div></div>`)
	return b.String()
}

func renderSummaryEntries(entries model.SummaryEntries) string {
	return renderEditableSummary(entries, memoryEditor{})
}

func UsersByID(handler *debuger.DebugHandler) map[string]*model.User {
	return usersByID(handler)
}

func usersByID(handler *debuger.DebugHandler) map[string]*model.User {
	out := map[string]*model.User{}
	if handler == nil {
		return out
	}
	users, err := data.NewDataProvider(handler.GetStore()).GetAllUsers()
	if err != nil {
		return out
	}
	for _, user := range users {
		if user != nil && user.UserID != "" {
			out[user.UserID] = user
		}
	}
	return out
}

func userLink(users map[string]*model.User, userID string) string {
	return components.UserDebugLink(users[userID], userID)
}

func renderOpenedToolsCard(handler *debuger.DebugHandler, session *model.Session, files []*model.OpenedFile) string {
	type nodeTools struct {
		Path  string
		Title string
		Tools []string
	}
	seen := map[string]bool{}
	nodes := make([]nodeTools, 0)
	add := func(path, title string) {
		if path == "" || seen[path] {
			return
		}
		seen[path] = true
		if title == "" {
			title = path
		}
		nodes = append(nodes, nodeTools{Path: path, Title: title, Tools: handler.ToolsForNode(path)})
	}
	if session != nil {
		for _, digest := range session.NodeDigests {
			add(digest.Path, digest.Title)
		}
	}
	for _, file := range files {
		if file != nil && file.IsOpen {
			add(file.FilePath, file.FileName)
		}
	}

	toolCount := 0
	for _, node := range nodes {
		toolCount += len(node.Tools)
	}
	out := collapsibleCardStart("Opened Tools", "wrench", toolCount, false)
	out += `<p class="text-muted mb-3">Tools granted by explicitly opened knowledge nodes. Unopened nodes do not contribute tools or prompt text.</p>`
	if len(nodes) == 0 {
		out += components.InfoAlert("No opened nodes, so no node-owned tools are active.")
		out += collapsibleCardEnd()
		return out
	}
	for _, node := range nodes {
		body := ""
		if len(node.Tools) == 0 {
			body = `<p class="text-muted mb-0">This node is open but declares no active tools.</p>`
		} else {
			var badges []string
			for _, name := range node.Tools {
				badges = append(badges, components.Badge(name, "secondary"))
			}
			body = strings.Join(badges, " ")
		}
		meta := components.Badge(fmt.Sprintf("%d tools", len(node.Tools)), "info") + " " + components.Badge(node.Path, "light text-dark")
		out += components.CollapsibleSection(node.Title, meta, body, false)
	}
	out += collapsibleCardEnd()
	return out
}

func activeConversationContext(store debuger.DebugStore, userID string) (*model.Conversation, *model.Session) {
	if store == nil || userID == "" {
		return nil, nil
	}
	user, err := store.GetUser(userID)
	if err != nil || user == nil {
		return nil, nil
	}
	conversations, err := store.ListConversations(userID)
	if err != nil {
		return nil, nil
	}
	pick := func(id string) (*model.Conversation, *model.Session) {
		if id == "" {
			return nil, nil
		}
		for _, conversation := range conversations {
			if conversation == nil || conversation.ConversationID != id || conversation.UserID != userID {
				continue
			}
			session, err := store.GetUserSession(userID, conversation.SessionID)
			if err != nil || session == nil || session.UserID != userID {
				return conversation, nil
			}
			return conversation, session
		}
		return nil, nil
	}
	if conversation, session := pick(user.ActiveConversationID); conversation != nil {
		return conversation, session
	}
	for _, conversation := range conversations {
		if conversation == nil || conversation.Archived || conversation.UserID != userID {
			continue
		}
		session, err := store.GetUserSession(userID, conversation.SessionID)
		if err == nil && session != nil && session.UserID == userID {
			return conversation, session
		}
		return conversation, nil
	}
	return nil, nil
}
