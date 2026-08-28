package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/adamf/jetway/internal/ulid"
	"github.com/adamf/jetway/pkg/pnr"
)

// Postgres is the production Store.
type Postgres struct {
	pool *pgxpool.Pool
}

// OpenPostgres connects and verifies the schema is present.
func OpenPostgres(ctx context.Context, dsn string) (*Postgres, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("store: bad DSN: %w", err)
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("store: connect: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("store: ping: %w", err)
	}
	return &Postgres{pool: pool}, nil
}

func (s *Postgres) Close() error { s.pool.Close(); return nil }

func (s *Postgres) AppendMessage(ctx context.Context, m *Message) error {
	if m.ID == "" {
		m.ID = ulid.New()
	}
	diags, err := json.Marshal(nonNilDiags(m.Diagnostics))
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO message (id, direction, at, transport, peer, format, kind,
		                     raw, sha256, size_bytes, status, error, dedup_key,
		                     pnr_id, correlation_id, diagnostics)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`,
		m.ID, m.Direction, m.At, m.Transport, m.Peer, m.Format, nullIfEmpty(m.Kind),
		m.Raw, m.SHA256, m.Size, m.Status, nullIfEmpty(m.Error), nullIfEmpty(m.DedupKey),
		nullIfEmpty(m.PNRID), nullIfEmpty(m.CorrelationID), diags)
	if err != nil {
		return fmt.Errorf("store: append message: %w", err)
	}
	return nil
}

func (s *Postgres) UpdateMessage(ctx context.Context, m *Message) error {
	diags, err := json.Marshal(nonNilDiags(m.Diagnostics))
	if err != nil {
		return err
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE message SET status=$2, error=$3, kind=$4, format=$5,
		                   pnr_id=$6, correlation_id=$7, dedup_key=$8, diagnostics=$9
		WHERE id=$1`,
		m.ID, m.Status, nullIfEmpty(m.Error), nullIfEmpty(m.Kind), m.Format,
		nullIfEmpty(m.PNRID), nullIfEmpty(m.CorrelationID), nullIfEmpty(m.DedupKey), diags)
	if err != nil {
		return fmt.Errorf("store: update message: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

const messageColumns = `id, direction, at, transport, peer, format,
	coalesce(kind,''), raw, sha256, size_bytes, status, coalesce(error,''),
	coalesce(dedup_key,''), coalesce(pnr_id,''), coalesce(correlation_id,''), diagnostics`

func scanMessage(row pgx.Row) (*Message, error) {
	var m Message
	var diags []byte
	err := row.Scan(&m.ID, &m.Direction, &m.At, &m.Transport, &m.Peer, &m.Format,
		&m.Kind, &m.Raw, &m.SHA256, &m.Size, &m.Status, &m.Error,
		&m.DedupKey, &m.PNRID, &m.CorrelationID, &diags)
	if err != nil {
		return nil, err
	}
	if len(diags) > 0 {
		_ = json.Unmarshal(diags, &m.Diagnostics)
	}
	return &m, nil
}

func (s *Postgres) GetMessage(ctx context.Context, id string) (*Message, error) {
	m, err := scanMessage(s.pool.QueryRow(ctx, `SELECT `+messageColumns+` FROM message WHERE id=$1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return m, err
}

func (s *Postgres) ListMessages(ctx context.Context, f MessageFilter) ([]*Message, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, `
		SELECT `+messageColumns+` FROM message
		WHERE ($1='' OR peer=$1)
		  AND ($2='' OR pnr_id=$2)
		  AND ($3='' OR status=$3)
		  AND ($4='' OR id>$4)
		ORDER BY id DESC LIMIT $5`,
		f.Peer, f.PNRID, string(f.Status), f.SinceID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Message
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	// Query descending so a limit keeps the newest; hand back chronological.
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, rows.Err()
}

func (s *Postgres) FindByDedupKey(ctx context.Context, peer, key string) (string, bool, error) {
	if key == "" {
		return "", false, nil
	}
	var id string
	err := s.pool.QueryRow(ctx, `
		SELECT id FROM message
		WHERE peer=$1 AND dedup_key=$2 AND direction='in'
		ORDER BY id ASC LIMIT 1`, peer, key).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	return id, err == nil, err
}

func (s *Postgres) CreatePNR(ctx context.Context, p *pnr.PNR, events []Event) error {
	if p.ID == "" {
		p.ID = ulid.New()
	}
	p.Version = 1
	state, err := json.Marshal(p)
	if err != nil {
		return err
	}
	return s.tx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO pnr (id, record_locator, version, status, created_at, updated_at, state)
			VALUES ($1,$2,$3,$4,$5,$6,$7)`,
			p.ID, p.RecordLocator, p.Version, p.Status, p.CreatedAt, p.UpdatedAt, state)
		if err != nil {
			if isUniqueViolation(err) {
				return ErrDuplicate
			}
			return err
		}
		return insertEvents(ctx, tx, p.ID, 0, events)
	})
}

func (s *Postgres) UpdatePNR(ctx context.Context, p *pnr.PNR, expected int64, events []Event) error {
	p.Version = expected + 1
	state, err := json.Marshal(p)
	if err != nil {
		return err
	}
	return s.tx(ctx, func(tx pgx.Tx) error {
		// The version predicate is the whole point: if another writer got here
		// first, this update affects no rows and the caller must re-read.
		tag, err := tx.Exec(ctx, `
			UPDATE pnr SET version=$2, status=$3, updated_at=$4, state=$5, record_locator=$6
			WHERE id=$1 AND version=$7`,
			p.ID, p.Version, p.Status, p.UpdatedAt, state, p.RecordLocator, expected)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			var exists bool
			if err := tx.QueryRow(ctx, `SELECT true FROM pnr WHERE id=$1`, p.ID).Scan(&exists); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return ErrNotFound
				}
				return err
			}
			return ErrConflict
		}
		var maxSeq int64
		if err := tx.QueryRow(ctx,
			`SELECT coalesce(max(seq),0) FROM pnr_event WHERE pnr_id=$1`, p.ID).Scan(&maxSeq); err != nil {
			return err
		}
		return insertEvents(ctx, tx, p.ID, maxSeq, events)
	})
}

