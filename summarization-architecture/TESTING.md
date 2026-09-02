# Summarization testing

## Deterministic tests

```bash
GOCACHE=/tmp/agentize-go-cache go test ./model ./engine ./debuger/pages ./debuger/ui/components
```

Coverage includes legacy scalar-to-array decoding, immutable append semantics,
tag ordering, title synchronization, invalid empty responses, system-prompt
retention/cleanup, and independent detail-page pagination.

## Full suite

```bash
GOCACHE=/tmp/agentize-go-cache go test ./...
```

The host compatibility check must also run `crypto-data/internal/aichat` and
`crypto-data/internal/debugapi` in a temporary Go workspace that uses the local
Agentize module (without editing the host's production `go.mod`). This check
passed on 2026-09-02.

## PostgreSQL

The host repository owns the canonical integration config. From `crypto-data`,
run the bounded PostgreSQL store smoke tests using `configs/config.yaml`; never
print the DSN, credentials, or `proxy.url`. A connection timeout is reported as
VPN/reachability, not an application failure.

Verification on 2026-09-02:

```bash
AI_CHAT_PG_SMOKE=1 GOCACHE=/tmp/crypto-go-cache go test ./internal/aichat \
  -run '^TestAIChatPostgresStoreSmoke$' -count=1
```

The deterministic setup reached the canonical configuration but the host had
no route to `192.168.100.200:5432`. This is a VPN/network reachability result;
the PostgreSQL application path remains to be re-run once that route is
available.

## Production read-only checks

Use `/debug/v1/whoami`, `/debug/v1/health/ai-chat`, and the three aggregate
Agentize metric prefixes. The Debug API intentionally cannot return a session
transcript or session identifier; inspect those only through the authenticated
Agentize admin page.
