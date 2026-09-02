package store

import (
	"context"
	"embed"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// migration is one numbered schema change.
type migration struct {
	Version int
	Name    string
	SQL     string
}

// Migrations returns the embedded schema changes in version order.
func Migrations() ([]migration, error) {
	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		return nil, err
	}
	var out []migration
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		num, _, ok := strings.Cut(e.Name(), "_")
		if !ok {
			return nil, fmt.Errorf("store: migration %q is not named <version>_<name>.sql", e.Name())
		}
		v, err := strconv.Atoi(num)
		if err != nil {
			return nil, fmt.Errorf("store: migration %q has a non-numeric version: %w", e.Name(), err)
		}
		b, err := migrationFS.ReadFile("migrations/" + e.Name())
		if err != nil {
			return nil, err
		}
		out = append(out, migration{Version: v, Name: e.Name(), SQL: string(b)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	for i := range out {
		if out[i].Version != i+1 {
			return nil, fmt.Errorf("store: migration versions must be dense from 1; found %d at position %d",
				out[i].Version, i+1)
		}
	}
	return out, nil
}

// MigrateSchema applies any migrations the database has not seen.
//
// Each migration runs inside its own transaction together with the row that
// records it, so a failure part-way leaves the database at a version that
// actually reflects its contents. Applying is idempotent, which is what makes
// running it on every start safe and removes a manual deployment step.
func MigrateSchema(ctx context.Context, s *Postgres) error { return migrateThrough(ctx, s, 0) }

// migrateThrough applies migrations up to and including a version; zero
// means all of them. Tests use it to stand a database at an older schema.
func migrateThrough(ctx context.Context, s *Postgres, through int) error {
	if _, err := s.pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migration (
			version    int PRIMARY KEY,
			name       text NOT NULL,
			applied_at timestamptz NOT NULL DEFAULT now()
		)`); err != nil {
		return fmt.Errorf("store: create migration table: %w", err)
	}

	applied := map[int]bool{}
	rows, err := s.pool.Query(ctx, `SELECT version FROM schema_migration`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			rows.Close()
			return err
		}
		applied[v] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	ms, err := Migrations()
	if err != nil {
		return err
	}
	for _, m := range ms {
		if applied[m.Version] {
			continue
		}
		if through > 0 && m.Version > through {
			break
		}
		err := s.tx(ctx, func(tx pgx.Tx) error {
			// Two processes booting against one database take turns: the
			// lock serialises them and the re-check inside it makes the
			// second one find the work done.
			if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('jetway_schema_migration'))`); err != nil {
				return err
			}
			var done bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM schema_migration WHERE version=$1)`, m.Version).Scan(&done); err != nil {
				return err
			}
			if done {
				return nil
			}
			if _, err := tx.Exec(ctx, m.SQL); err != nil {
				return err
			}
			_, err := tx.Exec(ctx,
				`INSERT INTO schema_migration (version, name) VALUES ($1,$2)`, m.Version, m.Name)
			return err
		})
		if err != nil {
			return fmt.Errorf("store: apply migration %s: %w", m.Name, err)
		}
	}
	return nil
}

// SchemaSQL returns every migration concatenated, for inspection and for
// bootstrapping a database by hand.
func SchemaSQL() (string, error) {
	ms, err := Migrations()
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, m := range ms {
		fmt.Fprintf(&b, "-- %s\n%s\n", m.Name, m.SQL)
	}
	return b.String(), nil
}
