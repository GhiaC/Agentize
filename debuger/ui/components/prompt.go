package components

import (
	"fmt"
	"html/template"
	"strings"

	"github.com/ghiac/agentize/model"
)

// PromptArrayEntry is one independently readable system-prompt document for the
// debug UI. Core PromptSection values and persisted SystemPromptEntry values
// both project into this view.
type PromptArrayEntry struct {
	Key     string
	Title   string
	Content string
	Source  string
	Bytes   int
	Note    string

	// Core-only metadata. HasCoreMeta is false for worker-session snapshots.
	HasCoreMeta bool
	Required    bool
	Dynamic     bool
	Included    bool
}

// PromptEntriesFromSnapshot projects a persisted worker/core snapshot.
func PromptEntriesFromSnapshot(entries []model.SystemPromptEntry) []PromptArrayEntry {
	out := make([]PromptArrayEntry, 0, len(entries))
	for _, entry := range entries {
		out = append(out, PromptArrayEntry{
			Key:     entry.Key,
			Title:   entry.Title,
			Content: entry.Content,
			Source:  entry.Source,
			Bytes:   len(entry.Content),
		})
	}
	return out
}

// PromptEntriesFromSections projects the Core's live/preview section array.
func PromptEntriesFromSections(sections []model.PromptSection) []PromptArrayEntry {
	out := make([]PromptArrayEntry, 0, len(sections))
	for _, section := range sections {
		out = append(out, PromptArrayEntry{
			Key:         section.Key,
			Title:       section.Title,
			Content:     section.Content,
			Bytes:       section.Bytes,
			Note:        section.Note,
			HasCoreMeta: true,
			Required:    section.Required,
			Dynamic:     section.Dynamic,
			Included:    section.Included,
		})
	}
	return out
}

// ToolRetrievablePrompt reports whether an entry is knowledge, web, files, or
// full position lists that belong behind tools rather than in the current
// prompt array. A compact account-status summary is allowed.
func ToolRetrievablePrompt(key, title string) (bool, string) {
	k := strings.ToLower(strings.TrimSpace(key))
	t := strings.ToLower(strings.TrimSpace(title))
	haystack := k + " " + t
	switch {
	case k == "account_status" || strings.Contains(haystack, "account status"):
		return false, ""
	case strings.Contains(haystack, "knowledge"):
		return true, "Knowledge is searched and read with manage_knowledge. Node content is never a prompt entry."
	case strings.Contains(k, "web") || strings.Contains(t, "web result") || strings.Contains(t, "web search"):
		return true, "Web results stay behind search tools and are not copied into the prompt."
	case strings.Contains(haystack, "position"):
		return true, "Open positions are read with account tools. Only a short account-status summary belongs in the prompt."
	case strings.HasPrefix(k, "opened_node") || k == "opened_files" || k == "opened_nodes" || k == "opened_tools" || k == "file_index":
		return true, "Opened nodes grant tools. Their content is not copied into the prompt."
	case k == "user_files" || strings.Contains(t, "user files"):
		return true, "User files are listed and edited with manage_files."
	default:
		return false, ""
	}
}

// RenderPromptArray renders an ordered prompt array as an index plus one
// independently expandable document per entry. Tool-retrievable dumps are kept
// out of the current list so the operator can read the live contract.
func RenderPromptArray(entries []PromptArrayEntry) string {
	var current, excluded []PromptArrayEntry
	for _, entry := range entries {
		if excludedDump, _ := ToolRetrievablePrompt(entry.Key, entry.Title); excludedDump {
			excluded = append(excluded, entry)
			continue
		}
		current = append(current, entry)
	}

	var b strings.Builder
	if len(current) == 0 && len(excluded) == 0 {
		b.WriteString(InfoAlert("No system-prompt snapshot has been assembled yet."))
		return b.String()
	}

	if len(current) > 0 {
		b.WriteString(`<div class="table-responsive mb-3"><table class="table table-sm align-middle mb-0"><thead><tr>`)
		b.WriteString(`<th class="text-center" style="width:3rem">#</th><th>Prompt</th><th>Key</th><th>Source</th><th class="text-nowrap">Size</th><th>Role</th>`)
		b.WriteString(`</tr></thead><tbody>`)
		for i, entry := range current {
			anchor := promptAnchor(entry.Key, i)
			b.WriteString(fmt.Sprintf(`<tr><td class="text-center">%d</td><td><a href="#%s">%s</a></td><td>%s</td><td>%s</td><td class="text-nowrap">%s</td><td>%s</td></tr>`,
				i+1, template.HTMLEscapeString(anchor), template.HTMLEscapeString(promptTitle(entry)),
				InlineCode(entry.Key), sourceCell(entry.Source), formatPromptBytes(entry.Bytes), roleBadges(entry)))
		}
		b.WriteString(`</tbody></table></div>`)
		for i, entry := range current {
			b.WriteString(renderPromptDocument(i+1, entry, i == 0 && entry.Content != ""))
		}
	}

	if len(excluded) > 0 {
		b.WriteString(WarningAlert("These entries are knowledge, web results, files, or full position lists. They are tool data, not current prompt documents, and are hidden from the live array."))
		b.WriteString(CollapsibleCardStartWithCount("Excluded from current prompt", "slash-circle", len(excluded), false))
		for i, entry := range excluded {
			_, reason := ToolRetrievablePrompt(entry.Key, entry.Title)
			if entry.Note == "" {
				entry.Note = reason
			}
			b.WriteString(renderPromptDocument(i+1, entry, false))
		}
		b.WriteString(CollapsibleCardEnd())
	}
	return b.String()
}

