package store

import (
	"bytes"
	"reflect"
	"testing"
)

func openMemSQLite(t *testing.T) Store {
	t.Helper()
	s, err := Open(Config{Backend: "sqlite", SQLitePath: ":memory:"})
	if err != nil {
		t.Fatalf("Open(sqlite :memory:): %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestNewMeteredWrapsAndIsIdempotent(t *testing.T) {
	s := openMemSQLite(t)

	m := NewMetered(s)
	if _, ok := m.(*meteredStore); !ok {
		t.Fatalf("NewMetered should return a *meteredStore, got %T", m)
	}
	if NewMetered(m) != m {
		t.Fatal("NewMetered must not double-wrap an already-metered store")
	}
	if NewMetered(nil) != nil {
		t.Fatal("NewMetered(nil) should return nil")
	}
}

func TestMeteredForwardsBehaviorMaintainerAndUnwrap(t *testing.T) {
	s := openMemSQLite(t)
	m := NewMetered(s)

	// BackendInfo passes through, and drives the backend label.
	if got := m.BackendInfo().Type; got != "SQLite" {
		t.Fatalf("BackendInfo().Type = %q, want SQLite", got)
	}
	if ms := m.(*meteredStore); ms.backend != "sqlite" {
		t.Fatalf("backend label = %q, want sqlite", ms.backend)
	}

	// Behaves identically to the wrapped store: a missing get is (nil, nil).
	if f, err := m.GetUserFile("does-not-exist"); err != nil || f != nil {
		t.Fatalf("GetUserFile(missing) = (%v, %v), want (nil, nil)", f, err)
	}

	// Maintainer methods are forwarded (assert via the Store-typed value).
	mt, ok := m.(Maintainer)
	if !ok {
		t.Fatal("metered store should implement store.Maintainer")
	}
	if _, err := mt.Verify(); err != nil {
		t.Fatalf("forwarded Verify on a fresh db should succeed: %v", err)
	}
	var buf bytes.Buffer
	if err := mt.Backup(&buf); err != nil {
		t.Fatalf("forwarded Backup should succeed: %v", err)
	}

	// Unwrap exposes the underlying store for concrete-type access.
	u, ok := m.(interface{ Unwrap() Store })
	if !ok || u.Unwrap() != s {
		t.Fatal("Unwrap() should return the wrapped store")
	}
}

func TestMeteredOperationalHealthTracksOperationsThatHaveNotReturned(t *testing.T) {
	s := openMemSQLite(t)
	m := NewMetered(s).(*meteredStore)

	first := m.begin("GetAllMessages")
	second := m.begin("GetAllMessages")
	third := m.begin("PutConversation")
	health := m.OperationalHealth()
	if got := health["in_flight"]; got != 3 {
		t.Fatalf("in_flight = %v, want 3", got)
	}
	operations, ok := health["operations"].([]map[string]any)
	if !ok {
		t.Fatalf("operations type = %T", health["operations"])
	}
	want := []string{"GetAllMessages", "PutConversation"}
	got := make([]string, 0, len(operations))
	for _, operation := range operations {
		got = append(got, operation["operation"].(string))
		if operation["oldest_seconds"].(float64) < 0 {
			t.Fatalf("negative oldest age: %#v", operation)
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("operations = %v, want %v", got, want)
	}

	m.observe(first)
	m.observe(second)
	m.observe(third)
	if got := m.OperationalHealth()["in_flight"]; got != 0 {
		t.Fatalf("in_flight after completion = %v, want 0", got)
	}
}
