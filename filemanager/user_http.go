package filemanager

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

type UserHandler struct {
	service   *UserService
	owner     OwnerResolver
	maxUpload int64
}

type createRequest struct {
	Path string `json:"path"`
	Kind string `json:"kind"`
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
		items, err := h.service.List(user, r.URL.Query().Get("path"))
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
		if req.Kind != "directory" {
			writeError(w, fmt.Errorf("use upload to create files"), 400)
			return
		}
		entry, err := h.service.CreateFolder(user, req.Path)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		writeJSON(w, 201, entry)
	case http.MethodDelete:
		recursive, _ := strconv.ParseBool(r.URL.Query().Get("recursive"))
		if err := h.service.Delete(user, r.URL.Query().Get("id"), recursive); err != nil {
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
		start, _ := strconv.Atoi(q.Get("start"))
		end, _ := strconv.Atoi(q.Get("end"))
		limit, _ := strconv.Atoi(q.Get("limit"))
		result, meta, err := h.service.Read(user, q.Get("id"), ReadOptions{Mode: ReadMode(q.Get("mode")), Start: start, End: end, Limit: limit})
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
		meta, err := h.service.Write(user, req.ID, []byte(req.Content))
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
	meta, err := h.service.Move(user, req.ID, req.Destination)
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
	meta, err := h.service.Upload(user, name, header.Header.Get("Content-Type"), data)
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
	data, meta, err := h.service.backend.ReadUserFileForUser(user, r.URL.Query().Get("id"))
	if err != nil {
		writeServiceError(w, err)
		return
	}
	mimeType := meta.MIMEType
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	disposition := "inline"
	// Active document types are never rendered in the product origin. Only
	// image/PDF previews may be inline; everything else is attachment-only.
	if r.URL.Query().Get("download") == "1" || (!strings.HasPrefix(mimeType, "image/") && mimeType != "application/pdf") {
		disposition = "attachment"
	}
	safe := strings.NewReplacer("\"", "", "\\", "", "\n", "", "\r", "").Replace(pathBase(meta.Name))
	w.Header().Set("Content-Disposition", fmt.Sprintf("%s; filename=%q", disposition, safe))
	w.Header().Set("Content-Type", mimeType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(200)
	_, _ = w.Write(data)
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
