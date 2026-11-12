package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ghiac/agentize/mcpauth"
	"github.com/ghiac/agentize/toolruntime"
)

type credentialStore struct {
	record mcpauth.Record
	hash   string
}

func (s *credentialStore) Insert(_ context.Context, r mcpauth.Record, h string) error {
	s.record = r
	s.hash = h
	return nil
}
func (s *credentialStore) List(context.Context, string) ([]mcpauth.Record, error) {
	return []mcpauth.Record{s.record}, nil
}
func (s *credentialStore) Revoke(context.Context, string, string, time.Time) (bool, error) {
	now := time.Now()
	s.record.RevokedAt = &now
	return true, nil
}
func (s *credentialStore) FindByHash(_ context.Context, h string) (mcpauth.Record, bool, error) {
	return s.record, h == s.hash, nil
}
func (s *credentialStore) CountActive(context.Context, string, time.Time) (int, error) { return 0, nil }
func (s *credentialStore) Touch(context.Context, string, time.Time) error              { return nil }

type directory struct{}

func (directory) Banned(context.Context, string) (bool, error) { return false, nil }

type ledger struct {
	broken                    bool
	admitted, finished, calls int
	usage                     []mcpauth.Usage
}

func (l *ledger) Begin(context.Context, toolruntime.Admission) (toolruntime.Receipt, error) {
	if l.broken {
		return nil, errors.New("private database error")
	}
	l.admitted++
	return l, nil
}
func (l *ledger) Finish(context.Context, string) error { l.finished++; return nil }
func (l *ledger) RecordUsage(_ context.Context, u mcpauth.Usage) error {
	l.usage = append(l.usage, u)
	return nil
}
func (l *ledger) UsageSummary(context.Context, string, mcpauth.UsageQuery) (mcpauth.UsageSummary, error) {
	return mcpauth.UsageSummary{}, nil
}

type provider struct{ ledger *ledger }

func (p provider) List(_ context.Context, principal mcpauth.Principal) ([]Tool, error) {
	if principal.UserID != "alice" {
		return []Tool{}, nil
	}
	return []Tool{{Name: "echo", InputSchema: json.RawMessage(`{"type":"object"}`)}}, nil
}
func (p provider) Call(_ context.Context, principal mcpauth.Principal, name string, args json.RawMessage) (any, error) {
	p.ledger.calls++
	return map[string]any{"secret_text": "user content"}, nil
}
func TestAuthenticatedStatelessTransportAndAdmission(t *testing.T) {
	store := &credentialStore{}
	access, err := mcpauth.New(store, directory{})
	if err != nil {
		t.Fatal(err)
	}
	issued, err := access.Issue(t.Context(), "alice", "test", 1)
	if err != nil {
		t.Fatal(err)
	}
	ledger := &ledger{}
	server, _ := NewProvider(provider{ledger})
	handler := Handler(access, server, ledger)
	request := func(body, token, origin string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Accept", "application/json, text/event-stream")
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo","arguments":{}}}`
	if r := request(body, "", ""); r.Code != 401 || r.Header().Get("WWW-Authenticate") == "" {
		t.Fatal("missing credential accepted")
	}
	if r := request(body, issued.Token, "https://untrusted.invalid"); r.Code != 403 {
		t.Fatal("browser origin accepted")
	}
	ledger.broken = true
	if r := request(body, issued.Token, ""); !strings.Contains(r.Body.String(), "audit is unavailable") {
		t.Fatal("admission failure hidden")
	}
	if ledger.calls != 0 {
		t.Fatal("executed before durable admission")
	}
	ledger.broken = false
	if r := request(body, issued.Token, ""); !strings.Contains(r.Body.String(), "structuredContent") {
		t.Fatal(r.Body.String())
	}
	if ledger.calls != 1 || ledger.admitted != 1 || ledger.finished != 1 {
		t.Fatal("audit sequence failed")
	}
	if ledger.usage[0].Detail != "" {
		t.Fatal("user content copied to audit")
	}
	if err := access.Revoke(t.Context(), "alice", issued.Record.ID); err != nil {
		t.Fatal(err)
	}
	if r := request(body, issued.Token, ""); r.Code != 401 {
		t.Fatal("revoked token accepted")
	}
}
func TestCompositeRejectsDuplicateNames(t *testing.T) {
	p := provider{&ledger{}}
	if _, err := Combine(p, p).List(t.Context(), mcpauth.Principal{UserID: "alice"}); err == nil {
		t.Fatal("duplicate handler replaced policy")
	}
}
