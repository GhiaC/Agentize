package agentize

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ghiac/agentize/debuger"
	"github.com/gin-gonic/gin"
)

func TestDeprecatedSessionURLIsNotAPrimaryKey(t *testing.T) {
	t.Setenv("AGENTIZE_DEBUG_UNSAFE", "1")
	gin.SetMode(gin.TestMode)
	ag := &Agentize{}
	router := gin.New()
	ag.RegisterRoutes(router)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/agentize/debug/sessions/2", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unscoped session URL status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/agentize/debug/sessions/2?user=alice", nil))
	if rec.Code != http.StatusMovedPermanently {
		t.Fatalf("owner-scoped leftover URL status = %d, want 301; body=%s", rec.Code, rec.Body.String())
	}
	want := debuger.SessionPath("alice", "2")
	if rec.Header().Get("Location") != want {
		t.Fatalf("redirect = %q, want %q", rec.Header().Get("Location"), want)
	}
}
