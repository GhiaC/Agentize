package components

import (
	"strings"
	"testing"
)

func TestTagBadgesWrapAndEscape(t *testing.T) {
	if got := TagBadges(nil); got != "-" {
		t.Fatalf("empty tags = %q, want -", got)
	}
	html := TagBadges([]string{"concise", "long-tag-that-should-wrap", `<script>`})
	if !strings.Contains(html, `class="tag-badges"`) {
		t.Fatalf("expected wrapping container, got %s", html)
	}
	if !strings.Contains(html, "concise") || !strings.Contains(html, "long-tag-that-should-wrap") {
		t.Fatalf("missing tags: %s", html)
	}
	if strings.Contains(html, `<script>`) {
		t.Fatal("tag text must be HTML-escaped")
	}
	if !strings.Contains(html, `&lt;script&gt;`) {
		t.Fatalf("expected escaped tag, got %s", html)
	}
}
