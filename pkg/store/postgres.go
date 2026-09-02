package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
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

	// RetireGrace is how long after its last flight a record may be
	// retired: it sets purge_at, the partition a record lives in. Set on the
	// root store before any Node view is taken. Zero means three days.
	RetireGrace time.Duration

	// node is which system's rows this store sees. The empty string is the
	// single-tenant deployment; Node returns a view for any other.
	node string
	// shared marks a Node view, whose Close must leave the pool alone.
	shared bool
	// parts remembers which daily partitions exist, shared by every view.
	parts *partitionCache
}

// partitionCache is the set of daily partitions this process has seen or
// made, so a write does not ask the catalogue every time.
type partitionCache struct {
	mu   sync.Mutex
	have map[string]bool
}

// purgeAt is the day a record may be retired: the departure of its last air
// segment plus the grace, or a year from creation when it holds no flight.
func (s *Postgres) purgeAt(p *pnr.PNR) time.Time {
	grace := s.RetireGrace
	if grace <= 0 {
		grace = 72 * time.Hour
	}
	var last time.Time
	for _, sg := range p.Segments {
		if sg.Type == pnr.SegmentAir && sg.Depart.After(last) {
			last = sg.Depart
		}
	}
	if last.IsZero() {
		base := p.CreatedAt
		if base.IsZero() {
			base = s.now()
		}
		return base.UTC().AddDate(1, 0, 0).Truncate(24 * time.Hour)
	}
	return last.UTC().Add(grace).Truncate(24 * time.Hour)
}

// locatorHeld reports whether a system already holds a live record under a
// locator. The unique index carries the partition key, so it alone cannot
// refuse the same locator on a different retirement day; this can.
func locatorHeld(ctx context.Context, tx pgx.Tx, node, locator string) (bool, error) {
	if locator == "" {
		return false, nil
	}
	var held bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pnr WHERE node=$1 AND record_locator=$2)`, node, locator).Scan(&held)
	return held, err
}

// ensurePartitions makes sure a daily partition exists for each purge day
// given, on both partitioned tables. Creation takes an advisory lock so two
// processes writing the same first record of a day do not race the
// catalogue.
func (s *Postgres) ensurePartitions(ctx context.Context, days ...time.Time) error {
	for _, d := range days {
		day := d.UTC().Truncate(24 * time.Hour)
		key := day.Format("20060102")
		s.parts.mu.Lock()
		have := s.parts.have[key]
		s.parts.mu.Unlock()
		if have {
			continue
		}
		err := s.tx(ctx, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('jetway_pnr_partitions'))`); err != nil {
				return err
			}
			from, to := day.Format("2006-01-02"), day.AddDate(0, 0, 1).Format("2006-01-02")
			for _, table := range []string{"pnr", "pnr_event"} {
				q := fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s_p_%s PARTITION OF %s FOR VALUES FROM ('%s') TO ('%s')`,
					table, key, table, from, to)
				if _, err := tx.Exec(ctx, q); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return fmt.Errorf("store: partition for %s: %w", key, err)
		}
		s.parts.mu.Lock()
		s.parts.have[key] = true
		s.parts.mu.Unlock()
	}
	return nil
}

// Retired is what RetireBefore removed.
type Retired struct {
	Partitions int // daily partitions dropped, across both tables
	Records    int // rows deleted from the default partitions
	QueueItems int // queue items whose record is gone
}

// RetireBefore retires every record whose purge day has passed: the daily
// partitions of records and events with an upper bound at or before the
// cutoff are dropped, rows that landed in the default partitions are
// deleted, and queue items left pointing at nothing go too. It acts on the
// whole database, not one system's view: retention is the deployment's
// policy, and a day is a day for everyone in the book.
func (s *Postgres) RetireBefore(ctx context.Context, cutoff time.Time) (Retired, error) {
	var out Retired
	rows, err := s.pool.Query(ctx, `
		SELECT c.relname, pg_get_expr(c.relpartbound, c.oid)
		FROM pg_inherits i JOIN pg_class c ON c.oid = i.inhrelid
		WHERE i.inhparent IN ('pnr'::regclass, 'pnr_event'::regclass)`)
	if err != nil {
		return out, fmt.Errorf("store: retire: %w", err)
	}
	type part struct{ name string }
	var drop []string
	for rows.Next() {
		var name, bound string
		if err := rows.Scan(&name, &bound); err != nil {
			rows.Close()
			return out, err
		}
		upper, ok := partitionUpper(bound)
		if ok && !upper.After(cutoff) {
			drop = append(drop, name)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return out, err
	}
	for _, name := range drop {
		if _, err := s.pool.Exec(ctx, `DROP TABLE IF EXISTS `+pgx.Identifier{name}.Sanitize()); err != nil {
			return out, fmt.Errorf("store: retire %s: %w", name, err)
		}
		out.Partitions++
		s.parts.mu.Lock()
		delete(s.parts.have, strings.TrimPrefix(strings.TrimPrefix(name, "pnr_event_p_"), "pnr_p_"))
		s.parts.mu.Unlock()
	}
	tag, err := s.pool.Exec(ctx, `DELETE FROM pnr_default WHERE purge_at < $1`, cutoff)
	if err != nil {
		return out, fmt.Errorf("store: retire default: %w", err)
	}
	out.Records = int(tag.RowsAffected())
	if _, err := s.pool.Exec(ctx, `DELETE FROM pnr_event_default WHERE purge_at < $1`, cutoff); err != nil {
		return out, fmt.Errorf("store: retire default events: %w", err)
	}
	tag, err = s.pool.Exec(ctx, `DELETE FROM queue_item q WHERE NOT EXISTS (SELECT 1 FROM pnr p WHERE p.id = q.pnr_id)`)
	if err != nil {
		return out, fmt.Errorf("store: retire queue: %w", err)
	}
	out.QueueItems = int(tag.RowsAffected())
	// A default partition that has just been emptied is dead space until
	// vacuum gets to it -- two gigabytes of it after a conversion. Empty is
	// the moment to truncate, which gives the space back at once.
	for _, table := range []string{"pnr_default", "pnr_event_default"} {
		var n int64
		if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM `+table).Scan(&n); err == nil && n == 0 {
			if _, err := s.pool.Exec(ctx, `TRUNCATE `+table); err != nil {
				return out, fmt.Errorf("store: retire truncate %s: %w", table, err)
			}
		}
	}
	return out, nil
}

