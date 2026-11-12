package mcpauth

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/ghiac/agentize/toolruntime"

	"github.com/lib/pq"
)

// PostgresStore is the PostgreSQL store for private MCP tokens and command admission.
type PostgresStore struct{ db *sql.DB }

func NewPostgresStore(db *sql.DB) *PostgresStore {
	if db == nil {
		return nil
	}
	return &PostgresStore{db: db}
}

func (r *PostgresStore) Insert(ctx context.Context, rec Record, tokenHash string) error {
	if r == nil || r.db == nil {
		return errors.New("mcp credentials: postgres is required")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Serialize issuance for one owner across every server instance.
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, "agentize-mcp:"+rec.Owner); err != nil {
		return err
	}
	var active int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM mcp_credentials WHERE owner=$1 AND revoked_at IS NULL AND expires_at>$2`, rec.Owner, rec.CreatedAt).Scan(&active); err != nil {
		return err
	}
	if active >= maxActiveCredentials {
		return ErrLimit
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO mcp_credentials (id, owner, name, token_hash, scopes, expires_at, created_at) VALUES ($1,$2,$3,$4,$5,$6,$7)`, rec.ID, rec.Owner, rec.Name, tokenHash, pq.Array(rec.Scopes), rec.ExpiresAt, rec.CreatedAt)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (r *PostgresStore) List(ctx context.Context, owner string) ([]Record, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("mcp credentials: postgres is required")
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, owner, name, scopes, expires_at, revoked_at, created_at, last_used_at
		FROM mcp_credentials WHERE owner=$1 ORDER BY created_at DESC`, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Record
	for rows.Next() {
		rec, err := scanCredential(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

func (r *PostgresStore) Revoke(ctx context.Context, owner, id string, at time.Time) (bool, error) {
	if r == nil || r.db == nil {
		return false, errors.New("mcp credentials: postgres is required")
	}
	res, err := r.db.ExecContext(ctx, `
		UPDATE mcp_credentials SET revoked_at=$3
		WHERE id=$1 AND owner=$2 AND revoked_at IS NULL`, id, owner, at)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	if n == 1 {
		return true, nil
	}
	var found string
	err = r.db.QueryRowContext(ctx, `SELECT owner FROM mcp_credentials WHERE id=$1`, id).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) || found != owner {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (r *PostgresStore) FindByHash(ctx context.Context, hash string) (Record, bool, error) {
	if r == nil || r.db == nil {
		return Record{}, false, errors.New("mcp credentials: postgres is required")
	}
	row := r.db.QueryRowContext(ctx, `
		SELECT id, owner, name, scopes, expires_at, revoked_at, created_at, last_used_at
		FROM mcp_credentials WHERE token_hash=$1`, hash)
	rec, err := scanCredential(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Record{}, false, nil
	}
	if err != nil {
		return Record{}, false, err
	}
	return rec, true, nil
}

func (r *PostgresStore) CountActive(ctx context.Context, owner string, now time.Time) (int, error) {
	if r == nil || r.db == nil {
		return 0, errors.New("mcp credentials: postgres is required")
	}
	var n int
	err := r.db.QueryRowContext(ctx, `
		SELECT count(*) FROM mcp_credentials
		WHERE owner=$1 AND revoked_at IS NULL AND expires_at > $2`, owner, now).Scan(&n)
	return n, err
}

func (r *PostgresStore) Touch(ctx context.Context, id string, at time.Time) error {
	if r == nil || r.db == nil {
		return errors.New("mcp credentials: postgres is required")
	}
	_, err := r.db.ExecContext(ctx, `UPDATE mcp_credentials SET last_used_at=$2 WHERE id=$1`, id, at)
	return err
}

type credentialScanner interface{ Scan(dest ...any) error }

func scanCredential(row credentialScanner) (Record, error) {
	var rec Record
	var scopes pq.StringArray
	var revoked, used sql.NullTime
	err := row.Scan(&rec.ID, &rec.Owner, &rec.Name, &scopes, &rec.ExpiresAt, &revoked, &rec.CreatedAt, &used)
	if err != nil {
		return Record{}, err
	}
	rec.Scopes = append([]string(nil), scopes...)
	if revoked.Valid {
		rec.RevokedAt = &revoked.Time
	}
	if used.Valid {
		rec.LastUsedAt = &used.Time
	}
	return rec, nil
}

func (r *PostgresStore) Begin(ctx context.Context, admission toolruntime.Admission) (toolruntime.Receipt, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("mcp credentials: postgres is required")
	}
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO mcp_invocations (
			request_id, user_id, credential_id, tool, tool_version, catalog_version,
			payload_hash, idempotency_key, inference_allowed, outcome)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,'admitted')`,
		admission.RequestID, admission.UserID, admission.CredentialID, admission.Tool, admission.ToolVersion,
		admission.CatalogVersion, admission.PayloadHash, admission.IdempotencyKey, admission.InferenceAllowed)
	if err != nil {
		return nil, err
	}
	return invocationReceipt{db: r.db, id: admission.RequestID}, nil
}

type invocationReceipt struct {
	db *sql.DB
	id string
}