func insertEvents(ctx context.Context, tx pgx.Tx, pnrID string, startSeq int64, events []Event) error {
	for i := range events {
		e := events[i]
		if e.ID == "" {
			e.ID = ulid.New()
		}
		if e.At.IsZero() {
			e.At = time.Now().UTC()
		}
		startSeq++
		_, err := tx.Exec(ctx, `
			INSERT INTO pnr_event (id, pnr_id, seq, type, detail, payload, message_id, actor, at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
			e.ID, pnrID, startSeq, e.Type, nullIfEmpty(e.Detail), jsonOrNil(e.Payload),
			nullIfEmpty(e.MessageID), nullIfEmpty(e.Actor), e.At)
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *Postgres) getPNR(ctx context.Context, where string, arg any) (*pnr.PNR, error) {
	var state []byte
	var version int64
	err := s.pool.QueryRow(ctx, `SELECT state, version FROM pnr WHERE `+where, arg).Scan(&state, &version)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var p pnr.PNR
	if err := json.Unmarshal(state, &p); err != nil {
		return nil, fmt.Errorf("store: decode pnr state: %w", err)
	}
	// The column is authoritative for the version: the projection is written in
	// the same transaction but the column is what the concurrency check reads.
	p.Version = version
	return &p, nil
}

func (s *Postgres) GetPNR(ctx context.Context, locator string) (*pnr.PNR, error) {
	return s.getPNR(ctx, "record_locator=$1", locator)
}

func (s *Postgres) GetPNRByID(ctx context.Context, id string) (*pnr.PNR, error) {
	return s.getPNR(ctx, "id=$1", id)
}

func (s *Postgres) ListPNRs(ctx context.Context, limit int) ([]*pnr.PNR, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx,
		`SELECT state, version FROM pnr ORDER BY updated_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*pnr.PNR
	for rows.Next() {
		var state []byte
		var version int64
		if err := rows.Scan(&state, &version); err != nil {
			return nil, err
		}
		var p pnr.PNR
		if err := json.Unmarshal(state, &p); err != nil {
			return nil, err
		}
		p.Version = version
		out = append(out, &p)
	}
	return out, rows.Err()
}

func (s *Postgres) Events(ctx context.Context, pnrID string) ([]Event, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, pnr_id, seq, type, coalesce(detail,''), payload,
		       coalesce(message_id,''), coalesce(actor,''), at
		FROM pnr_event WHERE pnr_id=$1 ORDER BY seq`, pnrID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var e Event
		var payload []byte
		if err := rows.Scan(&e.ID, &e.PNRID, &e.Seq, &e.Type, &e.Detail,
			&payload, &e.MessageID, &e.Actor, &e.At); err != nil {
			return nil, err
		}
		e.Payload = payload
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Postgres) NextLocatorCounter(ctx context.Context) (uint64, error) {
	var n int64
	if err := s.pool.QueryRow(ctx, `SELECT nextval('locator_counter')`).Scan(&n); err != nil {
		return 0, err
	}
	return uint64(n), nil
}

func (s *Postgres) tx(ctx context.Context, fn func(pgx.Tx) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op once committed
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func isUniqueViolation(err error) bool {
	// 23505 is unique_violation. Matching on the code rather than the message
	// keeps this working across locales and server versions.
	var pgErr interface{ SQLState() string }
	return errors.As(err, &pgErr) && pgErr.SQLState() == "23505"
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func jsonOrNil(b json.RawMessage) any {
	if len(b) == 0 {
		return nil
	}
	return []byte(b)
}

func nonNilDiags(d []Diagnostic) []Diagnostic {
	if d == nil {
		return []Diagnostic{}
	}
	return d
}
