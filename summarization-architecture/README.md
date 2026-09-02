# Session summarization

This folder is the source of truth for Agentize session-memory cycles and the
debug session page.

- [ARCHITECTURE.md](./ARCHITECTURE.md) — invariants, lifecycle, persistence and
  the Conversation/Session boundary.
- [TESTING.md](./TESTING.md) — deterministic and PostgreSQL verification.

The canonical implementation is `engine.SessionScheduler`. The older
`model.SessionHandler.SummarizeSession` remains a compatibility entry point and
must preserve the same storage invariants.
