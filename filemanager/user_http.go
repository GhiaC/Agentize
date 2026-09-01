package filemanager

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ghiac/agentize/metrics"
)

type UserHandler struct {
	service   *UserService
	owner     OwnerResolver
	maxUpload int64
}

type createRequest struct {
	Path     string `json:"path"`
	Kind     string `json:"kind"`
	MIMEType string `json:"mime_type,omitempty"`
	Content  string `json:"content,omitempty"`
}

func NewUserHandler(service *UserService, owner OwnerResolver) *UserHandler {
	return &UserHandler{service: service, owner: owner, maxUpload: 20 << 20}
}

func (h *UserHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	userID := ""
	if h.owner != nil {
		userID = strings.TrimSpace(h.owner(r))
	}
	if userID == "" {
		writeError(w, fmt.Errorf("authentication required"), http.StatusUnauthorized)
		return
	}
	endpoint := strings.TrimSuffix(r.URL.Path, "/")
	endpoint = endpoint[strings.LastIndex(endpoint, "/")+1:]
	switch endpoint {
	case "entries":
		h.entries(w, r, userID)
	case "file":
		h.file(w, r, userID)
	case "move":
		h.move(w, r, userID)
	case "upload":
		h.upload(w, r, userID)
	case "raw":
		h.raw(w, r, userID)
	default:
		writeError(w, fmt.Errorf("not found"), http.StatusNotFound)
	}
}

func (h *UserHandler) entries(w http.ResponseWriter, r *http.Request, user string) {
	switch r.Method {
	case http.MethodGet:
		start := time.Now()
		items, err := h.service.List(user, r.URL.Query().Get("path"))
		observeFileOp("list", err, start, 0, "")
		if err != nil {
			writeServiceError(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"path": r.URL.Query().Get("path"), "entries": items})
	case http.MethodPost:
		var req createRequest
		if !decode(w, r, &req) {
			return
		}
		kind := strings.ToLower(strings.TrimSpace(req.Kind))
		start := time.Now()
		switch kind {
		case "directory", "folder":
			entry, err := h.service.CreateFolder(user, req.Path)
			observeFileOp("create_folder", err, start, 0, "")
			if err != nil {
				writeServiceError(w, err)
				return
			}
			writeJSON(w, 201, entry)
		case "file", "text", "markdown", "csv":
			entry, err := h.service.CreateFile(user, req.Path, req.MIMEType, []byte(req.Content))
			observeFileOp("create_file", err, start, int64(len(req.Content)), "stored")
			if err != nil {
				writeServiceError(w, err)
				return
			}
			writeJSON(w, 201, entry)
		default:
			writeError(w, fmt.Errorf("kind must be directory or file"), 400)
		}
	case http.MethodDelete:
		recursive, _ := strconv.ParseBool(r.URL.Query().Get("recursive"))
		start := time.Now()
		err := h.service.Delete(user, r.URL.Query().Get("id"), recursive)
		observeFileOp("delete", err, start, 0, "")
		if err != nil {
			writeServiceError(w, err)
			return
		}
		writeJSON(w, 200, map[string]bool{"deleted": true})
	default:
		methodNotAllowed(w)
	}
}

func (h *UserHandler) file(w http.ResponseWriter, r *http.Request, user string) {
	switch r.Method {
	case http.MethodGet:
		q := r.URL.Query()
		startLine, _ := strconv.Atoi(q.Get("start"))
		end, _ := strconv.Atoi(q.Get("end"))
		limit, _ := strconv.Atoi(q.Get("limit"))
		started := time.Now()
		result, meta, err := h.service.Read(user, q.Get("id"), ReadOptions{Mode: ReadMode(q.Get("mode")), Start: startLine, End: end, Limit: limit})
		observeFileOp("read", err, started, int64(len(result.Content)), "read")
		if err != nil {
			writeServiceError(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"read": result, "file": meta})
	case http.MethodPut:
		var req struct {
			ID      string `json:"id"`
			Content string `json:"content"`
		}
		if !decode(w, r, &req) {
			return
		}
		started := time.Now()
		meta, err := h.service.Write(user, req.ID, []byte(req.Content))
		observeFileOp("write", err, started, int64(len(req.Content)), "stored")
		if err != nil {
			writeServiceError(w, err)
			return
		}
		writeJSON(w, 200, meta)
	default:
		methodNotAllowed(w)
	}
}

