package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/adamf/jetway/pkg/pnr"
	"github.com/adamf/jetway/pkg/ulid"
)

// Postgres is the production Store.
type Postgres struct {
	pool *pgxpool.Pool

	// Now, when set, stamps defaults instead of the wall clock. Set before
	// use; read without a lock.
	Now func() time.Time
}

func (s *Postgres) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now().UTC()
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
		                     pnr_id, correlation_id, diagnostics, possible_duplicate,
		                     trace_id, span_id)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19)`,
		m.ID, m.Direction, m.At, m.Transport, m.Peer, m.Format, nullIfEmpty(m.Kind),
		m.Raw, m.SHA256, m.Size, m.Status, nullIfEmpty(m.Error), nullIfEmpty(m.DedupKey),
		nullIfEmpty(m.PNRID), nullIfEmpty(m.CorrelationID), diags, m.PossibleDuplicate,
		nullIfEmpty(m.TraceID), nullIfEmpty(m.SpanID))
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
		                   pnr_id=$6, correlation_id=$7, dedup_key=$8, diagnostics=$9,
		                   possible_duplicate=$10
		WHERE id=$1`,
		m.ID, m.Status, nullIfEmpty(m.Error), nullIfEmpty(m.Kind), m.Format,
		nullIfEmpty(m.PNRID), nullIfEmpty(m.CorrelationID), nullIfEmpty(m.DedupKey), diags,
		m.PossibleDuplicate)
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
	coalesce(dedup_key,''), coalesce(pnr_id,''), coalesce(correlation_id,''), diagnostics,
	possible_duplicate, coalesce(trace_id,''), coalesce(span_id,'')`

func scanMessage(row pgx.Row) (*Message, error) {
	var m Message
	var diags []byte
	err := row.Scan(&m.ID, &m.Direction, &m.At, &m.Transport, &m.Peer, &m.Format,
		&m.Kind, &m.Raw, &m.SHA256, &m.Size, &m.Status, &m.Error,
		&m.DedupKey, &m.PNRID, &m.CorrelationID, &diags, &m.PossibleDuplicate,
		&m.TraceID, &m.SpanID)
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
	return s.findKey(ctx, Inbound, peer, key)
}

func (s *Postgres) FindOutboundByKey(ctx context.Context, peer, key string) (string, bool, error) {
	return s.findKey(ctx, Outbound, peer, key)
}

func (s *Postgres) findKey(ctx context.Context, dir Direction, peer, key string) (string, bool, error) {
	if key == "" {
		return "", false, nil
	}
	var id string
	err := s.pool.QueryRow(ctx, `
		SELECT id FROM message
		WHERE peer=$1 AND dedup_key=$2 AND direction=$3
		ORDER BY id ASC LIMIT 1`, peer, key, string(dir)).Scan(&id)
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
			INSERT INTO pnr (id, record_locator, version, status, created_at, updated_at, state, next_deadline)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
			p.ID, p.RecordLocator, p.Version, p.Status, p.CreatedAt, p.UpdatedAt, state,
			p.NextDeadline())
		if err != nil {
			if isUniqueViolation(err) {
				return ErrDuplicate
			}
			return err
		}
		return insertEvents(ctx, tx, p.ID, 0, events, s.now)
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
			UPDATE pnr SET version=$2, status=$3, updated_at=$4, state=$5, record_locator=$6,
			               next_deadline=$8
			WHERE id=$1 AND version=$7`,
			p.ID, p.Version, p.Status, p.UpdatedAt, state, p.RecordLocator, expected,
			p.NextDeadline())
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
		return insertEvents(ctx, tx, p.ID, maxSeq, events, s.now)
	})
}

