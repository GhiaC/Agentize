package filemanager

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/ghiac/agentize/model"
)

const FolderMIME = "application/vnd.agentize.folder"

var (
	ErrInvalidPath = errors.New("invalid file-manager path")
	ErrTooLarge    = errors.New("file exceeds configured read limit")
)

type ReadMode string

const (
	ReadFull  ReadMode = "full"
	ReadHead  ReadMode = "head"
	ReadTail  ReadMode = "tail"
	ReadLines ReadMode = "lines"
)

type ReadOptions struct {
	Mode              ReadMode
	Start, End, Limit int
}
type ReadResult struct {
	Path       string   `json:"path"`
	Content    string   `json:"content"`
	Mode       ReadMode `json:"mode"`
	StartLine  int      `json:"start_line"`
	EndLine    int      `json:"end_line"`
	TotalLines int      `json:"total_lines"`
	Truncated  bool     `json:"truncated"`
}

// UserBackend is the Agentize document surface needed by the filesystem view.
// Every mutating/read method receives the owner and must enforce ownership.
type UserBackend interface {
	ListUserFiles(userID string) ([]*model.UserFile, error)
	ReadUserFileForUser(userID, fileID string) ([]byte, *model.UserFile, error)
	RecordUserFileForUser(userID, name, mimeType string, source model.FileSource, data []byte) (*model.UserFile, error)
	UpdateUserFileContentForUser(userID, fileID string, data []byte) (*model.UserFile, error)
	MoveUserFileForUser(userID, fileID, name string) (*model.UserFile, error)
	DeleteUserFileForUser(userID, fileID string) error
}

type UserService struct{ backend UserBackend }

type UserEntry struct {
	ID        string           `json:"id,omitempty"`
	Name      string           `json:"name"`
	Path      string           `json:"path"`
	Kind      string           `json:"kind"`
	MIMEType  string           `json:"mime_type,omitempty"`
	Size      int64            `json:"size"`
	Source    model.FileSource `json:"source,omitempty"`
	SessionID string           `json:"session_id,omitempty"`
	CreatedAt time.Time        `json:"created_at,omitempty"`
}

func NewUserService(backend UserBackend) *UserService { return &UserService{backend: backend} }

func virtualPath(name string) (string, error) {
	name = strings.TrimSpace(strings.ReplaceAll(name, "\\", "/"))
	if name == "" || strings.HasPrefix(name, "/") {
		return "", ErrInvalidPath
	}
	clean := path.Clean(name)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", ErrInvalidPath
	}
	return clean, nil
}

func virtualDir(name string) (string, error) {
	name = strings.Trim(strings.TrimSpace(strings.ReplaceAll(name, "\\", "/")), "/")
	if name == "" {
		return "", nil
	}
	return virtualPath(name)
}