func (r *PostgresStore) RecordUsage(ctx context.Context, usage Usage) error {
	if r == nil || r.db == nil {
		return errors.New("mcp credentials: postgres is required")
	}
	if strings.TrimSpace(usage.UserID) == "" || strings.TrimSpace(usage.CredentialID) == "" || strings.TrimSpace(usage.Tool) == "" || strings.TrimSpace(usage.Outcome) == "" {
		return errors.New("mcp usage: owner, credential, tool, and outcome are required")
	}
	if usage.DurationMS < 0 || usage.InferenceUnits < 0 {
		return errors.New("mcp usage: measurements cannot be negative")
	}
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO mcp_usage (user_id, credential_id, tool, outcome, duration_ms, inference_units, job_id, detail)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		usage.UserID, usage.CredentialID, usage.Tool, usage.Outcome, usage.DurationMS, usage.InferenceUnits, usage.JobID, trimUsageDetail(usage.Detail))
	return err
}

func (r *PostgresStore) UsageSummary(ctx context.Context, owner string, query UsageQuery) (UsageSummary, error) {
	if r == nil || r.db == nil {
		return UsageSummary{}, errors.New("mcp credentials: postgres is required")
	}
	limit := query.Limit
	if limit <= 0 || limit > 50 {
		limit = 20
	}
	offset := query.Offset
	if offset < 0 {
		offset = 0
	}
	summary := UsageSummary{
		Tools: []ToolUsage{}, Statuses: []StatusCount{}, Events: []UsageEvent{},
		Offset: offset, Limit: limit, Outcome: query.Outcome,
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT credential_id, tool, count(*),
		       count(*) FILTER (WHERE outcome <> 'ok'),
		       coalesce(sum(duration_ms), 0), coalesce(sum(inference_units), 0)
		FROM mcp_usage
		WHERE user_id=$1 AND created_at >= $2
		GROUP BY credential_id, tool
		ORDER BY credential_id, tool`, owner, query.Since)
	if err != nil {
		return UsageSummary{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var row ToolUsage
		if err := rows.Scan(&row.CredentialID, &row.Tool, &row.Calls, &row.Errors, &row.DurationMS, &row.InferenceUnits); err != nil {
			return UsageSummary{}, err
		}
		summary.Calls += row.Calls
		summary.Errors += row.Errors
		summary.DurationMS += row.DurationMS
		summary.InferenceUnits += row.InferenceUnits
		summary.Tools = append(summary.Tools, row)
	}
	if err := rows.Err(); err != nil {
		return UsageSummary{}, err
	}
	statuses, err := r.db.QueryContext(ctx, `
		SELECT outcome, count(*)
		FROM mcp_usage
		WHERE user_id=$1 AND created_at >= $2
		GROUP BY outcome
		ORDER BY outcome`, owner, query.Since)
	if err != nil {
		return UsageSummary{}, err
	}
	defer statuses.Close()
	for statuses.Next() {
		var row StatusCount
		if err := statuses.Scan(&row.Outcome, &row.Calls); err != nil {
			return UsageSummary{}, err
		}
		summary.Statuses = append(summary.Statuses, row)
	}
	if err := statuses.Err(); err != nil {
		return UsageSummary{}, err
	}
	if err := r.db.QueryRowContext(ctx, `
		SELECT count(*)
		FROM mcp_usage
		WHERE user_id=$1 AND created_at >= $2 AND ($3 = '' OR outcome = $3)`,
		owner, query.Since, query.Outcome).Scan(&summary.EventTotal); err != nil {
		return UsageSummary{}, err
	}
	events, err := r.db.QueryContext(ctx, `
		SELECT credential_id, tool, outcome, duration_ms, inference_units, job_id, detail, created_at
		FROM mcp_usage
		WHERE user_id=$1 AND created_at >= $2 AND ($3 = '' OR outcome = $3)
		ORDER BY created_at DESC
		LIMIT $4 OFFSET $5`, owner, query.Since, query.Outcome, limit, offset)
	if err != nil {
		return UsageSummary{}, err
	}
	defer events.Close()
	for events.Next() {
		var event UsageEvent
		if err := events.Scan(&event.CredentialID, &event.Tool, &event.Outcome, &event.DurationMS, &event.InferenceUnits, &event.JobID, &event.Detail, &event.CreatedAt); err != nil {
			return UsageSummary{}, err
		}
		summary.Events = append(summary.Events, event)
	}
	return summary, events.Err()
}

func trimUsageDetail(detail string) string {
	detail = strings.TrimSpace(detail)
	detail = strings.ReplaceAll(detail, "\n", " ")
	if strings.Contains(detail, "mcp_") {
		return "detail omitted"
	}
	if len(detail) > 400 {
		detail = detail[:400]
	}
	return detail
}

func (r invocationReceipt) Finish(ctx context.Context, outcome string) error {
	if outcome == "" {
		outcome = "ok"
	}
	res, err := r.db.ExecContext(ctx, `
		UPDATE mcp_invocations SET outcome=$2, finished_at=now()
		WHERE request_id=$1 AND outcome='admitted'`, r.id, outcome)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return errors.New("mcp invocation outcome was not admitted")
	}
	return nil
}
