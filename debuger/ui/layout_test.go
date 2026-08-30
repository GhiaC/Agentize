package ui

import (
	"strings"
	"testing"
)

func TestNavbarAndBodyMarksReplaceableContentRegion(t *testing.T) {
	html := NavbarAndBody("/agentize/debug/users", "<p>users</p>")

	if !strings.Contains(html, `id="sidebar"`) {
		t.Fatal("dashboard shell must contain the persistent sidebar")
	}
	if !strings.Contains(html, `id="dashboard-content"`) {
		t.Fatal("dashboard shell must expose a replaceable content region")
	}
	if !strings.Contains(html, `aria-live="polite"`) {
		t.Fatal("replaceable content region must announce updates")
	}
}

func TestScriptsUseContentNavigationWithoutReplacingSidebar(t *testing.T) {
	script := GetScripts()
	for _, want := range []string{
		"fetch(url.href",
		"nextDocument.getElementById('dashboard-content')",
		"content.innerHTML = nextContent.innerHTML",
		"window.history.pushState",
		"window.addEventListener('popstate'",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("content navigation script is missing %q", want)
		}
	}
	if strings.Contains(script, "app.innerHTML") {
		t.Fatal("content navigation must not replace the persistent app shell")
	}
}
