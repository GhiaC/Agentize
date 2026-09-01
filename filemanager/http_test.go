package filemanager

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandlerContract(t *testing.T) {
	s, err := New(Config{Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	h := NewHandler(s)
	request := func(method, target, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, target, strings.NewReader(body))
		if body != "" {
			r.Header.Set("Content-Type", "application/json")
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := request(http.MethodPost, "/api/entries", `{"path":"docs","kind":"directory"}`); w.Code != http.StatusCreated {
		t.Fatalf("mkdir: %d %s", w.Code, w.Body.String())
	}
	if w := request(http.MethodPost, "/api/entries", `{"path":"docs/a.txt","kind":"file","content":"a\nb\nc"}`); w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	if w := request(http.MethodGet, "/api/file?path=docs/a.txt&mode=tail&limit=2", ""); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"content":"b\nc"`) {
		t.Fatalf("tail: %d %s", w.Code, w.Body.String())
	}
	if w := request(http.MethodDelete, "/api/entries?path=docs&recursive=true", ""); w.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", w.Code, w.Body.String())
	}
}