func (s *Postgres) DividePNR(ctx context.Context, parent *pnr.PNR, expected int64,
	child *pnr.PNR, parentEvents, childEvents []Event) error {

	if child.ID == "" {
		child.ID = ulid.New()
	}
	child.Version = 1
	parent.Version = expected + 1
	parentState, err := json.Marshal(parent)
	if err != nil {
		return err
	}
	childState, err := json.Marshal(child)
	if err != nil {
		return err
	}

	return s.tx(ctx, func(tx pgx.Tx) error {
		// The parent goes first, so a losing writer rolls back before the
		// child locator is consumed. Creating the child first and failing here
		// is the torn write this method exists to make impossible.
		tag, err := tx.Exec(ctx, `
			UPDATE pnr SET version=$2, status=$3, updated_at=$4, state=$5, record_locator=$6,
			               next_deadline=$8
			WHERE id=$1 AND version=$7`,
			parent.ID, parent.Version, parent.Status, parent.UpdatedAt, parentState,
			parent.RecordLocator, expected, parent.NextDeadline())
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			var exists bool
			if err := tx.QueryRow(ctx, `SELECT true FROM pnr WHERE id=$1`, parent.ID).Scan(&exists); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return ErrNotFound
				}
				return err
			}
			return ErrConflict
		}
		var maxSeq int64
		if err := tx.QueryRow(ctx,
			`SELECT coalesce(max(seq),0) FROM pnr_event WHERE pnr_id=$1`, parent.ID).Scan(&maxSeq); err != nil {
			return err
		}
		if err := insertEvents(ctx, tx, parent.ID, maxSeq, parentEvents, s.now); err != nil {
			return err
		}

		if _, err := tx.Exec(ctx, `
			INSERT INTO pnr (id, record_locator, version, status, created_at, updated_at, state, next_deadline)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
			child.ID, child.RecordLocator, child.Version, child.Status,
			child.CreatedAt, child.UpdatedAt, childState, child.NextDeadline()); err != nil {
			if isUniqueViolation(err) {
				return ErrDuplicate
			}
			return err
		}
		return insertEvents(ctx, tx, child.ID, 0, childEvents, s.now)
	})
}

func insertEvents(ctx context.Context, tx pgx.Tx, pnrID string, startSeq int64, events []Event, now func() time.Time) error {
	for i := range events {
		e := events[i]
		if e.ID == "" {
			e.ID = ulid.New()
		}
		if e.At.IsZero() {
			e.At = now()
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

// scanPNRRow decodes one (state, version) row into a record.
func scanPNRRow(row pgx.Row) (*pnr.PNR, error) {
	var state []byte
	var version int64
	if err := row.Scan(&state, &version); err != nil {
		return nil, err
	}
	var p pnr.PNR
	if err := json.Unmarshal(state, &p); err != nil {
		return nil, fmt.Errorf("store: decode pnr state: %w", err)
	}
	// The column is authoritative for the version: the projection is written
	// in the same transaction but the column is what the concurrency check
	// reads.
	p.Version = version
	return &p, nil
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

const queueColumns = `id, queue, pnr_id, coalesce(locator,''), code, reason,
	coalesce(message_id,''), segment_ref, placed_at, placed_by,
	worked_at, coalesce(worked_by,''), coalesce(note,'')`

func scanQueueItem(row pgx.Row) (*QueueItem, error) {
	var q QueueItem
	if err := row.Scan(&q.ID, &q.Queue, &q.PNRID, &q.Locator, &q.Code, &q.Reason,
		&q.MessageID, &q.SegmentRef, &q.PlacedAt, &q.PlacedBy,
		&q.WorkedAt, &q.WorkedBy, &q.Note); err != nil {
		return nil, err
	}
	return &q, nil
}

func (s *Postgres) Enqueue(ctx context.Context, item *QueueItem) error {
	if item.ID == "" {
		item.ID = ulid.New()
	}
	if item.PlacedAt.IsZero() {
		item.PlacedAt = s.now()
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO queue_item (id, queue, pnr_id, locator, code, reason,
		                        message_id, segment_ref, placed_at, placed_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		item.ID, item.Queue, item.PNRID, nullIfEmpty(item.Locator), item.Code, item.Reason,
		nullIfEmpty(item.MessageID), item.SegmentRef, item.PlacedAt, item.PlacedBy)
	if err != nil {
		// The partial unique index fires when this record is already pending on
		// this queue for this reason, which is the sweeper running again rather
		// than an error.
		if isUniqueViolation(err) {
			return ErrDuplicate
		}
		return fmt.Errorf("store: enqueue: %w", err)
	}
	return nil
}

func (s *Postgres) WorkQueueItem(ctx context.Context, id, by, note string) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE queue_item SET worked_at=now(), worked_by=$2, note=$3
		WHERE id=$1 AND worked_at IS NULL`, id, by, nullIfEmpty(note))
	if err != nil {
		return fmt.Errorf("store: work queue item: %w", err)
	}
	if tag.RowsAffected() == 1 {
		return nil
	}
	// Nothing updated: either the item does not exist or someone already
	// worked it. Those are different answers to the caller.
	var exists bool
	if err := s.pool.QueryRow(ctx,
		`SELECT true FROM queue_item WHERE id=$1`, id).Scan(&exists); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	return ErrConflict
}

