# Store Package

This package provides storage for Agentize. Every backend implements one
unified interface, **`store.Store`**, and is guaranteed — at compile time and by
a shared conformance test suite — to behave identically. Switching between SQLite,
MongoDB, and PostgreSQL is a configuration change, never a code change.

**PostgreSQL is the production store.** New durable fields ship on
`PostgreSQLStore` first and must have a live PostgreSQL test. SQLite is the
isolated-test / library-default backend. MongoDB is secondary. When a host
selects `Backend: "postgres"`, Open fails closed; it does not fall back.

## Unified interface & `store.Open`

`store.Store` is the single, complete contract (sessions, core sessions, users,
messages, opened files, user files, tool calls, summarization logs, route traces,
visited nodes, lifecycle, and `BackendInfo`). `SQLiteStore`, `MongoDBStore`,
`PostgreSQLStore`, and the cached `DBStore` all implement it; a compile-time
assertion in `store.go` fails the build if any backend ever drifts.

The simplest way to construct a backend is the `store.Open` factory:

```go
import "github.com/ghiac/agentize/store"

// PostgreSQL — production. Agentize objects live in an isolated schema (default "agentize").
st, err := store.Open(store.Config{
    Backend: "postgres", PostgresAddr: "localhost:5432", PostgresDatabase: "app",
    PostgresUser: "app", PostgresSchema: "agentize",
})

// SQLite — isolated unit tests. Empty path => ./data/sessions.db; ":memory:" => ephemeral.
st, err := store.Open(store.Config{Backend: "sqlite", SQLitePath: ":memory:"})

// MongoDB — secondary backend, kept for parity.
st, err := store.Open(store.Config{Backend: "mongodb", MongoURI: "mongodb://localhost:27017"})
```

### Behavioral contract (identical on every backend)

- Optional "get by id" lookups (`GetUser`, `GetCoreSession`, `GetUserFile`,
  `GetToolCallByID`, `GetToolCallByToolID`) return `(nil, nil)` when not found.
- `Get(sessionID)` returns an error when the session does not exist.
- `Put*` methods are upserts.
- List orderings are fixed (messages, summarization logs & route traces
  newest-first; opened files oldest-first).
- Timestamps round-trip at one-second precision.

Parity is enforced by `conformance_test.go`, which runs the same suite against
SQLite (file + in-memory), MongoDB, and PostgreSQL. MongoDB/PostgreSQL can use
testcontainers or an external server (`MONGODB_URI`, `AGENTIZE_POSTGRES_ADDR` /
`AGENTIZE_POSTGRES_CONFIG_FILE`).

## Available Stores

### DBStore (SQLite test / library default)
SQLite-backed storage with an in-memory read cache for sessions and users. It
**persists** to a SQLite file and is what `agentize.New` uses when the host
does not pass a store. Production hosts should pass `PostgreSQLStore` instead.

```go
import "github.com/ghiac/agentize/store"

dbStore, err := store.NewDBStore() // ./data/sessions.db
```

### SQLiteStore
SQLite-based storage that persists data to a file. Use it for isolated tests
and local library defaults, not for production hosts.

```go
import "github.com/ghiac/agentize/store"

// File-based storage
sqliteStore, err := store.NewSQLiteStore("./data/sessions.db")
if err != nil {
    log.Fatal(err)
}
defer sqliteStore.Close()

// In-memory storage (for testing)
sqliteStore, err := store.NewSQLiteStore("")
if err != nil {
    log.Fatal(err)
}
defer sqliteStore.Close()
```

### MongoDBStore
MongoDB-based storage kept for parity with older hosts. New durable fields
must land on PostgreSQL first; MongoDB is secondary.

```go
import "github.com/ghiac/agentize/store"

// Using default configuration
config := store.DefaultMongoDBStoreConfig()
config.URI = "mongodb://localhost:27017"
config.Database = "agentize"
config.Collection = "sessions"

mongoStore, err := store.NewMongoDBStore(config)
if err != nil {
    log.Fatal(err)
}
defer mongoStore.Close()

// Or use convenience function
mongoStore, err := store.NewMongoDBStoreFromURI("mongodb://localhost:27017")
if err != nil {
    log.Fatal(err)
}
defer mongoStore.Close()
```

### PostgreSQLStore (production)
PostgreSQL-backed storage. This is the backend production hosts must use.
Agentize objects live in an isolated schema (default `agentize`) with JSONB
documents, `ON CONFLICT` upserts, and covering indexes. Backup is delegated to
`pg_dump`. New schema goes here first; add SQLite/MongoDB parity after the
PostgreSQL path and its test are green.

