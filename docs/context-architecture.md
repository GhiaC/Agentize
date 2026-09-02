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
- **Knowledge context**: a compact node index plus the contents and capabilities
  of nodes explicitly opened in the session.

Collections that can grow without bound (files, conversations, historic
sessions) are queried with tools. They are not copied into every prompt.

## Prompt contract

Worker/conversation sessions assemble entries in this deterministic order:

1. `agent_instructions` — the stable worker contract.
2. `knowledge_tree` — compact metadata for discoverable nodes; no node content.
3. `opened_nodes` — one entry per explicitly opened node.
4. `opened_tools` — a compact capability manifest for tools contributed by the
   opened nodes. The actual JSON schemas travel through the LLM tool channel.
5. `user_context` — cross-conversation summary entries and tags.
6. `session_context` — this session's title, summary entries, and tags.

The Core prompt contains only its controller instructions, user context and its
own session context. Deployment policy, agent catalog prose, registered-tool
prose, active-session lists, session lists and conversation lists are excluded.
Available operations remain discoverable from the request's tool schemas.

`Session.SystemPrompts` is the durable last-assembled snapshot. System prompts
must never be inserted into `Session.Msgs`; transcript history contains only
conversation/tool messages. A legacy fallback in the dashboard may show old
system messages until existing rows are naturally rewritten.

## Knowledge-tree capability rule

Opening a node grants two independent pieces of context for that session:

- its content may be injected as an opened-node system entry;
- its active tools may be exposed in the request tool array.

No descendant, sibling, or unopened node grants tools. Root behaves like every
other node: its tools are available only when root is explicitly represented in
the session's opened-node set. Built-in platform tools (file manager, result
inspection, scheduler, browser) are not knowledge-node tools and are gated by
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

- the ordered `Current System Prompts` array with key/title/source/size;
- Knowledge Tree node count/index;
- Opened Nodes;
- Opened Tools, grouped by contributing node;
- separate User Context and Session Context cards.

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

- `go test ./...` — passed.
- Focused context/tool regression tests — passed.
- PostgreSQL smoke command:
  `AGENTIZE_POSTGRES_CONFIG_FILE=../tradding-planner/crypto-data/configs/config.yaml go test ./store -run '^TestPostgreSQLStoreLiveSchemaAndRoundTrip$' -count=1 -v`
- PostgreSQL result: environment unreachable (`no route to host` for the
  configured shared server). This is a VPN/network reachability condition; the
  test did not reach application schema or round-trip assertions. Re-run the
  same bounded test when the shared network is available.