func (s *Postgres) ListQueue(ctx context.Context, f QueueFilter) ([]*QueueItem, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 200
	}
	rows, err := s.pool.Query(ctx, `
		SELECT `+queueColumns+` FROM queue_item
		WHERE ($1='' OR queue=$1)
		  AND ($2='' OR pnr_id=$2)
		  AND ($3 OR worked_at IS NULL)
		ORDER BY id DESC LIMIT $4`,
		f.Queue, f.PNRID, f.IncludeWorked, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*QueueItem
	for rows.Next() {
		q, err := scanQueueItem(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, q)
	}
	return out, rows.Err()
}

func (s *Postgres) QueueCounts(ctx context.Context) (map[string]int, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT queue, count(*) FROM queue_item WHERE worked_at IS NULL GROUP BY queue`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var name string
		var n int
		if err := rows.Scan(&name, &n); err != nil {
			return nil, err
		}
		out[name] = n
	}
	return out, rows.Err()
}

// findOnePNR runs a containment query and returns the first match.
//
// Containment is what the pnr_state_idx GIN index supports (it is built with
// jsonb_path_ops), so these are index lookups rather than the table scans they
// replace.
func (s *Postgres) findOnePNR(ctx context.Context, contains string) (*pnr.PNR, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT state, version FROM pnr WHERE state @> $1::jsonb ORDER BY updated_at DESC LIMIT 1`,
		contains)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		p, err := scanPNRRow(rows)
		if err != nil {
			return nil, err
		}
		return p, nil
	}
	return nil, rows.Err()
}

func (s *Postgres) FindPNRByDocument(ctx context.Context, compactNumber string) (*pnr.PNR, error) {
	// A document number is three digits of airline code and ten of serial, and
	// it is stored split, so the query has to split it too.
	if len(compactNumber) != 13 {
		return nil, nil
	}
	q, err := json.Marshal(map[string]any{
		"tickets": []any{map[string]any{
			"number": map[string]any{
				"airline_code": compactNumber[:3],
				"serial":       compactNumber[3:],
			},
		}},
	})
	if err != nil {
		return nil, err
	}
	return s.findOnePNR(ctx, string(q))
}

func (s *Postgres) FindPNRByExternalLocator(ctx context.Context, owner, value string) (*pnr.PNR, error) {
	if value == "" {
		return nil, nil
	}
	loc := map[string]any{"value": value}
	if owner != "" {
		loc["owner"] = owner
	}
	q, err := json.Marshal(map[string]any{"locators": []any{loc}})
	if err != nil {
		return nil, err
	}
	return s.findOnePNR(ctx, string(q))
}

