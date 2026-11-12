// Package mcpauth issues and checks private application credentials.
// It is not an OAuth server. A raw token is returned once and only its hash is stored.
package mcpauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/oklog/ulid/v2"
)

const (
	maxActiveCredentials = 5
	defaultTTLDays       = 90
	maxTTLDays           = 365
)

var (
	ErrUnauthenticated = errors.New("mcpauth: unauthenticated")
	ErrForbidden       = errors.New("mcpauth: forbidden")
	ErrUnavailable     = errors.New("mcpauth: unavailable")
	ErrInvalid         = errors.New("mcpauth: invalid")
	ErrLimit           = errors.New("mcpauth: limit")
	ErrUnknownUser     = errors.New("mcpauth: unknown user")
)

// GrantedScopes is the only access a signed-in user can place on a private token.
// Inference and trading scopes are not included.
var GrantedScopes = []string{"catalog:read", "research:read", "research:write", "files:write"}

// Record is stored credential metadata. It never contains the raw token.
type Record struct {
	ID         string
	Owner      string
	Name       string
	Scopes     []string
	ExpiresAt  time.Time
	RevokedAt  *time.Time
	CreatedAt  time.Time
	LastUsedAt *time.Time
}

// Principal is the authenticated MCP caller.
type Principal struct {
	UserID       string
	CredentialID string
	Scopes       []string
}

// Store is the durable credential table. A nil store is not a fallback.
type Store interface {
	Insert(context.Context, Record, string) error
	List(context.Context, string) ([]Record, error)
	Revoke(context.Context, string, string, time.Time) (bool, error)
	FindByHash(context.Context, string) (Record, bool, error)
	CountActive(context.Context, string, time.Time) (int, error)
	Touch(context.Context, string, time.Time) error
}

// Usage is one authenticated MCP request. InferenceUnits is the internal model
// count, not a guessed price. Research calls record zero.
type Usage struct {
	UserID         string
	CredentialID   string
	Tool           string
	Outcome        string
	JobID          string
	Detail         string
	DurationMS     int64
	InferenceUnits int64
}

type UsageEvent struct {
	CredentialID   string    `json:"credential_id"`
	Tool           string    `json:"tool"`
	Outcome        string    `json:"outcome"`
	JobID          string    `json:"job_id,omitempty"`
	Detail         string    `json:"detail,omitempty"`
	DurationMS     int64     `json:"duration_ms"`
	InferenceUnits int64     `json:"inference_units"`
	CreatedAt      time.Time `json:"created_at"`
}

type ToolUsage struct {
	CredentialID   string `json:"credential_id"`
	Tool           string `json:"tool"`
	Calls          int    `json:"calls"`
	Errors         int    `json:"errors"`
	DurationMS     int64  `json:"duration_ms"`
	InferenceUnits int64  `json:"inference_units"`
}

// UsageQuery pages one owner's request log. Outcome is empty, ok, tool_error, or error.
type UsageQuery struct {
	Since   time.Time
	Outcome string
	Limit   int
	Offset  int
}

type StatusCount struct {
	Outcome string `json:"outcome"`
	Calls   int    `json:"calls"`
}

type UsageSummary struct {
	Calls          int           `json:"calls"`
	Errors         int           `json:"errors"`
	DurationMS     int64         `json:"duration_ms"`
	InferenceUnits int64         `json:"inference_units"`
	Tools          []ToolUsage   `json:"tools"`
	Statuses       []StatusCount `json:"statuses"`
	Events         []UsageEvent  `json:"events"`
	EventTotal     int           `json:"event_total"`
	Offset         int           `json:"offset"`
	Limit          int           `json:"limit"`
	Outcome        string        `json:"outcome,omitempty"`
}

// UsageStore is the durable per-owner measurement. It is optional for isolated
// credential tests and required in production.
type UsageStore interface {
	RecordUsage(context.Context, Usage) error
	UsageSummary(context.Context, string, UsageQuery) (UsageSummary, error)
}