// partitionUpper reads the upper bound out of a range partition's bound
// expression: FOR VALUES FROM ('2025-11-26 00:00:00+00') TO ('2025-11-27 00:00:00+00').
func partitionUpper(bound string) (time.Time, bool) {
	i := strings.LastIndex(bound, "TO (")
	if i < 0 {
		return time.Time{}, false
	}
	v := strings.Trim(strings.TrimSuffix(bound[i+4:], ")"), "' ")
	for _, layout := range []string{"2006-01-02 15:04:05-07", "2006-01-02 15:04:05+00", "2006-01-02 15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, v); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// Node returns a view of the same database scoped to one system.
//
// A hosted platform runs many carriers' reservations systems on one book of
// record. Each gets a Node view: its rows are written with its name and its
// queries see only those, so a locator, a flight lookup or a queue listing
// on one carrier's system never surfaces another's booking. The pool is
// shared; closing a view is a no-op, and the root store's Close is the one
// that releases it.
func (s *Postgres) Node(name string) *Postgres {
	c := *s
	c.node = name
	c.shared = true
	return &c
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
	return &Postgres{pool: pool, parts: &partitionCache{have: map[string]bool{}}}, nil
}

func (s *Postgres) Close() error {
	if s.shared {
		return nil
	}
	s.pool.Close()
	return nil
}

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
		                     trace_id, span_id, node)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20)`,
		m.ID, m.Direction, m.At, m.Transport, m.Peer, m.Format, nullIfEmpty(m.Kind),
		m.Raw, m.SHA256, m.Size, m.Status, nullIfEmpty(m.Error), nullIfEmpty(m.DedupKey),
		nullIfEmpty(m.PNRID), nullIfEmpty(m.CorrelationID), diags, m.PossibleDuplicate,
		nullIfEmpty(m.TraceID), nullIfEmpty(m.SpanID), s.node)
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
		WHERE id=$1 AND node=$11`,
		m.ID, m.Status, nullIfEmpty(m.Error), nullIfEmpty(m.Kind), m.Format,
		nullIfEmpty(m.PNRID), nullIfEmpty(m.CorrelationID), nullIfEmpty(m.DedupKey), diags,
		m.PossibleDuplicate, s.node)
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
	m, err := scanMessage(s.pool.QueryRow(ctx, `SELECT `+messageColumns+` FROM message WHERE id=$1 AND node=$2`, id, s.node))
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
		  AND node=$6
		ORDER BY id DESC LIMIT $5`,
		f.Peer, f.PNRID, string(f.Status), f.SinceID, limit, s.node)
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
		WHERE peer=$1 AND dedup_key=$2 AND direction=$3 AND node=$4
		ORDER BY id ASC LIMIT 1`, peer, key, string(dir), s.node).Scan(&id)
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
	pa := s.purgeAt(p)
	if err := s.ensurePartitions(ctx, pa); err != nil {
		return err
	}
	return s.tx(ctx, func(tx pgx.Tx) error {
		if held, err := locatorHeld(ctx, tx, s.node, p.RecordLocator); err != nil {
			return err
		} else if held {
			return ErrDuplicate
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO pnr (id, record_locator, version, status, created_at, updated_at, state, next_deadline, node, purge_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
			p.ID, p.RecordLocator, p.Version, p.Status, p.CreatedAt, p.UpdatedAt, state,
			p.NextDeadline(), s.node, pa)
		if err != nil {
			if isUniqueViolation(err) {
				return ErrDuplicate
			}
			return err
		}
		return insertEvents(ctx, tx, s.node, p.ID, 0, events, s.now, pa)
	})
}

