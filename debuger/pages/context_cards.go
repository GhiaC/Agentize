package pages

import (
	"fmt"
	"html/template"
	"strings"

	"github.com/ghiac/agentize/debuger"
	"github.com/ghiac/agentize/debuger/ui/components"
	"github.com/ghiac/agentize/model"
)

func renderUserContextCard(user *model.User) string {
	count := 0
	if user != nil {
		count = len(user.ContextSummary) + len(user.ContextTags)
	}
	out := components.CollapsibleCardStartWithCount("User Context", "person-lines-fill", count, true)
	out += `<p class="text-muted mb-3">Cross-conversation facts. The summarizer appends entries and tags; this is memory, not a new instruction.</p>`
	if user == nil || (len(user.ContextSummary) == 0 && len(user.ContextTags) == 0) {
		out += components.InfoAlert("No cross-conversation facts yet. Entries appear here after conversations are summarized.")
		out += components.CollapsibleCardEnd()
		return out
	}
	if len(user.ContextSummary) > 0 {
		out += `<h6>Summary</h6>` + renderSummaryEntries(user.ContextSummary)
	}
	if len(user.ContextTags) > 0 {
		out += `<h6>Tags</h6>` + components.TagBadges(user.ContextTags)
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
	out += `<p class="text-muted mb-3">Title, summary entries, and tags for the active conversation. Detailed history stays in messages and tools.</p>`
	if conversation != nil {
		out += fmt.Sprintf(`<p class="mb-2"><strong>Conversation:</strong> %s</p>`, components.InlineCode(conversation.ConversationID))
	}
	if session == nil || (session.Title == "" && len(session.Summary) == 0 && len(session.Tags) == 0) {
		out += components.InfoAlert("No title, summary, or tags on the active conversation yet.")
		out += components.CollapsibleCardEnd()
		return out
	}
	title := session.Title
	if title == "" {
		title = "Untitled"
	}
	out += fmt.Sprintf(`<p class="mb-2"><strong>Title:</strong> %s</p>`, template.HTMLEscapeString(title))
	if len(session.Summary) > 0 {
		out += `<h6>Summary</h6>` + renderSummaryEntries(session.Summary)
	}
	if len(session.Tags) > 0 {
		out += `<h6>Tags</h6>` + components.TagBadges(session.Tags)
	}
	out += components.CollapsibleCardEnd()
	return out
}

func renderSummaryEntries(entries model.SummaryEntries) string {
	if len(entries) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(`<ol class="mb-3 ps-3">`)
	for _, entry := range entries {
		b.WriteString(`<li class="mb-2">`)
		b.WriteString(template.HTMLEscapeString(entry))
		b.WriteString(`</li>`)
	}
	b.WriteString(`</ol>`)
	return b.String()
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
			session, err := store.GetSession(conversation.SessionID)
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
		session, err := store.GetSession(conversation.SessionID)
		if err == nil && session != nil && session.UserID == userID {
			return conversation, session
		}
		return conversation, nil
	}
	return nil, nil
}