// Directory reports whether a product user is banned. Lookup failure is not "not banned".
type Directory interface {
	Banned(context.Context, string) (bool, error)
}

type Service struct {
	store  Store
	users  Directory
	now    func() time.Time
	scopes []string
}

func New(store Store, users Directory) (*Service, error) {
	if store == nil || users == nil {
		return nil, fmt.Errorf("mcpauth: credential store and user directory are required")
	}
	return NewWithScopes(store, users, GrantedScopes)
}

// NewWithScopes chooses the maximum token scopes for a host at startup.
// Scope grants do not replace the provider's per-user authorization.
func NewWithScopes(store Store, users Directory, scopes []string) (*Service, error) {
	if store == nil || users == nil || len(scopes) == 0 {
		return nil, ErrInvalid
	}
	out := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		if strings.TrimSpace(scope) == "" {
			return nil, ErrInvalid
		}
		out = append(out, scope)
	}
	return &Service{store: store, users: users, now: time.Now, scopes: out}, nil
}
func HasScope(p Principal, scope string) bool {
	for _, granted := range p.Scopes {
		if granted == scope {
			return true
		}
	}
	return false
}

type Issued struct {
	Record Record
	Token  string
}

func (s *Service) Issue(ctx context.Context, owner, name string, ttlDays int) (Issued, error) {
	return s.IssueWithScopes(ctx, owner, name, ttlDays, nil)
}

// IssueWithScopes narrows the host's allowed scopes. A nil list uses its configured grant.
func (s *Service) IssueWithScopes(ctx context.Context, owner, name string, ttlDays int, requested []string) (Issued, error) {
	scopes := append([]string(nil), s.scopes...)
	if requested != nil {
		if len(requested) == 0 {
			return Issued{}, ErrInvalid
		}
		scopes = []string{}
		seen := map[string]bool{}
		for _, scope := range requested {
			if !HasScope(Principal{Scopes: s.scopes}, scope) || seen[scope] {
				return Issued{}, ErrInvalid
			}
			seen[scope] = true
			scopes = append(scopes, scope)
		}
	}

	owner = strings.TrimSpace(owner)
	if owner == "" {
		return Issued{}, ErrUnauthenticated
	}
	name, err := cleanName(name)
	if err != nil {
		return Issued{}, err
	}
	if ttlDays == 0 {
		ttlDays = defaultTTLDays
	}
	if ttlDays < 1 || ttlDays > maxTTLDays {
		return Issued{}, fmt.Errorf("%w: expiry must be 1 to %d days", ErrInvalid, maxTTLDays)
	}
	if err := s.userAllowed(ctx, owner); err != nil {
		return Issued{}, err
	}
	active, err := s.store.CountActive(ctx, owner, s.now())
	if err != nil {
		return Issued{}, ErrUnavailable
	}
	if active >= maxActiveCredentials {
		return Issued{}, fmt.Errorf("%w: at most %d active credentials", ErrLimit, maxActiveCredentials)
	}
	raw, err := randomToken()
	if err != nil {
		return Issued{}, ErrUnavailable
	}
	now := s.now().UTC()
	rec := Record{
		ID:        ulid.MustNew(ulid.Timestamp(now), rand.Reader).String(),
		Owner:     owner,
		Name:      name,
		Scopes:    scopes,
		ExpiresAt: now.Add(time.Duration(ttlDays) * 24 * time.Hour),
		CreatedAt: now,
	}
	if err := s.store.Insert(ctx, rec, hashToken(raw)); err != nil {
		if errors.Is(err, ErrLimit) {
			return Issued{}, ErrLimit
		}
		return Issued{}, ErrUnavailable
	}
	return Issued{Record: rec, Token: raw}, nil
}

func (s *Service) List(ctx context.Context, owner string) ([]Record, error) {
	owner = strings.TrimSpace(owner)
	if owner == "" {
		return nil, ErrUnauthenticated
	}
	if err := s.userAllowed(ctx, owner); err != nil {
		return nil, err
	}
	rows, err := s.store.List(ctx, owner)
	if err != nil {
		return nil, ErrUnavailable
	}
	return rows, nil
}