func renderPromptDocument(idx int, entry PromptArrayEntry, open bool) string {
	var badges []string
	if entry.HasCoreMeta {
		if entry.Required {
			badges = append(badges, Badge("Required", "danger"))
		} else {
			badges = append(badges, Badge("Optional", "secondary"))
		}
		if entry.Dynamic {
			badges = append(badges, Badge("Dynamic", "info"))
		} else {
			badges = append(badges, Badge("Static", "success"))
		}
		if entry.Content == "" {
			badges = append(badges, Badge("Empty", "secondary"))
		} else if !entry.Included {
			badges = append(badges, Badge("Dropped (budget)", "warning text-dark"))
		}
	}
	if entry.Key != "" {
		badges = append(badges, Badge(entry.Key, "secondary"))
	}
	badges = append(badges, Badge(formatPromptBytes(entry.Bytes), "info"))
	if entry.Source != "" {
		badges = append(badges, Badge(entry.Source, "light text-dark"))
	}

	var body strings.Builder
	if entry.Note != "" {
		body.WriteString(InfoAlert(entry.Note))
	}
	if entry.Content != "" {
		body.WriteString(fmt.Sprintf("<pre>%s</pre>", template.HTMLEscapeString(entry.Content)))
	} else if entry.Note == "" {
		body.WriteString(`<p class="text-muted mb-0"><em>(empty)</em></p>`)
	}

	title := fmt.Sprintf("%d. %s", idx, promptTitle(entry))
	html := CollapsibleSection(title, strings.Join(badges, " "), body.String(), open)
	anchor := promptAnchor(entry.Key, idx-1)
	return strings.Replace(html, `<details class="collapsible-section"`, `<details id="`+template.HTMLEscapeString(anchor)+`" class="collapsible-section"`, 1)
}

func promptTitle(entry PromptArrayEntry) string {
	if strings.TrimSpace(entry.Title) != "" {
		return entry.Title
	}
	if strings.TrimSpace(entry.Key) != "" {
		return entry.Key
	}
	return "Untitled prompt"
}

func promptAnchor(key string, idx int) string {
	cleaned := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' {
			return r
		}
		return '-'
	}, key)
	if cleaned == "" {
		cleaned = "prompt"
	}
	return fmt.Sprintf("prompt-%s-%d", cleaned, idx)
}

func sourceCell(source string) string {
	if strings.TrimSpace(source) == "" {
		return `<span class="text-muted">—</span>`
	}
	return InlineCode(source)
}

func roleBadges(entry PromptArrayEntry) string {
	if excluded, _ := ToolRetrievablePrompt(entry.Key, entry.Title); excluded {
		return Badge("tool data", "warning text-dark")
	}
	switch entry.Key {
	case "agent_instructions", "core_controller":
		return Badge("instructions", "success")
	case "user_context":
		return Badge("user memory", "info")
	case "session_context", "core_session_context":
		return Badge("session memory", "info")
	case "account_status":
		return Badge("account status", "primary")
	default:
		if entry.Content == "" {
			return Badge("empty", "secondary")
		}
		return Badge("prompt", "secondary")
	}
}

func formatPromptBytes(n int) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	return fmt.Sprintf("%.1f KB", float64(n)/1024)
}
