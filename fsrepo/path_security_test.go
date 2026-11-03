package fsrepo

import (
	"os"
	"path/filepath"
	"testing"
)

func TestKnowledgePathsRejectTraversalAndSymlinkAliases(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	if err := os.MkdirAll(filepath.Join(root, "private"), 0755); err != nil {
		t.Fatal(err)
	}
	repo, err := NewNodeRepository(base)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "private"), filepath.Join(root, "alias")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"../root", "root/../../outside", root, `root\private`, "root/alias"} {
		if _, err := repo.LoadNode(name); err == nil {
			t.Fatalf("unsafe path accepted: %s", name)
		}
		if _, err := repo.GetChildren(name); err == nil {
			t.Fatalf("unsafe traversal accepted: %s", name)
		}
	}
	if node, err := repo.LoadNode("root/./private"); err != nil || node.Path != "root/private" {
		t.Fatalf("canonical node rejected: %v %v", node, err)
	}
	// Files are checked before the cache too, so replacing node.yaml with a link cannot retain a cached grant.
	if _, err := repo.LoadNode("root/private"); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(base, "outside.yaml")
	if err := os.WriteFile(target, []byte("id: outside\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, "private", "node.yaml")); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.LoadNode("root/private"); err == nil {
		t.Fatal("symlinked policy bypassed a cached node")
	}
}