func (s *Service) Revoke(ctx context.Context, owner, id string) error {
	owner = strings.TrimSpace(owner)
	id = strings.TrimSpace(id)
	if owner == "" {
		return ErrUnauthenticated
	}
	if id == "" {
		return fmt.Errorf("%w: credential id is required", ErrInvalid)
	}
	if err := s.userAllowed(ctx, owner); err != nil {
		return err
	}
	ok, err := s.store.Revoke(ctx, owner, id, s.now().UTC())
	if err != nil {
		return ErrUnavailable
	}
	if !ok {
		return ErrForbidden
	}
	return nil
}

func (s *Service) Authenticate(ctx context.Context, token string) (Principal, error) {
	token = strings.TrimSpace(token)
	if !strings.HasPrefix(token, "mcp_") || len(token) < 20 {
		return Principal{}, ErrUnauthenticated
	}
	rec, ok, err := s.store.FindByHash(ctx, hashToken(token))
	if err != nil {
		return Principal{}, ErrUnavailable
	}
	now := s.now().UTC()
	if !ok || rec.RevokedAt != nil || !rec.ExpiresAt.After(now) || strings.TrimSpace(rec.Owner) == "" {
		return Principal{}, ErrUnauthenticated
	}
	if err := s.userAllowed(ctx, rec.Owner); err != nil {
		if errors.Is(err, ErrForbidden) || errors.Is(err, ErrUnknownUser) {
			return Principal{}, ErrForbidden
		}
		return Principal{}, err
	}
	if err := s.store.Touch(ctx, rec.ID, now); err != nil {
		return Principal{}, ErrUnavailable
	}
	return Principal{UserID: rec.Owner, CredentialID: rec.ID, Scopes: append([]string(nil), rec.Scopes...)}, nil
}

// Usage returns the signed-in owner's measured MCP requests. Another owner's
// rows are not included. A store without measurement returns ErrUnavailable
// rather than a fake zero.
func (s *Service) Usage(ctx context.Context, owner string, query UsageQuery) (UsageSummary, error) {
	owner = strings.TrimSpace(owner)
	if owner == "" {
		return UsageSummary{}, ErrUnauthenticated
	}
	usage, ok := s.store.(UsageStore)
	if !ok || usage == nil {
		return UsageSummary{}, ErrUnavailable
	}
	if err := s.userAllowed(ctx, owner); err != nil {
		return UsageSummary{}, err
	}
	switch query.Outcome {
	case "", "ok", "tool_error", "error":
	default:
		return UsageSummary{}, fmt.Errorf("%w: status must be ok, tool_error, or error", ErrInvalid)
	}
	if query.Limit <= 0 {
		query.Limit = 20
	}
	if query.Limit > 50 {
		query.Limit = 50
	}
	if query.Offset < 0 {
		query.Offset = 0
	}
	if query.Offset > 10000 {
		query.Offset = 10000
	}
	query.Since = s.now().UTC().Add(-30 * 24 * time.Hour)
	return usage.UsageSummary(ctx, owner, query)
}

func (s *Service) userAllowed(ctx context.Context, owner string) error {
	banned, err := s.users.Banned(ctx, owner)
	if errors.Is(err, ErrUnknownUser) {
		return ErrForbidden
	}
	if err != nil {
		return ErrUnavailable
	}
	if banned {
		return ErrForbidden
	}
	return nil
}

func cleanName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 40 {
		return "", fmt.Errorf("%w: name must be 1 to 40 characters", ErrInvalid)
	}
	for _, r := range name {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == ' ' || r == '-' || r == '_' {
			continue
		}
		return "", fmt.Errorf("%w: name has unsupported characters", ErrInvalid)
	}
	return name, nil
}

func randomToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return "mcp_" + base64.RawURLEncoding.EncodeToString(buf), nil
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
