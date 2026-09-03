package components

import (
	"fmt"
	"html/template"
	"strings"

	"github.com/ghiac/agentize/model"
)

// EntityID renders an Agentize entity id for operators. Legacy concatenated
// ids show as their numeric seq so the deprecated form is unused in the UI.
func EntityID(id string) string {
	if strings.TrimSpace(id) == "" {
		return ""
	}
	return InlineCode(model.DisplayID(id))
}

// EntityIDCodeBlock is the block variant for detail pages.
func EntityIDCodeBlock(id string) string {
	if strings.TrimSpace(id) == "" {
		return ""
	}
	return CodeBlock(model.DisplayID(id))
}

// EntityIDLink shows DisplayID as the label and keeps href on the stored id
// so lookups still resolve leftover concat rows.
func EntityIDLink(id, href string) string {
	if strings.TrimSpace(id) == "" {
		return ""
	}
	return fmt.Sprintf(`<a href="%s" class="text-decoration-none">%s</a>`, href, EntityID(id))
}

// EntityIDText is a plain escaped DisplayID without a code tag.
func EntityIDText(id string) string {
	return template.HTMLEscapeString(model.DisplayID(id))
}
