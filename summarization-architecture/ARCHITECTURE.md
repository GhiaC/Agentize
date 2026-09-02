# Summarization architecture

## Durable model

`Session.Summary` is `model.SummaryEntries`, serialized as a JSON array of
strings under the existing `Summary` key. Entries are ordered oldest to newest.
Old rows whose JSON contains a scalar string load as a one-element array and are
written back in array form on their next normal update; no destructive database
migration is required.

The invariants are:

1. Existing summary entries are immutable. A cycle may only append new,
   non-empty, non-duplicate important facts.
2. Existing tags keep their value and order. A cycle may only append a new
   case-insensitive tag, up to seven tags.
3. A syntactically empty provider response is a failed cycle: messages are not
   archived and `SummarizedAt` is not advanced. A valid JSON `[]` is a successful
   no-op and sets `SummaryInitialized`, preventing a retry loop.
4. The title is regenerated on every successful summary cycle. The linked
   `Conversation.Title` is updated from the same value as `Session.Title`.
5. System messages are mutable runtime configuration. They stay in the active
   window and are never appended to `ArchivedMsgs`; legacy archived system
   snapshots are purged on the next cycle.

## Cycle

The scheduler locks and reloads the session, formats only user messages, asks
the model for a JSON array containing only new facts, validates the response,
appends facts and tags, regenerates the title, and then rolls the transcript
window. The session is persisted before the linked conversation is synchronized.
Conversation sync is retried naturally by every later cycle and a failure is
logged without discarding the already durable session memory.

Rows with `SummarizedAt` but no summary and no `SummaryInitialized` are legacy
incomplete rows. They use first-cycle eligibility and can recover from archived
messages even when the active window is empty.

## Debug UI

The session detail page has independent 25-row pages for current prompts,
active messages, archived messages, summarization cycles, tool calls and opened
files. Each collection uses its own query parameter so moving through archived
history does not change the tool-call page. Collection cards are native
`<details>` controls; a requested page greater than one opens its card.

Only active system prompts are labeled current. Historical prompt snapshots
from older releases are counted as migration debt but their full content is not
shown as current state.

## Host-owned context

Hosts that persist a marker-based system context must enforce exactly one
active marker message and remove old marker snapshots from `ArchivedMsgs`.
`crypto-data/internal/aichat.refreshSessionContext` owns that rule for
`AI_CHAT_SESSION_CONTEXT_V1`.

The host currently consumes Agentize as a versioned Go module. Releasing this
change therefore requires publishing the Agentize revision first, updating the
host's `github.com/ghiac/agentize` version, and deploying both changes together.
Do not add a local-path `replace` directive to the production host module.

## Operational visibility

The production Debug API remains owner-free and must never expose session IDs,
chat text, prompt bodies, tool arguments or results. It exposes only aggregate
low-cardinality metrics through these allowlisted families:

- `agentize_summarization_`
- `agentize_scheduler_`
- `agentize_summary_`
