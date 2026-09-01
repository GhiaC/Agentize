package filemanager

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestServiceCRUDAndReads(t *testing.T) {
	s, err := New(Config{Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Mkdir("notes"); err != nil {
		t.Fatal(err)
	}
	if err := s.Write("notes/a.md", []byte("one\ntwo\nthree\nfour"), true); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		opts ReadOptions
		want string
	}{{ReadOptions{Mode: ReadHead, Limit: 2}, "one\ntwo"}, {ReadOptions{Mode: ReadTail, Limit: 2}, "three\nfour"}, {ReadOptions{Mode: ReadLines, Start: 2, End: 3}, "two\nthree"}} {
		got, err := s.Read("notes/a.md", tc.opts)
		if err != nil {
			t.Fatal(err)
		}
		if got.Content != tc.want {
			t.Errorf("content=%q want %q", got.Content, tc.want)
		}
	}
	if err := s.Move("notes/a.md", "notes/b.md"); err != nil {
		t.Fatal(err)
	}
	entries, err := s.List("notes")
	if err != nil || len(entries) != 1 || entries[0].Path != "notes/b.md" {
		t.Fatalf("entries=%#v err=%v", entries, err)
	}
	if err := s.Delete("notes", true); err != nil {
		t.Fatal(err)
	}
}

func TestServiceRejectsEscapesAndRootMutation(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	s, err := New(Config{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.List("../outside"); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("traversal err=%v", err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.List("escape"); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("symlink err=%v", err)
	}
	if err := s.Write("escape", []byte("must not escape"), false); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("symlink write err=%v", err)
	}
	if err := s.Delete("", true); !errors.Is(err, ErrRootMutation) {
		t.Fatalf("root delete err=%v", err)
	}
}