func (s *UserService) List(userID, directory string) ([]UserEntry, error) {
	directory, err := virtualDir(directory)
	if err != nil {
		return nil, err
	}
	files, err := s.backend.ListUserFiles(userID)
	if err != nil {
		return nil, err
	}
	directories := make(map[string]UserEntry)
	var out []UserEntry
	prefix := ""
	if directory != "" {
		prefix = directory + "/"
	}
	for _, file := range files {
		if file == nil {
			continue
		}
		filePath, pathErr := virtualPath(file.Name)
		if pathErr != nil || !strings.HasPrefix(filePath, prefix) {
			continue
		}
		rest := strings.TrimPrefix(filePath, prefix)
		if rest == "" {
			continue
		}
		first, _, hasTail := strings.Cut(rest, "/")
		entryPath := first
		if directory != "" {
			entryPath = directory + "/" + first
		}
		if hasTail {
			if _, ok := directories[entryPath]; !ok {
				directories[entryPath] = UserEntry{Name: first, Path: entryPath, Kind: "directory"}
			}
			continue
		}
		kind := "file"
		if file.MIMEType == FolderMIME {
			kind = "directory"
		}
		entry := UserEntry{ID: file.FileID, Name: first, Path: entryPath, Kind: kind, MIMEType: file.MIMEType, Size: file.Size, Source: file.Source, SessionID: file.SessionID, CreatedAt: file.CreatedAt}
		if kind == "directory" {
			directories[entryPath] = entry
		} else {
			out = append(out, entry)
		}
	}
	for _, entry := range directories {
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind == "directory"
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out, nil
}

func (s *UserService) CreateFolder(userID, name string) (*model.UserFile, error) {
	name, err := virtualPath(name)
	if err != nil {
		return nil, err
	}
	if err := s.ensureAvailable(userID, name, ""); err != nil {
		return nil, err
	}
	return s.backend.RecordUserFileForUser(userID, name, FolderMIME, model.FileSourceUploaded, nil)
}

func (s *UserService) CreateFile(userID, name, mimeType string, data []byte) (*model.UserFile, error) {
	return s.Upload(userID, name, mimeType, data)
}

func (s *UserService) Upload(userID, name, mimeType string, data []byte) (*model.UserFile, error) {
	name, err := virtualPath(name)
	if err != nil {
		return nil, err
	}
	if err := s.ensureAvailable(userID, name, ""); err != nil {
		return nil, err
	}
	return s.backend.RecordUserFileForUser(userID, name, ResolveMIME(name, mimeType, data), model.FileSourceUploaded, data)
}

func (s *UserService) Read(userID, fileID string, opts ReadOptions) (ReadResult, *model.UserFile, error) {
	data, meta, err := s.backend.ReadUserFileForUser(userID, fileID)
	if err != nil {
		return ReadResult{}, meta, err
	}
	if meta.MIMEType == FolderMIME {
		return ReadResult{}, meta, errors.New("cannot read a directory")
	}
	if len(data) > 2<<20 {
		return ReadResult{}, meta, ErrTooLarge
	}
	lines, err := scanLines(bytes.NewReader(data), 1<<20)
	if err != nil {
		return ReadResult{}, meta, err
	}
	mode := opts.Mode
	if mode == "" {
		mode = ReadFull
	}
	start, end := 1, len(lines)
	switch mode {
	case ReadFull:
	case ReadHead:
		if opts.Limit <= 0 {
			opts.Limit = 50
		}
		end = min(opts.Limit, len(lines))
	case ReadTail:
		if opts.Limit <= 0 {
			opts.Limit = 50
		}
		start = max(1, len(lines)-opts.Limit+1)
	case ReadLines:
		start, end = opts.Start, opts.End
		if start <= 0 {
			start = 1
		}
		if end <= 0 {
			end = start
		}
		if start > end {
			return ReadResult{}, meta, errors.New("start line must not exceed end line")
		}
		if start > len(lines) {
			start = len(lines) + 1
		}
		end = min(end, len(lines))
	default:
		return ReadResult{}, meta, fmt.Errorf("unsupported read mode %q", mode)
	}
	selected := []string{}
	if start <= end && start <= len(lines) {
		selected = lines[start-1 : end]
	}
	return ReadResult{Path: meta.Name, Content: strings.Join(selected, "\n"), Mode: mode, StartLine: start, EndLine: end, TotalLines: len(lines), Truncated: len(selected) < len(lines)}, meta, nil
}

func (s *UserService) Write(userID, fileID string, data []byte) (*model.UserFile, error) {
	return s.backend.UpdateUserFileContentForUser(userID, fileID, data)
}

func (s *UserService) Move(userID, fileID, destination string) (*model.UserFile, error) {
	destination, err := virtualPath(destination)
	if err != nil {
		return nil, err
	}
	files, err := s.backend.ListUserFiles(userID)
	if err != nil {
		return nil, err
	}
	var target *model.UserFile
	for _, file := range files {
		if file != nil && file.FileID == fileID {
			target = file
			break
		}
	}
	if target == nil {
		return nil, osNotExist()
	}
	if directoryAt(files, destination) {
		destination, err = virtualPath(destination + "/" + path.Base(strings.TrimSuffix(target.Name, "/")))
		if err != nil {
			return nil, err
		}
	}
	if err = s.ensureAvailable(userID, destination, fileID); err != nil {
		return nil, err
	}
	oldPath := strings.TrimSuffix(target.Name, "/")
	updated, err := s.backend.MoveUserFileForUser(userID, fileID, destination)
	if err != nil {
		return nil, err
	}
	if target.MIMEType == FolderMIME {
		prefix := oldPath + "/"
		for _, child := range files {
			if child != nil && strings.HasPrefix(child.Name, prefix) {
				if _, moveErr := s.backend.MoveUserFileForUser(userID, child.FileID, destination+"/"+strings.TrimPrefix(child.Name, prefix)); moveErr != nil {
					return nil, fmt.Errorf("move folder child %s: %w", child.FileID, moveErr)
				}
			}
		}
	}
	return updated, nil
}

func (s *UserService) Delete(userID, fileID string, recursive bool) error {
	files, err := s.backend.ListUserFiles(userID)
	if err != nil {
		return err
	}
	var target *model.UserFile
	for _, f := range files {
		if f != nil && f.FileID == fileID {
			target = f
			break
		}
	}
	if target == nil {
		return osNotExist()
	}
	if target.MIMEType != FolderMIME {
		return s.backend.DeleteUserFileForUser(userID, fileID)
	}
	prefix := strings.TrimSuffix(target.Name, "/") + "/"
	var children []*model.UserFile
	for _, f := range files {
		if f != nil && strings.HasPrefix(f.Name, prefix) {
			children = append(children, f)
		}
	}
	if len(children) > 0 && !recursive {
		return errors.New("directory is not empty")
	}
	for _, f := range children {
		if err := s.backend.DeleteUserFileForUser(userID, f.FileID); err != nil {
			return err
		}
	}
	return s.backend.DeleteUserFileForUser(userID, fileID)
}

func (s *UserService) ensureAvailable(userID, name, exceptID string) error {
	files, err := s.backend.ListUserFiles(userID)
	if err != nil {
		return err
	}
	for _, f := range files {
		if f != nil && f.FileID != exceptID && strings.EqualFold(strings.TrimSuffix(f.Name, "/"), strings.TrimSuffix(name, "/")) {
			return errors.New("an entry already exists at this path")
		}
	}
	return nil
}

func directoryAt(files []*model.UserFile, dest string) bool {
	if dest == "" {
		return false
	}
	prefix := dest + "/"
	for _, file := range files {
		if file == nil {
			continue
		}
		filePath, err := virtualPath(file.Name)
		if err != nil {
			continue
		}
		if filePath == dest && file.MIMEType == FolderMIME {
			return true
		}
		if strings.HasPrefix(filePath, prefix) {
			return true
		}
	}
	return false
}

func osNotExist() error { return errors.New("file not found") }

// extraMIME covers types Go's mime package does not always know about.
var extraMIME = map[string]string{
	".txt":      "text/plain; charset=utf-8",
	".md":       "text/markdown; charset=utf-8",
	".markdown": "text/markdown; charset=utf-8",
	".csv":      "text/csv; charset=utf-8",
	".json":     "application/json",
	".png":      "image/png",
	".jpg":      "image/jpeg",
	".jpeg":     "image/jpeg",
	".gif":      "image/gif",
	".webp":     "image/webp",
	".svg":      "image/svg+xml",
	".bmp":      "image/bmp",
	".ico":      "image/x-icon",
	".pdf":      "application/pdf",
	".html":     "text/html; charset=utf-8",
	".htm":      "text/html; charset=utf-8",
	".xml":      "application/xml",
	".yaml":     "text/yaml; charset=utf-8",
	".yml":      "text/yaml; charset=utf-8",
	".js":       "text/javascript; charset=utf-8",
	".ts":       "text/plain; charset=utf-8",
	".css":      "text/css; charset=utf-8",
}

// ResolveMIME picks a content type from the stored value, filename, then bytes.
// Empty and generic octet-stream types are treated as unknown so uploads keep
// a usable image/text type for previews.
func ResolveMIME(name, mimeType string, data []byte) string {
	mimeType = strings.TrimSpace(strings.Split(mimeType, ";")[0])
	if mimeType != "" && !strings.EqualFold(mimeType, "application/octet-stream") && !strings.EqualFold(mimeType, "binary/octet-stream") {
		return mimeType
	}
	ext := strings.ToLower(path.Ext(name))
	if t := extraMIME[ext]; t != "" {
		return t
	}
	if t := mime.TypeByExtension(ext); t != "" {
		return t
	}
	if len(data) > 0 {
		return http.DetectContentType(data)
	}
	return "application/octet-stream"
}

func IsImageMIME(mimeType string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(mimeType)), "image/")
}

func scanLines(r io.Reader, maxLine int) ([]string, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), maxLine)
	var lines []string
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	return lines, scanner.Err()
}

// OwnerResolver extracts the authenticated product user from a request.
type OwnerResolver func(*http.Request) string
