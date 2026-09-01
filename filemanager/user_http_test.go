package filemanager

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestUserHandlerCreatesTextFilesMovesIntoFoldersAndServesImages(t *testing.T) {
	s := NewUserService(newMemoryUserFiles())
	h := NewUserHandler(s, func(*http.Request) string { return "alice" })

	create := httptest.NewRecorder()
	h.ServeHTTP(create, httptest.NewRequest("POST", "/entries", strings.NewReader(`{"path":"docs","kind":"directory"}`)))
	if create.Code != 201 {
		t.Fatalf("create folder status=%d body=%s", create.Code, create.Body.String())
	}

	note := httptest.NewRecorder()
	h.ServeHTTP(note, httptest.NewRequest("POST", "/entries", strings.NewReader(`{"path":"hello.md","kind":"file","content":"# Hi"}`)))
	if note.Code != 201 {
		t.Fatalf("create file status=%d body=%s", note.Code, note.Body.String())
	}
	var created map[string]any
	if err := json.Unmarshal(note.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	fileID, _ := created["FileID"].(string)
	if fileID == "" {
		t.Fatalf("create file response=%s", note.Body.String())
	}
	mimeType, _ := created["MIMEType"].(string)
	if !strings.Contains(mimeType, "markdown") {
		t.Fatalf("created mime=%q", mimeType)
	}

	moved := httptest.NewRecorder()
	h.ServeHTTP(moved, httptest.NewRequest("POST", "/move", strings.NewReader(`{"id":"`+fileID+`","destination":"docs"}`)))
	if moved.Code != 200 {
		t.Fatalf("move status=%d body=%s", moved.Code, moved.Body.String())
	}
	var movedFile map[string]any
	if err := json.Unmarshal(moved.Body.Bytes(), &movedFile); err != nil {
		t.Fatal(err)
	}
	if movedFile["Name"] != "docs/hello.md" {
		t.Fatalf("moved name=%v body=%s", movedFile["Name"], moved.Body.String())
	}

	listed := httptest.NewRecorder()
	h.ServeHTTP(listed, httptest.NewRequest("GET", "/entries?path=docs", nil))
	if listed.Code != 200 || !strings.Contains(listed.Body.String(), "hello.md") {
		t.Fatalf("list body=%s", listed.Body.String())
	}

	png := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0, 0, 0, 0}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	_ = writer.WriteField("path", "")
	part, err := writer.CreateFormFile("file", "shot.png")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = part.Write(png); err != nil {
		t.Fatal(err)
	}
	_ = writer.Close()
	uploadReq := httptest.NewRequest("POST", "/upload", &body)
	uploadReq.Header.Set("Content-Type", writer.FormDataContentType())
	uploaded := httptest.NewRecorder()
	h.ServeHTTP(uploaded, uploadReq)
	if uploaded.Code != 201 {
		t.Fatalf("upload status=%d body=%s", uploaded.Code, uploaded.Body.String())
	}
	var image map[string]any
	if err := json.Unmarshal(uploaded.Body.Bytes(), &image); err != nil {
		t.Fatal(err)
	}
	imageID, _ := image["FileID"].(string)
	if image["MIMEType"] != "image/png" {
		t.Fatalf("uploaded mime=%v body=%s", image["MIMEType"], uploaded.Body.String())
	}

	raw := httptest.NewRecorder()
	h.ServeHTTP(raw, httptest.NewRequest("GET", "/raw?id="+imageID, nil))
	if raw.Code != 200 {
		t.Fatalf("raw status=%d", raw.Code)
	}
	if ct := raw.Header().Get("Content-Type"); !strings.HasPrefix(ct, "image/png") {
		t.Fatalf("raw content-type=%q", ct)
	}
	if !strings.Contains(raw.Header().Get("Content-Disposition"), "inline") {
		t.Fatalf("raw disposition=%q", raw.Header().Get("Content-Disposition"))
	}
	got, _ := io.ReadAll(raw.Body)
	if !bytes.Equal(got, png) {
		t.Fatalf("raw bytes mismatch")
	}
}

func TestUserHandlerRejectsAnonymousRequests(t *testing.T) {
	h := NewUserHandler(NewUserService(newMemoryUserFiles()), nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/entries", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d", w.Code)
	}
}
