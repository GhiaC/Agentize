package mcpauth

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type memStore struct {
	rows []stored
	fail bool
}

type stored struct {
	rec  Record
	hash string
}

func (m *memStore) Insert(_ context.Context, rec Record, hash string) error {
	if m.fail {
		return errors.New("down")
	}
	m.rows = append(m.rows, stored{rec: rec, hash: hash})
	return nil
}
func (m *memStore) List(_ context.Context, owner string) ([]Record, error) {
	if m.fail {
		return nil, errors.New("down")
	}
	var out []Record
	for _, row := range m.rows {
		if row.rec.Owner == owner {
			out = append(out, row.rec)
		}
	}
	return out, nil
}
func (m *memStore) Revoke(_ context.Context, owner, id string, at time.Time) (bool, error) {
	if m.fail {
		return false, errors.New("down")
	}
	for i := range m.rows {
		if m.rows[i].rec.ID == id && m.rows[i].rec.Owner == owner {
			if m.rows[i].rec.RevokedAt == nil {
				m.rows[i].rec.RevokedAt = &at
			}
			return true, nil
		}
	}
	return false, nil
}
func (m *memStore) FindByHash(_ context.Context, hash string) (Record, bool, error) {
	if m.fail {
		return Record{}, false, errors.New("down")
	}
	for _, row := range m.rows {
		if row.hash == hash {
			return row.rec, true, nil
		}
	}
	return Record{}, false, nil
}
func (m *memStore) CountActive(_ context.Context, owner string, now time.Time) (int, error) {
	if m.fail {
		return 0, errors.New("down")
	}
	n := 0
	for _, row := range m.rows {
		if row.rec.Owner == owner && row.rec.RevokedAt == nil && row.rec.ExpiresAt.After(now) {
			n++
		}
	}
	return n, nil
}
func (m *memStore) Touch(_ context.Context, id string, at time.Time) error {
	if m.fail {
		return errors.New("down")
	}
	for i := range m.rows {
		if m.rows[i].rec.ID == id {
			m.rows[i].rec.LastUsedAt = &at
			return nil
		}
	}
	return nil
}

type dir struct {
	banned map[string]bool
	fail   bool
}

func (d dir) Banned(_ context.Context, id string) (bool, error) {
	if d.fail {
		return false, errors.New("down")
	}
	banned, ok := d.banned[id]
	if !ok {
		return false, ErrUnknownUser
	}
	return banned, nil
}

func TestIssueListRevokeAndAuthenticate(t *testing.T) {
	store := &memStore{}
	svc, err := New(store, dir{banned: map[string]bool{"user-a": false, "user-b": false}})
	if err != nil {
		t.Fatal(err)
	}
	issued, err := svc.Issue(context.Background(), "user-a", "Laptop", 30)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(issued.Token, "mcp_") || strings.Contains(issued.Record.Name, issued.Token) {
		t.Fatal("token was not a one-time secret")
	}
	listed, err := svc.List(context.Background(), "user-a")
	if err != nil || len(listed) != 1 || listed[0].ID != issued.Record.ID {
		t.Fatal(err, listed)
	}
	for _, row := range store.rows {
		if strings.Contains(row.hash, issued.Token) || row.hash == issued.Token {
			t.Fatal("raw token stored")
		}
	}
	principal, err := svc.Authenticate(context.Background(), issued.Token)
	if err != nil || principal.UserID != "user-a" || principal.CredentialID != issued.Record.ID {
		t.Fatalf("principal=%+v err=%v", principal, err)
	}
	if _, err := svc.Authenticate(context.Background(), "mcp_forged"); !errors.Is(err, ErrUnauthenticated) {
		t.Fatal(err)
	}
	if err := svc.Revoke(context.Background(), "user-b", issued.Record.ID); !errors.Is(err, ErrForbidden) {
		t.Fatal(err)
	}
	if err := svc.Revoke(context.Background(), "user-a", issued.Record.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Authenticate(context.Background(), issued.Token); !errors.Is(err, ErrUnauthenticated) {
		t.Fatal(err)
	}
}

func TestBannedExpiredAndStoreFailure(t *testing.T) {
	store := &memStore{}
	users := dir{banned: map[string]bool{"user-a": false}}
	svc, err := New(store, users)
	if err != nil {
		t.Fatal(err)
	}
	issued, err := svc.Issue(context.Background(), "user-a", "Phone", 1)
	if err != nil {
		t.Fatal(err)
	}
	users.banned["user-a"] = true
	if _, err := svc.Authenticate(context.Background(), issued.Token); !errors.Is(err, ErrForbidden) {
		t.Fatal(err)
	}
	if _, err := svc.Issue(context.Background(), "user-a", "Again", 1); !errors.Is(err, ErrForbidden) {
		t.Fatal(err)
	}
	users.banned["user-a"] = false
	svc.now = func() time.Time { return time.Now().Add(48 * time.Hour) }
	if _, err := svc.Authenticate(context.Background(), issued.Token); !errors.Is(err, ErrUnauthenticated) {
		t.Fatal(err)
	}
	store.fail = true
	if _, err := svc.Issue(context.Background(), "user-a", "Later", 1); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	if _, err := New(nil, users); err == nil {
		t.Fatal("nil store accepted")
	}
}

func TestExplicitScopesCannotEscalate(t *testing.T) {
	store := &memStore{}
	configured := []string{"tools:execute", "conversations:read"}
	access, err := NewWithScopes(store, dir{banned: map[string]bool{"alice": false}}, configured)
	if err != nil {
		t.Fatal(err)
	}
	configured[0] = "inference:execute"
	for _, scopes := range [][]string{{"inference:execute"}, {"tools:execute", "tools:execute"}, {}} {
		if _, err := access.IssueWithScopes(t.Context(), "alice", "test", 1, scopes); !errors.Is(err, ErrInvalid) {
			t.Fatal("scope escalation or ambiguous grant accepted")
		}
	}
	issued, err := access.IssueWithScopes(t.Context(), "alice", "test", 1, []string{"conversations:read"})
	if err != nil {
		t.Fatal(err)
	}
	principal, err := access.Authenticate(t.Context(), issued.Token)
	if err != nil || len(principal.Scopes) != 1 || principal.Scopes[0] != "conversations:read" {
		t.Fatal("narrowed scope not preserved")
	}
}
