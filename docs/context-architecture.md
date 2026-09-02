# Context and capability architecture

Status: accepted implementation design (2026-09-02)

## Goals

Every LLM request receives a small, explicit array of system-prompt entries. A
session persists the last array actually assembled for it so the debug dashboard
can explain the request without reconstructing it from transcript messages.

Durable memory is split by ownership:

- **User context**: facts and tags that remain useful across conversations.
- **Session context**: title, append-only summary entries, and tags for one
  session/conversation.
- **Knowledge state**: opened node identities are runtime capability state, not
  prompt content. Knowledge is discovered and read on demand.

Collections that can grow without bound (files, conversations, historic
sessions) are queried with tools. They are not copied into every prompt.

## Prompt contract

Worker/conversation sessions assemble entries in this deterministic order:

1. `agent_instructions` — the stable worker contract.
2. `user_context` — cross-conversation summary entries and tags.
3. `session_context` — this session's title, summary entries, and tags.

Knowledge, complete position lists, web results, user files, and tool manifests
never become prompt entries. A compact account state may be written into
session context by the owning product, but detailed positions remain tool data.

The Core prompt contains only its controller instructions, user context and its
own session context. Deployment policy, agent catalog prose, registered-tool
prose, active-session lists, session lists and conversation lists are excluded.
Available operations remain discoverable from the request's tool schemas.

`Session.SystemPrompts` is the durable last-assembled snapshot. System prompts
must never be inserted into `Session.Msgs`; transcript history contains only
conversation/tool messages. A legacy fallback in the dashboard may show old
system messages until existing rows are naturally rewritten.

## Knowledge-tree capability rule

`manage_knowledge` is the discovery/read interface: `list` and `search`
return bounded metadata, `get` reads one node without changing capability
state, `open` returns its content and activates that exact node's tools, and
`close` deactivates them. `open_node` / `close_node` are the same open/close
operations with a dedicated schema. Both are registered as executable tools
and advertised whenever a knowledge repository is configured. Node content is
returned in the tool result; it is never copied into the system prompt.

`search_tools` searches the tree and returns `{name, path}` so the model can
open the owning node. It does not load an unopened node's schema into the
turn.

No descendant, sibling, or unopened node grants tools. Root behaves like every
other node: its tools are available only when root is explicitly represented in
the session's opened-node set. Built-in platform tools (file manager, result
inspection, scheduler, browser, and knowledge-tree `open_node` /
`manage_knowledge`) are not knowledge-node tools and are gated by
their configured runtime dependency.

The former `LoadAllTools()` path violated this invariant and must not be used on
the request path.

## File system tools

User files are an on-demand file system, not prompt material. `manage_files`
owns the lifecycle and enforces the injected user identity on every read/change:

- paginated and filterable `list`, with deterministic sorting;
- ranged `read`, `grep` across one/all text files, `save`;
- targeted replacement, line-range edit, and full overwrite;
- image edit and delete.

Large results remain behind the existing result-buffer inspection tools. File
bytes and the full user-file list never appear in a system prompt.

## Conversation and session discovery

Conversation identity is `ConversationID`; session IDs are storage/runtime
details. Listing, selecting, inspecting, renaming, archiving and deleting
conversations remain tools. Historic session discovery likewise remains a tool
when required for compatibility. Neither collection is a prompt section.

## Summarization and memory writes

The summarization scheduler is the only automatic memory writer. Its existing
session-summary and session-tag generators produce append-only Session deltas.
A separate classifier returns the cross-conversation proposal:

```json
{
  "summary": ["stable user fact"],
  "tags": ["stable-user-tag"]
}
```

The scheduler validates the shape, normalizes only new tags, performs
case-insensitive deduplication, preserves previous entries byte-for-byte and
applies configured limits. It first persists the user delta on the Session as a
retryable outbox, then idempotently merges it into User and clears the outbox.
This remains recoverable without requiring a cross-document transaction. An
empty array is a valid no-op. Provider text never directly mutates storage.

Interactive agents may receive a `manage_context` tool with `get`,
`add_summary`, and `add_tags` actions scoped to `user` or `session`. These calls
use injected identities and the same merge service as the scheduler. The
scheduler itself does not run as an unconstrained tool-using agent: proposed
changes plus an application-owned commit is easier to audit, retry and make
idempotent.

## PostgreSQL and compatibility

User context and prompt snapshots live in the existing JSONB `users.data` and
`sessions.data` documents, so no new OLTP table is required. PostgreSQL remains
the primary production path; SQLite exercises serialization and contracts in
unit tests. Missing fields decode as empty values. No backend fallback is
allowed when PostgreSQL is configured.

## Observability and dashboard

Session detail renders:

- the ordered `Current System Prompts` array as an index plus one independently
  readable document per entry (key/title/source/size);
- tool-retrievable dumps (knowledge, web, files, full position lists) in a
  separate excluded bucket, never labelled current;
- Opened Nodes;
- Opened Tools, grouped by contributing node;
- separate User Context and Session Context cards, including empty states.

The user detail page shows User Context, the active conversation's Session
Context, a paginated Conversations list, and the Core prompt array. Nonsense
counters, Core Agent (Brain), raw session lists, and Open Files are not shown.

The snapshot timestamp makes staleness visible. Archived system messages are
reported only as legacy debt and never labelled current.

## Rollout

1. Stop loading tools from unopened nodes and add regression tests.
2. Introduce typed prompt snapshots and dashboard rendering with legacy read
   fallback.
3. Remove unbounded Core prompt sections and add User/Session context.
4. Complete file-list pagination/filter/sort and line editing.
5. Add structured user-context deltas to summarization, then verify PostgreSQL
   serialization against the configured integration environment.

## Verification record

Implemented on 2026-09-02.

- Focused engine/Core/dashboard/store unit tests — passed after this revision.
- Focused context/tool regression tests — passed.
- PostgreSQL smoke command:
  `AGENTIZE_POSTGRES_CONFIG_FILE=../tradding-planner/crypto-data/configs/config.yaml go test ./store -run '^TestPostgreSQLStoreLiveSchemaAndRoundTrip$' -count=1 -v`
- PostgreSQL result: environment unreachable (`no route to host` for the
  configured shared server). This is a VPN/network reachability condition; the
  test did not reach application schema or round-trip assertions. Re-run the
  same bounded test when the shared network is available.
