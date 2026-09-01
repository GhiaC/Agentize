package filemanager

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
)

type Handler struct{ service *Service }

func NewHandler(service *Service) *Handler { return &Handler{service: service} }

type writeRequest struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}
type createRequest struct {
	Path    string `json:"path"`
	Kind    string `json:"kind"`
	Content string `json:"content"`
}
type moveRequest struct {
	Source      string `json:"source"`
	Destination string `json:"destination"`
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if h == nil || h.service == nil {
		writeError(w, errors.New("file manager unavailable"), http.StatusServiceUnavailable)
		return
	}
	endpoint := strings.TrimSuffix(r.URL.Path, "/")
	endpoint = endpoint[strings.LastIndex(endpoint, "/")+1:]
	switch endpoint {
	case "entries":
		h.entries(w, r)
	case "file":
		h.file(w, r)
	case "move":
		h.move(w, r)
	default:
		writeError(w, os.ErrNotExist, http.StatusNotFound)
	}
}

func (h *Handler) entries(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		entries, err := h.service.List(r.URL.Query().Get("path"))
		if err != nil {
			writeServiceError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"path": r.URL.Query().Get("path"), "entries": entries})
	case http.MethodPost:
		var req createRequest
		if !decode(w, r, &req) {
			return
		}
		var err error
		switch req.Kind {
		case "directory":
			err = h.service.Mkdir(req.Path)
		case "file", "":
			err = h.service.Write(req.Path, []byte(req.Content), true)
		default:
			writeError(w, errors.New("kind must be file or directory"), http.StatusBadRequest)
			return
		}
		if err != nil {
			writeServiceError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"path": req.Path})
	case http.MethodDelete:
		recursive, _ := strconv.ParseBool(r.URL.Query().Get("recursive"))
		if err := h.service.Delete(r.URL.Query().Get("path"), recursive); err != nil {
			writeServiceError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
	default:
		methodNotAllowed(w)
	}
}

func (h *Handler) file(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		q := r.URL.Query()
		start, _ := strconv.Atoi(q.Get("start"))
		end, _ := strconv.Atoi(q.Get("end"))
		limit, _ := strconv.Atoi(q.Get("limit"))
		result, err := h.service.Read(q.Get("path"), ReadOptions{Mode: ReadMode(q.Get("mode")), Start: start, End: end, Limit: limit})
		if err != nil {
			writeServiceError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, result)
	case http.MethodPut:
		var req writeRequest
		if !decode(w, r, &req) {
			return
		}
		if err := h.service.Write(req.Path, []byte(req.Content), false); err != nil {
			writeServiceError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"path": req.Path})
	default:
		methodNotAllowed(w)
	}
}

func (h *Handler) move(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req moveRequest
	if !decode(w, r, &req) {
		return
	}
	if err := h.service.Move(req.Source, req.Destination); err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": req.Destination})
}

func decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	defer r.Body.Close()
	err := json.NewDecoder(io.LimitReader(r.Body, 3<<20)).Decode(dst)
	if err != nil {
		writeError(w, err, http.StatusBadRequest)
		return false
	}
	return true
}
func writeJSON(w http.ResponseWriter, status int, value any) {
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
	case errors.Is(err, ErrInvalidPath), errors.Is(err, ErrRootMutation):
		status = http.StatusBadRequest
	case errors.Is(err, ErrTooLarge):
		status = http.StatusRequestEntityTooLarge
	case errors.Is(err, os.ErrNotExist):
		status = http.StatusNotFound
	case errors.Is(err, os.ErrExist):
		status = http.StatusConflict
	case errors.Is(err, os.ErrPermission):
		status = http.StatusForbidden
	}
	writeError(w, err, status)
}
