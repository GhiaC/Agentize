package filemanager

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ghiac/agentize/model"
)

type memoryUserFiles struct {
	files map[string]*model.UserFile
	data  map[string][]byte
	seq   int
}

func newMemoryUserFiles() *memoryUserFiles {
	return &memoryUserFiles{files: map[string]*model.UserFile{}, data: map[string][]byte{}}
}
func (m *memoryUserFiles) ListUserFiles(user string) (out []*model.UserFile, err error) {
	for _, f := range m.files {
		if f.UserID == user {
			copy := *f
			out = append(out, &copy)
		}
	}
	return
}
func (m *memoryUserFiles) ReadUserFileForUser(user, id string) ([]byte, *model.UserFile, error) {
	f := m.files[id]
	if f == nil || f.UserID != user {
		return nil, nil, fmt.Errorf("not found")
	}
	return append([]byte(nil), m.data[id]...), f, nil
}
func (m *memoryUserFiles) RecordUserFileForUser(user, name, mime string, source model.FileSource, data []byte) (*model.UserFile, error) {
	m.seq++
	id := fmt.Sprintf("f-%d", m.seq)
	f := &model.UserFile{FileID: id, UserID: user, Name: name, MIMEType: mime, Size: int64(len(data)), Source: source, CreatedAt: time.Now()}
	m.files[id] = f
	m.data[id] = append([]byte(nil), data...)
	return f, nil
}
func (m *memoryUserFiles) UpdateUserFileContentForUser(user, id string, data []byte) (*model.UserFile, error) {
	f := m.files[id]
	if f == nil || f.UserID != user {
		return nil, fmt.Errorf("not found")
	}
	m.data[id] = append([]byte(nil), data...)
	f.Size = int64(len(data))
	return f, nil
}
func (m *memoryUserFiles) MoveUserFileForUser(user, id, name string) (*model.UserFile, error) {
	f := m.files[id]
	if f == nil || f.UserID != user {
		return nil, fmt.Errorf("not found")
	}
	f.Name = name
	return f, nil
}
func (m *memoryUserFiles) DeleteUserFileForUser(user, id string) error {
	f := m.files[id]
	if f == nil || f.UserID != user {
		return fmt.Errorf("not found")
	}
	delete(m.files, id)
	delete(m.data, id)
	return nil
}

func TestUserServiceIsolatesOwnersAndManagesFolderTrees(t *testing.T) {
	b := newMemoryUserFiles()
	s := NewUserService(b)
	folder, err := s.CreateFolder("alice", "research")
	if err != nil {
		t.Fatal(err)
	}
	file, err := s.Upload("alice", "research/notes.md", "text/markdown", []byte("one\ntwo\nthree"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Upload("bob", "private.txt", "text/plain", []byte("secret")); err != nil {
		t.Fatal(err)
	}
	// Legacy uploads may share a display path; the filesystem must not hide one.
	b.seq++
	duplicate := &model.UserFile{FileID: fmt.Sprintf("f-%d", b.seq), UserID: "alice", Name: "research/notes.md", MIMEType: "text/markdown"}
	b.files[duplicate.FileID] = duplicate
	items, err := s.List("alice", "research")
	if err != nil || len(items) != 2 {
		t.Fatalf("alice items=%#v err=%v", items, err)
	}
	if _, _, err = s.Read("bob", file.FileID, ReadOptions{}); err == nil {
		t.Fatal("cross-owner read succeeded")
	}
	if _, err = s.Move("alice", folder.FileID, "archive"); err != nil {
		t.Fatal(err)
	}
	if b.files[file.FileID].Name != "archive/notes.md" {
		t.Fatalf("child path=%q", b.files[file.FileID].Name)
	}
	if err = s.Delete("alice", folder.FileID, true); err != nil {
		t.Fatal(err)
	}
	if len(b.files) != 1 {
		t.Fatalf("remaining files=%d, want bob only", len(b.files))
	}
}

func TestCreateTextCSVMarkdownAndMoveIntoFolder(t *testing.T) {
	s := NewUserService(newMemoryUserFiles())
	folder, err := s.CreateFolder("alice", "notes")
	if err != nil {
		t.Fatal(err)
	}
	txt, err := s.CreateFile("alice", "readme.txt", "", []byte("hello"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(txt.MIMEType, "text/plain") {
		t.Fatalf("txt mime=%q", txt.MIMEType)
	}
	md, err := s.CreateFile("alice", "plan.md", "", []byte("# Title"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(md.MIMEType, "markdown") {
		t.Fatalf("md mime=%q", md.MIMEType)
	}
	csv, err := s.CreateFile("alice", "rows.csv", "", []byte("a,b\n1,2"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(csv.MIMEType, "csv") {
		t.Fatalf("csv mime=%q", csv.MIMEType)
	}
	moved, err := s.Move("alice", txt.FileID, folder.Name)
	if err != nil {
		t.Fatal(err)
	}
	if moved.Name != "notes/readme.txt" {
		t.Fatalf("moved path=%q", moved.Name)
	}
	items, err := s.List("alice", "notes")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range items {
		if item.Name == "readme.txt" {
			found = true
		}
	}
	if !found {
		t.Fatalf("file not listed in destination folder: %#v", items)
	}
}

func TestResolveMIMEInfersImagesAndText(t *testing.T) {
	png := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}
	if got := ResolveMIME("shot.png", "application/octet-stream", png); got != "image/png" {
		t.Fatalf("png mime=%q", got)
	}
	if got := ResolveMIME("notes.md", "", nil); !strings.Contains(got, "markdown") {
		t.Fatalf("md mime=%q", got)
	}
	if got := ResolveMIME("rows.csv", "binary/octet-stream", nil); !strings.Contains(got, "csv") {
		t.Fatalf("csv mime=%q", got)
	}
	if !IsImageMIME("image/jpeg; charset=binary") {
		t.Fatal("jpeg should count as image")
	}
}