func (h *UserHandler) move(w http.ResponseWriter, r *http.Request, user string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req struct {
		ID          string `json:"id"`
		Destination string `json:"destination"`
	}
	if !decode(w, r, &req) {
		return
	}
	started := time.Now()
	meta, err := h.service.Move(user, req.ID, req.Destination)
	observeFileOp("move", err, started, 0, "")
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, 200, meta)
}

func (h *UserHandler) upload(w http.ResponseWriter, r *http.Request, user string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, h.maxUpload)
	if err := r.ParseMultipartForm(h.maxUpload); err != nil {
		writeError(w, err, 400)
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, err, 400)
		return
	}
	defer file.Close()
	data, err := io.ReadAll(file)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	directory, _ := virtualDir(r.FormValue("path"))
	name := header.Filename
	if directory != "" {
		name = directory + "/" + name
	}
	started := time.Now()
	meta, err := h.service.Upload(user, name, header.Header.Get("Content-Type"), data)
	observeFileOp("upload", err, started, int64(len(data)), "stored")
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, 201, meta)
}

func (h *UserHandler) raw(w http.ResponseWriter, r *http.Request, user string) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	started := time.Now()
	data, meta, err := h.service.backend.ReadUserFileForUser(user, r.URL.Query().Get("id"))
	observeFileOp("raw", err, started, int64(len(data)), "read")
	if err != nil {
		writeServiceError(w, err)
		return
	}
	mimeType := ResolveMIME(meta.Name, meta.MIMEType, data)
	disposition := "inline"
	// Active document types are never rendered in the product origin. Only
	// image/PDF previews may be inline; everything else is attachment-only.
	if r.URL.Query().Get("download") == "1" || (!IsImageMIME(mimeType) && mimeType != "application/pdf") {
		disposition = "attachment"
	}
	safe := strings.NewReplacer("\"", "", "\\", "", "\n", "", "\r", "").Replace(pathBase(meta.Name))
	w.Header().Set("Content-Disposition", fmt.Sprintf("%s; filename=%q", disposition, safe))
	w.Header().Set("Content-Type", mimeType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(200)
	_, _ = w.Write(data)
}

func observeFileOp(op string, err error, start time.Time, n int64, direction string) {
	metrics.FileOp(op, metrics.Status(err), time.Since(start))
	if err == nil && n > 0 && direction != "" {
		metrics.FileBytes(direction, n)
	}
}
func pathBase(p string) string {
	parts := strings.Split(strings.TrimSuffix(p, "/"), "/")
	if len(parts) == 0 {
		return "file"
	}
	return parts[len(parts)-1]
}

func decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	defer r.Body.Close()
	if err := json.NewDecoder(io.LimitReader(r.Body, 3<<20)).Decode(dst); err != nil {
		writeError(w, err, http.StatusBadRequest)
		return false
	}
	return true
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func writeError(w http.ResponseWriter, err error, status int) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}
func methodNotAllowed(w http.ResponseWriter) {
	writeError(w, errors.New("method not allowed"), http.StatusMethodNotAllowed)
}
func writeServiceError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, ErrInvalidPath):
		status = http.StatusBadRequest
	case errors.Is(err, ErrTooLarge):
		status = http.StatusRequestEntityTooLarge
	case strings.Contains(err.Error(), "not found"):
		status = http.StatusNotFound
	case strings.Contains(err.Error(), "already exists"), strings.Contains(err.Error(), "not empty"):
		status = http.StatusConflict
	}
	writeError(w, err, status)
}
