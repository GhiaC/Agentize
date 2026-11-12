package mcpauth

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

// SessionOwner is a host's existing authenticated and CSRF-protected session
// resolver. It must never accept an owner from query parameters or the body.
type SessionOwner func(*http.Request) (string, error)

// ManagementHandler provides reusable credential issuance, listing, revocation
// and usage on a host-mounted route. It does not implement a second login system.
// For writes the host resolver MUST validate CSRF as well as the login session.
func ManagementHandler(service *Service, owner SessionOwner) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json")
		fail := func(status int) {
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": http.StatusText(status)})
		}
		if service == nil || owner == nil {
			fail(http.StatusServiceUnavailable)
			return
		}
		user, err := owner(r)
		if err != nil || user == "" {
			fail(http.StatusUnauthorized)
			return
		}
		var result any
		switch r.Method {
		case http.MethodGet:
			if r.URL.Query().Get("view") == "usage" {
				result, err = service.Usage(r.Context(), user, UsageQuery{})
			} else {
				result, err = service.List(r.Context(), user)
			}
		case http.MethodPost:
			var body struct {
				Name    string   `json:"name"`
				TTLDays int      `json:"ttl_days"`
				Scopes  []string `json:"scopes"`
			}
			decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
			decoder.DisallowUnknownFields()
			if decoder.Decode(&body) != nil || decoder.Decode(new(any)) != io.EOF {
				fail(http.StatusBadRequest)
				return
			}
			var issued Issued
			issued, err = service.IssueWithScopes(r.Context(), user, body.Name, body.TTLDays, body.Scopes)
			result = map[string]any{"credential": issued.Record, "token": issued.Token}
		case http.MethodDelete:
			err = service.Revoke(r.Context(), user, r.URL.Query().Get("id"))
			result = map[string]bool{"revoked": err == nil}
		default:
			w.Header().Set("Allow", "GET, POST, DELETE")
			fail(http.StatusMethodNotAllowed)
			return
		}
		if err != nil {
			switch {
			case errors.Is(err, ErrUnauthenticated):
				fail(http.StatusUnauthorized)
			case errors.Is(err, ErrForbidden):
				fail(http.StatusForbidden)
			case errors.Is(err, ErrInvalid):
				fail(http.StatusBadRequest)
			case errors.Is(err, ErrLimit):
				fail(http.StatusConflict)
			default:
				fail(http.StatusServiceUnavailable)
			}
			return
		}
		_ = json.NewEncoder(w).Encode(result)
	})
}
