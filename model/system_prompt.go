package model

// SystemPromptEntry is one ordered system message in the last prompt array
// assembled for a session. It is persisted for observability; transcript
// messages remain in Session.Msgs and never contain runtime prompt snapshots.
type SystemPromptEntry struct {
	Key     string `json:"key"`
	Title   string `json:"title"`
	Content string `json:"content"`
	Source  string `json:"source,omitempty"`
}
