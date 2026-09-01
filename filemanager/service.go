// Package filemanager provides a sandboxed, host-embeddable file manager.
//
// A Service owns one filesystem root. Every public path is slash-separated and
// relative to that root; absolute paths, traversal and symlink escapes are
// rejected. Hosts should expose the HTTP handler instead of reimplementing file
// operations so the same safety and read semantics are shared everywhere.
package filemanager

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

var (
	ErrInvalidPath  = errors.New("invalid file-manager path")
	ErrRootMutation = errors.New("file-manager root cannot be mutated")
	ErrTooLarge     = errors.New("file exceeds configured read limit")
)

type Config struct {
	Root         string
	MaxReadBytes int64
	MaxLineBytes int
}

type Service struct {
	root         string
	maxReadBytes int64
	maxLineBytes int
}

type Entry struct {
	Name       string    `json:"name"`
	Path       string    `json:"path"`
	Kind       string    `json:"kind"`
	Size       int64     `json:"size"`
	ModifiedAt time.Time `json:"modified_at"`
}

type ReadMode string

const (
	ReadFull  ReadMode = "full"
	ReadHead  ReadMode = "head"
	ReadTail  ReadMode = "tail"
	ReadLines ReadMode = "lines"
)

type ReadOptions struct {
	Mode  ReadMode
	Start int // one-based, inclusive; used by lines
	End   int // one-based, inclusive; used by lines
	Limit int // line count; used by head/tail
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

func New(cfg Config) (*Service, error) {
	if strings.TrimSpace(cfg.Root) == "" {
		return nil, fmt.Errorf("filemanager: root is required")
	}
	root, err := filepath.Abs(cfg.Root)
	if err != nil {
		return nil, fmt.Errorf("filemanager: resolve root: %w", err)
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("filemanager: create root: %w", err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return nil, fmt.Errorf("filemanager: canonicalize root: %w", err)
	}
	if cfg.MaxReadBytes <= 0 {
		cfg.MaxReadBytes = 2 << 20
	}
	if cfg.MaxLineBytes <= 0 {
		cfg.MaxLineBytes = 1 << 20
	}
	return &Service{root: root, maxReadBytes: cfg.MaxReadBytes, maxLineBytes: cfg.MaxLineBytes}, nil
}

func (s *Service) Root() string { return s.root }

func cleanRelative(path string) (string, error) {
	path = strings.TrimSpace(strings.ReplaceAll(path, "\\", "/"))
	if path == "" || path == "." {
		return "", nil
	}
	if strings.HasPrefix(path, "/") {
		return "", ErrInvalidPath
	}
	clean := filepath.Clean(filepath.FromSlash(path))
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || filepath.IsAbs(clean) {
		return "", ErrInvalidPath
	}
	return clean, nil
}

func (s *Service) resolve(path string, allowMissing bool) (string, string, error) {
	rel, err := cleanRelative(path)
	if err != nil {
		return "", "", err
	}
	full := filepath.Join(s.root, rel)
	check := full
	if allowMissing {
		// Validate the final component when it already exists (it may be a
		// symlink); only fall back to the parent for a genuinely new entry.
		if _, statErr := os.Lstat(full); os.IsNotExist(statErr) {
			check = filepath.Dir(full)
		} else if statErr != nil {
			return "", "", statErr
		}
	}
	canonical, err := filepath.EvalSymlinks(check)
	if err != nil {
		return "", "", err
	}
	inside, err := filepath.Rel(s.root, canonical)
	if err != nil || inside == ".." || strings.HasPrefix(inside, ".."+string(filepath.Separator)) {
		return "", "", ErrInvalidPath
	}
	return full, filepath.ToSlash(rel), nil
}

func (s *Service) List(path string) ([]Entry, error) {
	full, rel, err := s.resolve(path, false)
	if err != nil {
		return nil, err
	}
	items, err := os.ReadDir(full)
	if err != nil {
		return nil, err
	}
	out := make([]Entry, 0, len(items))
	for _, item := range items {
		info, err := item.Info()
		if err != nil {
			return nil, err
		}
		kind := "file"
		if item.IsDir() {
			kind = "directory"
		} else if item.Type()&os.ModeSymlink != 0 {
			kind = "symlink"
		}
		child := item.Name()
		if rel != "" {
			child = rel + "/" + child
		}
		out = append(out, Entry{Name: item.Name(), Path: child, Kind: kind, Size: info.Size(), ModifiedAt: info.ModTime().UTC()})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind == "directory"
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out, nil
}

func (s *Service) Read(path string, opts ReadOptions) (ReadResult, error) {
	full, rel, err := s.resolve(path, false)
	if err != nil {
		return ReadResult{}, err
	}
	info, err := os.Stat(full)
	if err != nil {
		return ReadResult{}, err
	}
	if !info.Mode().IsRegular() {
		return ReadResult{}, fmt.Errorf("filemanager: %s is not a regular file", rel)
	}
	if info.Size() > s.maxReadBytes {
		return ReadResult{}, ErrTooLarge
	}
	f, err := os.Open(full)
	if err != nil {
		return ReadResult{}, err
	}
	defer f.Close()
	lines, err := scanLines(f, s.maxLineBytes)
	if err != nil {
		return ReadResult{}, err
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
			return ReadResult{}, fmt.Errorf("filemanager: start line must not exceed end line")
		}
		if start > len(lines) {
			start = len(lines) + 1
		}
		end = min(end, len(lines))
	default:
		return ReadResult{}, fmt.Errorf("filemanager: unsupported read mode %q", mode)
	}
	selected := []string{}
	if start <= end && start <= len(lines) {
		selected = lines[start-1 : end]
	}
	return ReadResult{Path: rel, Content: strings.Join(selected, "\n"), Mode: mode, StartLine: start, EndLine: end, TotalLines: len(lines), Truncated: len(selected) < len(lines)}, nil
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

func (s *Service) Write(path string, content []byte, createOnly bool) error {
	if int64(len(content)) > s.maxReadBytes {
		return ErrTooLarge
	}
	full, rel, err := s.resolve(path, true)
	if err != nil {
		return err
	}
	if rel == "" {
		return ErrRootMutation
	}
	flags := os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	if createOnly {
		flags |= os.O_EXCL
	}
	f, err := os.OpenFile(full, flags, 0o644)
	if err != nil {
		return err
	}
	if _, err = f.Write(content); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

func (s *Service) Mkdir(path string) error {
	full, rel, err := s.resolve(path, true)
	if err != nil {
		return err
	}
	if rel == "" {
		return ErrRootMutation
	}
	return os.Mkdir(full, 0o755)
}

func (s *Service) Move(source, destination string) error {
	src, srcRel, err := s.resolve(source, false)
	if err != nil {
		return err
	}
	dst, dstRel, err := s.resolve(destination, true)
	if err != nil {
		return err
	}
	if srcRel == "" || dstRel == "" {
		return ErrRootMutation
	}
	if _, err := os.Lstat(dst); err == nil {
		return os.ErrExist
	} else if !os.IsNotExist(err) {
		return err
	}
	return os.Rename(src, dst)
}

func (s *Service) Delete(path string, recursive bool) error {
	full, rel, err := s.resolve(path, false)
	if err != nil {
		return err
	}
	if rel == "" {
		return ErrRootMutation
	}
	info, err := os.Lstat(full)
	if err != nil {
		return err
	}
	if info.IsDir() && recursive {
		return os.RemoveAll(full)
	}
	return os.Remove(full)
}
