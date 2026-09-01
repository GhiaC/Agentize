package ui

import "testing"

func TestDefaultNavUsesSingleFileSystemPage(t *testing.T) {
	var fileSystem int
	for _, item := range DefaultNavItems() {
		if item.URL == "/agentize/debug/files" {
			t.Fatal("duplicate opened-files page is still in navigation")
		}
		if item.URL == "/agentize/debug/documents" && item.Text == "File System" {
			fileSystem++
		}
	}
	if fileSystem != 1 {
		t.Fatalf("file system nav entries=%d, want 1", fileSystem)
	}
}