func (s *Postgres) FindPNRsByFlight(ctx context.Context, flightKey, wireDate string, limit int) ([]*pnr.PNR, error) {
	carrier, number := splitFlightKey(flightKey)
	if carrier == "" || number == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 10000
	}

	// Containment matches exactly, and carriers write the same flight with and
	// without leading zeros, so every spelling has to be asked for.
	var where strings.Builder
	args := make([]any, 0, 8)
	for i, n := range flightNumberVariants(number) {
		seg := map[string]any{"carrier": carrier, "flight_num": n}
		if wireDate != "" {
			seg["wire_date"] = strings.ToUpper(wireDate)
		}
		q, err := json.Marshal(map[string]any{"segments": []any{seg}})
		if err != nil {
			return nil, err
		}
		if i > 0 {
			where.WriteString(" OR ")
		}
		fmt.Fprintf(&where, "state @> $%d::jsonb", i+1)
		args = append(args, string(q))
	}
	np := len(args)

	// Containment is the index's answer, not the question's: it will match a
	// segment this node has already cancelled, and it ignores segment type. So
	// it narrows, and SegmentOnFlight decides. That means the rows returned are
	// a superset, and stopping at the first page could report fewer bookings on
	// a flight than are really on it -- which is the class of quiet wrong
	// answer these lookups exist to remove. Page until the rows run out.
	sql := fmt.Sprintf(
		`SELECT state, version FROM pnr WHERE (%s) AND status <> $%d
		 ORDER BY updated_at DESC, id DESC LIMIT $%d OFFSET $%d`,
		where.String(), np+1, np+2, np+3)

	const page = 500
	var out []*pnr.PNR
	for offset := 0; len(out) < limit; offset += page {
		rows, err := s.pool.Query(ctx, sql, append(append([]any{}, args...),
			string(pnr.StatusCancelled), page, offset)...)
		if err != nil {
			return nil, err
		}
		n := 0
		for rows.Next() {
			n++
			p, err := scanPNRRow(rows)
			if err != nil {
				rows.Close()
				return nil, err
			}
			if pnrOnFlight(p, flightKey, wireDate) {
				out = append(out, p)
				if len(out) >= limit {
					break
				}
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
		if n < page {
			break
		}
	}
	return out, nil
}

// splitFlightKey separates a flight key such as "BA117" into its parts.
//
// The designator is the first two characters, not everything before the
// first digit: U2, 4U and 2B are real designators, and scanning for a digit
// splits them in half. Same defect, same fix, as NormaliseFlightKey.
func splitFlightKey(key string) (carrier, number string) {
	if len(key) < 3 {
		return "", ""
	}
	return key[:2], key[2:]
}

// flightNumberVariants returns the spellings of a flight number seen on the
// wire: bare, and zero-padded to three and four digits.
func flightNumberVariants(number string) []string {
	bare := strings.TrimLeft(number, "0")
	if bare == "" {
		bare = "0"
	}
	seen := map[string]bool{}
	var out []string
	for _, n := range []string{bare, fmt.Sprintf("%03s", bare), fmt.Sprintf("%04s", bare)} {
		n = strings.ReplaceAll(n, " ", "0")
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out
}

func (s *Postgres) FindPNRsStale(ctx context.Context, before time.Time, limit int) ([]*pnr.PNR, error) {
	if limit <= 0 {
		limit = 10000
	}
	// Ascending, served by pnr_stale_idx: the most overdue first, so a limit
	// drops the least urgent work rather than hiding all of it behind fresh
	// records.
	return s.queryPNRs(ctx,
		`SELECT state, version FROM pnr
		 WHERE status <> $1 AND updated_at < $2
		 ORDER BY updated_at ASC LIMIT $3`,
		string(pnr.StatusCancelled), before, limit)
}

func (s *Postgres) FindPNRsDueBy(ctx context.Context, deadline time.Time, limit int) ([]*pnr.PNR, error) {
	if limit <= 0 {
		limit = 10000
	}
	// next_deadline is maintained on write because a deadline buried in the
	// JSONB state cannot be range-scanned. Served by pnr_deadline_idx.
	return s.queryPNRs(ctx,
		`SELECT state, version FROM pnr
		 WHERE status <> $1 AND next_deadline IS NOT NULL AND next_deadline < $2
		 ORDER BY next_deadline ASC LIMIT $3`,
		string(pnr.StatusCancelled), deadline, limit)
}

// queryPNRs runs a query returning (state, version) rows.
func (s *Postgres) queryPNRs(ctx context.Context, sql string, args ...any) ([]*pnr.PNR, error) {
	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*pnr.PNR
	for rows.Next() {
		p, err := scanPNRRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