```go
import "github.com/ghiac/agentize/store"

st, err := store.NewPostgreSQLStore(store.PostgreSQLStoreConfig{
    Addr: "localhost:5432", Database: "app", User: "app", Schema: "agentize",
})
if err != nil {
    log.Fatal(err)
}
defer st.Close()
```

## Usage with Agentize

### Using SQLiteStore

```go
import (
    "github.com/ghiac/agentize"
    "github.com/ghiac/agentize/store"
)

// Create SQLite store
sqliteStore, err := store.NewSQLiteStore("./data/sessions.db")
if err != nil {
    log.Fatal(err)
}
defer sqliteStore.Close()

// Create Agentize with SQLite store
ag, err := agentize.NewWithOptions("./knowledge", &agentize.Options{
    SessionStore: sqliteStore,
})
if err != nil {
    log.Fatal(err)
}

// Now Agentize will use SQLite for session persistence
```

### Using MongoDBStore

```go
import (
    "github.com/ghiac/agentize"
    "github.com/ghiac/agentize/store"
)

// Create MongoDB store
config := store.DefaultMongoDBStoreConfig()
config.URI = "mongodb://localhost:27017"
config.Database = "agentize"
config.Collection = "sessions"

mongoStore, err := store.NewMongoDBStore(config)
if err != nil {
    log.Fatal(err)
}
defer mongoStore.Close()

// Create Agentize with MongoDB store
ag, err := agentize.NewWithOptions("./knowledge", &agentize.Options{
    SessionStore: mongoStore,
})
if err != nil {
    log.Fatal(err)
}

// Now Agentize will use MongoDB for session persistence
```

## Custom Store Implementation

A custom backend must implement the full **`store.Store`** interface (defined in
`store.go`) so it is a drop-in replacement for the built-ins. The easiest way to
guarantee correctness is to run the shared conformance suite against it.

`agentize.NewWithOptions` accepts any `store.SessionStore` for `Options.SessionStore`,
but verifies at startup that it also satisfies `store.Store`, failing fast with a
clear error otherwise — so missing methods never silently skip persistence.

## Database Schema

SQLiteStore uses the following schema:

```sql
CREATE TABLE sessions (
    session_id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    agent_type TEXT NOT NULL,
    data TEXT NOT NULL,  -- JSON serialized Session
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE INDEX idx_sessions_user_id ON sessions(user_id);
CREATE INDEX idx_sessions_updated_at ON sessions(updated_at);
CREATE UNIQUE INDEX idx_sessions_user_core ON sessions(user_id, agent_type) WHERE agent_type = 'core';
```

## Core Session Uniqueness

**Important**: For each user, there can be only **one Core session**. This is enforced by:

1. **Database constraint**: A unique index ensures only one Core session per user
2. **PutCoreSession method**: Automatically deletes any existing Core sessions before storing a new one
3. **Put method**: Automatically routes Core sessions to `PutCoreSession` to enforce uniqueness

### Helper Methods for Core Sessions

```go
// Get the Core session for a user (returns nil if none exists)
coreSession, err := sqliteStore.GetCoreSession("user123")

// Store/update Core session (replaces any existing Core session for the user)
err := sqliteStore.PutCoreSession(coreSession)

// Regular Put also works - it automatically handles Core session uniqueness
err := sqliteStore.Put(coreSession) // Same as PutCoreSession for Core sessions
```

## Notes

- **SQLiteStore**: Uses JSON serialization for Session objects. All timestamps are stored as Unix timestamps (integers). Isolated tests and local defaults only.

- **PostgreSQLStore**: JSONB documents, covering indexes, `ON CONFLICT` upserts, and a partial unique index for one Core session per user. Lives in an isolated schema so it can share a PostgreSQL server with a host application. `store.Open` requires addr and database; there is no silent fallback to SQLite/MongoDB.

- **Core Session Uniqueness**: Every store enforces that only one Core session exists per user. This is handled automatically through:
  - SQLite: Unique index with partial filter
  - MongoDB: Unique index with partial filter expression
  - PostgreSQL: Unique partial index on `user_id WHERE agent_type='core'`

- **UserNodes**: Visited nodes are still stored in-memory for performance (same for all store implementations)

- **Backward Compatibility**: All stores implement the same `SessionStore` interface, so you can switch between them without changing your code