// Acquire implements Leaser: one row per system, taken when free or lapsed.
// The insert-or-update is a single statement, so two standbys racing for a
// lapsed lease cannot both win.
func (s *Postgres) Acquire(ctx context.Context, system, holder string, ttl time.Duration) (bool, error) {
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO system_lease (system, holder, expires_at, taken_at)
		VALUES ($1, $2, now() + $3::interval, now())
		ON CONFLICT (system) DO UPDATE
		   SET holder = EXCLUDED.holder, expires_at = EXCLUDED.expires_at, taken_at = now()
		 WHERE system_lease.expires_at < now() OR system_lease.holder = EXCLUDED.holder`,
		system, holder, ttl.String())
	if err != nil {
		return false, fmt.Errorf("store: acquire lease: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// Renew implements Leaser.
func (s *Postgres) Renew(ctx context.Context, system, holder string, ttl time.Duration) (bool, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE system_lease SET expires_at = now() + $3::interval
		 WHERE system = $1 AND holder = $2 AND expires_at > now()`, system, holder, ttl.String())
	if err != nil {
		return false, fmt.Errorf("store: renew lease: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// Release implements Leaser.
func (s *Postgres) Release(ctx context.Context, system, holder string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM system_lease WHERE system = $1 AND holder = $2`, system, holder)
	return err
}

// Ping implements Pinger.
func (s *Postgres) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

// RevenueByLeg implements Store with one aggregate: each priced record's
// total shared evenly across its live air segments, summed per leg.
func (s *Postgres) RevenueByLeg(ctx context.Context, wireDate string) ([]LegRevenue, error) {
	rows, err := s.pool.Query(ctx, `
		WITH live AS (
			SELECT id, (state->'pricing'->>'total')::bigint AS total,
			       (SELECT count(*) FROM jsonb_array_elements(state->'segments') x
			         WHERE x->>'type' = 'air' AND coalesce(x->>'status','') <> 'XX') AS legs,
			       state->'segments' AS segs
			FROM pnr
			WHERE node = $1 AND status <> 'cancelled' AND state ? 'pricing'
		)
		SELECT upper(coalesce(nullif(seg->>'operating_carrier',''), seg->>'carrier')), ltrim(seg->>'flight_num','0'),
		       upper(seg->>'wire_date'), upper(coalesce(seg->>'board','')), sum(total / legs)::bigint
		FROM live, jsonb_array_elements(segs) seg
		WHERE legs > 0 AND seg->>'type' = 'air' AND coalesce(seg->>'status','') <> 'XX'
		  AND ($2 = '' OR upper(seg->>'wire_date') = upper($2))
		GROUP BY 1, 2, 3, 4
		ORDER BY 1, 2, 4`, s.node, wireDate)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LegRevenue
	for rows.Next() {
		var r LegRevenue
		if err := rows.Scan(&r.Carrier, &r.FlightNum, &r.WireDate, &r.Board, &r.Cents); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SoldSeats implements Store with one aggregate over the segments of the
// node's live records; leading zeros on flight numbers are dropped so the
// two spellings carriers use count as one flight.
func (s *Postgres) SoldSeats(ctx context.Context, carrier, wireDate string) ([]SoldSeats, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT ltrim(seg->>'flight_num', '0'), upper(seg->>'wire_date'), coalesce(seg->>'board',''), coalesce(seg->>'class',''), coalesce(seg->>'status',''),
		       sum(coalesce((seg->>'seats')::int, 0))::int
		FROM pnr, jsonb_array_elements(state->'segments') seg
		WHERE node = $1 AND status <> 'cancelled'
		  AND seg->>'type' = 'air' AND seg->>'carrier' = $2 AND coalesce(seg->>'status','') <> 'XX'
		  AND ($3 = '' OR upper(seg->>'wire_date') = upper($3))
		GROUP BY 1, 2, 3, 4, 5
		ORDER BY 1, 2, 3, 4, 5`, s.node, carrier, wireDate)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SoldSeats
	for rows.Next() {
		r := SoldSeats{Carrier: carrier}
		if err := rows.Scan(&r.FlightNum, &r.WireDate, &r.Board, &r.Class, &r.Status, &r.Seats); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// LoadPNRs implements Store with COPY: one stream for the records, one for
// their events, in one transaction, so a batch lands whole or not at all.
// Through a transaction-pooling proxy COPY is fine -- it is one statement
// inside the transaction the pool already pins to a connection.
func (s *Postgres) LoadPNRs(ctx context.Context, recs []*pnr.PNR, actor string) error {
	if len(recs) == 0 {
		return nil
	}
	now := s.now()
	rows := make([][]any, 0, len(recs))
	events := make([][]any, 0, len(recs))
	days := map[time.Time]bool{}
	for _, p := range recs {
		if p.ID == "" {
			p.ID = ulid.New()
		}
		p.Version = 1
		if p.CreatedAt.IsZero() {
			p.CreatedAt = now
		}
		if p.UpdatedAt.IsZero() {
			p.UpdatedAt = p.CreatedAt
		}
		state, err := json.Marshal(p)
		if err != nil {
			return err
		}
		pa := s.purgeAt(p)
		days[pa.UTC().Truncate(24*time.Hour)] = true
		rows = append(rows, []any{p.ID, p.RecordLocator, p.Version, string(p.Status), p.CreatedAt, p.UpdatedAt, state, p.NextDeadline(), s.node, pa})
		events = append(events, []any{ulid.New(), p.ID, int64(1), "loaded", nil, nil, nil, nullIfEmpty(actor), p.CreatedAt, s.node, pa})
	}
	dayList := make([]time.Time, 0, len(days))
	for d := range days {
		dayList = append(dayList, d)
	}
	if err := s.ensurePartitions(ctx, dayList...); err != nil {
		return err
	}
	locators := make([]string, 0, len(recs))
	for _, p := range recs {
		if p.RecordLocator != "" {
			locators = append(locators, p.RecordLocator)
		}
	}
	return s.tx(ctx, func(tx pgx.Tx) error {
		var held bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pnr WHERE node=$1 AND record_locator = ANY($2))`, s.node, locators).Scan(&held); err != nil {
			return err
		}
		if held {
			return ErrDuplicate
		}
		_, err := tx.CopyFrom(ctx, pgx.Identifier{"pnr"},
			[]string{"id", "record_locator", "version", "status", "created_at", "updated_at", "state", "next_deadline", "node", "purge_at"},
			pgx.CopyFromRows(rows))
		if err != nil {
			if isUniqueViolation(err) {
				return ErrDuplicate
			}
			return err
		}
		_, err = tx.CopyFrom(ctx, pgx.Identifier{"pnr_event"},
			[]string{"id", "pnr_id", "seq", "type", "detail", "payload", "message_id", "actor", "at", "node", "purge_at"},
			pgx.CopyFromRows(events))
		return err
	})
}

func (s *Postgres) UpdatePNR(ctx context.Context, p *pnr.PNR, expected int64, events []Event) error {
	p.Version = expected + 1
	state, err := json.Marshal(p)
	if err != nil {
		return err
	}
	pa := s.purgeAt(p)
	if err := s.ensurePartitions(ctx, pa); err != nil {
		return err
	}
	return s.tx(ctx, func(tx pgx.Tx) error {
		// The version predicate is the whole point: if another writer got here
		// first, this update affects no rows and the caller must re-read.
		// A changed purge day moves the row to its new partition.
		tag, err := tx.Exec(ctx, `
			UPDATE pnr SET version=$2, status=$3, updated_at=$4, state=$5, record_locator=$6,
			               next_deadline=$8, purge_at=$10
			WHERE id=$1 AND version=$7 AND node=$9`,
			p.ID, p.Version, p.Status, p.UpdatedAt, state, p.RecordLocator, expected,
			p.NextDeadline(), s.node, pa)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			var exists bool
			if err := tx.QueryRow(ctx, `SELECT true FROM pnr WHERE id=$1 AND node=$2`, p.ID, s.node).Scan(&exists); err != nil {
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
		return insertEvents(ctx, tx, s.node, p.ID, maxSeq, events, s.now, pa)
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

	parentPA, childPA := s.purgeAt(parent), s.purgeAt(child)
	if err := s.ensurePartitions(ctx, parentPA, childPA); err != nil {
		return err
	}
	return s.tx(ctx, func(tx pgx.Tx) error {
		// The parent goes first, so a losing writer rolls back before the
		// child locator is consumed. Creating the child first and failing here
		// is the torn write this method exists to make impossible.
		tag, err := tx.Exec(ctx, `
			UPDATE pnr SET version=$2, status=$3, updated_at=$4, state=$5, record_locator=$6,
			               next_deadline=$8, purge_at=$10
			WHERE id=$1 AND version=$7 AND node=$9`,
			parent.ID, parent.Version, parent.Status, parent.UpdatedAt, parentState,
			parent.RecordLocator, expected, parent.NextDeadline(), s.node, parentPA)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			var exists bool
			if err := tx.QueryRow(ctx, `SELECT true FROM pnr WHERE id=$1 AND node=$2`, parent.ID, s.node).Scan(&exists); err != nil {
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
		if err := insertEvents(ctx, tx, s.node, parent.ID, maxSeq, parentEvents, s.now, parentPA); err != nil {
			return err
		}

		if held, err := locatorHeld(ctx, tx, s.node, child.RecordLocator); err != nil {
			return err
		} else if held {
			return ErrDuplicate
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO pnr (id, record_locator, version, status, created_at, updated_at, state, next_deadline, node, purge_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
			child.ID, child.RecordLocator, child.Version, child.Status,
			child.CreatedAt, child.UpdatedAt, childState, child.NextDeadline(), s.node, childPA); err != nil {
			if isUniqueViolation(err) {
				return ErrDuplicate
			}
			return err
		}
		return insertEvents(ctx, tx, s.node, child.ID, 0, childEvents, s.now, childPA)
	})
}

func insertEvents(ctx context.Context, tx pgx.Tx, node, pnrID string, startSeq int64, events []Event, now func() time.Time, purgeAt time.Time) error {
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
			INSERT INTO pnr_event (id, pnr_id, seq, type, detail, payload, message_id, actor, at, node, purge_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
			e.ID, pnrID, startSeq, e.Type, nullIfEmpty(e.Detail), jsonOrNil(e.Payload),
			nullIfEmpty(e.MessageID), nullIfEmpty(e.Actor), e.At, node, purgeAt)
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
	err := s.pool.QueryRow(ctx, `SELECT state, version FROM pnr WHERE node=$2 AND `+where+` ORDER BY purge_at DESC LIMIT 1`, arg, s.node).Scan(&state, &version)
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
		`SELECT state, version FROM pnr WHERE node=$2 ORDER BY updated_at DESC LIMIT $1`, limit, s.node)
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
		                        message_id, segment_ref, placed_at, placed_by, node)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		item.ID, item.Queue, item.PNRID, nullIfEmpty(item.Locator), item.Code, item.Reason,
		nullIfEmpty(item.MessageID), item.SegmentRef, item.PlacedAt, item.PlacedBy, s.node)
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
		WHERE id=$1 AND worked_at IS NULL AND node=$4`, id, by, nullIfEmpty(note), s.node)
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
		`SELECT true FROM queue_item WHERE id=$1 AND node=$2`, id, s.node).Scan(&exists); err != nil {
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
		  AND node=$5
		ORDER BY id DESC LIMIT $4`,
		f.Queue, f.PNRID, f.IncludeWorked, limit, s.node)
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
		SELECT queue, count(*) FROM queue_item WHERE worked_at IS NULL AND node=$1 GROUP BY queue`, s.node)
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
		`SELECT state, version FROM pnr WHERE node=$2 AND state @> $1::jsonb ORDER BY updated_at DESC LIMIT 1`,
		contains, s.node)
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
	return s.findOnFlight(ctx, flightKey, wireDate, limit, false)
}

// FindPNRsEverOnFlight implements Store.
func (s *Postgres) FindPNRsEverOnFlight(ctx context.Context, flightKey, wireDate string, limit int) ([]*pnr.PNR, error) {
	return s.findOnFlight(ctx, flightKey, wireDate, limit, true)
}

func (s *Postgres) findOnFlight(ctx context.Context, flightKey, wireDate string, limit int, ever bool) ([]*pnr.PNR, error) {
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
		`SELECT state, version FROM pnr WHERE (%s) AND (status <> $%d OR $%d) AND node=$%d
		 ORDER BY updated_at DESC, id DESC LIMIT $%d OFFSET $%d`,
		where.String(), np+1, np+2, np+3, np+4, np+5)

	const page = 500
	var out []*pnr.PNR
	for offset := 0; len(out) < limit; offset += page {
		rows, err := s.pool.Query(ctx, sql, append(append([]any{}, args...),
			string(pnr.StatusCancelled), ever, s.node, page, offset)...)
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
			match := pnrOnFlight
			if ever {
				match = pnrEverOnFlight
			}
			if match(p, flightKey, wireDate) {
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
		 WHERE status <> $1 AND updated_at < $2 AND node=$4
		 ORDER BY updated_at ASC LIMIT $3`,
		string(pnr.StatusCancelled), before, limit, s.node)
}

func (s *Postgres) FindPNRsDueBy(ctx context.Context, deadline time.Time, limit int) ([]*pnr.PNR, error) {
	if limit <= 0 {
		limit = 10000
	}
	// next_deadline is maintained on write because a deadline buried in the
	// JSONB state cannot be range-scanned. Served by pnr_deadline_idx.
	return s.queryPNRs(ctx,
		`SELECT state, version FROM pnr
		 WHERE status <> $1 AND next_deadline IS NOT NULL AND next_deadline < $2 AND node=$4
		 ORDER BY next_deadline ASC LIMIT $3`,
		string(pnr.StatusCancelled), deadline, limit, s.node)
}

// Purge implements Store. Events and queue items go with their records
// through the foreign keys; what this deletes directly is bounded by node,
// so one system's retention never touches another's history.
func (s *Postgres) Purge(ctx context.Context, before time.Time) (Purged, error) {
	var out Purged
	// Queue items are counted before the cascade removes them.
	if err := s.pool.QueryRow(ctx, `
		SELECT count(*) FROM queue_item q JOIN pnr p ON p.id = q.pnr_id
		WHERE p.node=$1 AND p.updated_at < $2`, s.node, before).Scan(&out.QueueItems); err != nil {
		return out, fmt.Errorf("store: purge: %w", err)
	}
	tag, err := s.pool.Exec(ctx, `DELETE FROM pnr WHERE node=$1 AND updated_at < $2`, s.node, before)
	if err != nil {
		return out, fmt.Errorf("store: purge records: %w", err)
	}
	out.Records = int(tag.RowsAffected())
	// Events and queue items are no longer tied to their record by a foreign
	// key -- the record lives in a partition -- so they follow it here.
	if _, err := s.pool.Exec(ctx, `DELETE FROM pnr_event e WHERE e.node=$1 AND NOT EXISTS (SELECT 1 FROM pnr p WHERE p.id = e.pnr_id)`, s.node); err != nil {
		return out, fmt.Errorf("store: purge events: %w", err)
	}
	if _, err := s.pool.Exec(ctx, `DELETE FROM queue_item q WHERE q.node=$1 AND NOT EXISTS (SELECT 1 FROM pnr p WHERE p.id = q.pnr_id)`, s.node); err != nil {
		return out, fmt.Errorf("store: purge queue: %w", err)
	}
	tag, err = s.pool.Exec(ctx, `DELETE FROM message WHERE node=$1 AND at < $2`, s.node, before)
	if err != nil {
		return out, fmt.Errorf("store: purge messages: %w", err)
	}
	out.Messages = int(tag.RowsAffected())
	return out, nil
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
