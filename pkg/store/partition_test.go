package store

import (
	"context"
	"net/url"
	"os"
	"testing"
)

// The message log is the table that grows without bound, and dropping a
// month should be dropping a partition, not deleting a hundred million rows.
// Migration 0006 rebuilds an empty message table partitioned by time with a
// default partition to catch everything until monthly partitions are cut.
// A table that already holds data is left alone with a notice -- a migration
// must never quietly rewrite somebody's evidence.
func TestMessageLogIsPartitioned(t *testing.T) {
	dsn := os.Getenv("JETWAY_TEST_DSN")
	if dsn == "" {
		t.Skip("JETWAY_TEST_DSN not set")
	}
	ctx := context.Background()
	pg := freshDatabase(t, dsn, "jetway_partition_test")
	if err := MigrateSchema(ctx, pg); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	var relkind string
	if err := pg.pool.QueryRow(ctx,
		`SELECT relkind FROM pg_class WHERE relname = 'message'`).Scan(&relkind); err != nil {
		t.Fatal(err)
	}
	if relkind != "p" {
		t.Fatalf("message relkind = %q, want 'p' (partitioned)", relkind)
	}
	var parts int
	if err := pg.pool.QueryRow(ctx,
		`SELECT count(*) FROM pg_inherits WHERE inhparent = 'message'::regclass`).Scan(&parts); err != nil {
		t.Fatal(err)
	}
	if parts < 1 {
		t.Fatal("the partitioned message table has no default partition; writes would fail")
	}

	// And the store still works on top of it, dedup lookup included.
	m := newMsg("BA", []byte("ZCZC PARTITION TEST NNNN"))
	m.DedupKey = "part:1"
	if err := pg.AppendMessage(ctx, m); err != nil {
		t.Fatalf("AppendMessage onto the partitioned table: %v", err)
	}
	got, err := pg.GetMessage(ctx, m.ID)
	if err != nil || got == nil {
		t.Fatalf("GetMessage: %v %v", got, err)
	}
	id, ok, err := pg.FindByDedupKey(ctx, "BA", "part:1")
	if err != nil || !ok || id == "" {
		t.Fatalf("FindByDedupKey on the partitioned table: %q %v %v", id, ok, err)
	}
}

// A log that already holds data is left alone with a notice: the skip is the
// migration's promise never to rewrite somebody's evidence. Everything up to
// 0005 is applied, a message is written, and 0006 must then change nothing.
func TestPartitioningSkipsAPopulatedLog(t *testing.T) {
	dsn := os.Getenv("JETWAY_TEST_DSN")
	if dsn == "" {
		t.Skip("JETWAY_TEST_DSN not set")
	}
	ctx := context.Background()
	pg := freshDatabase(t, dsn, "jetway_partition_skip_test")

	ms, err := Migrations()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pg.pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migration (
			version int PRIMARY KEY, name text NOT NULL,
			applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		t.Fatal(err)
	}
	for _, m := range ms {
		if m.Version >= 6 {
			break
		}
		if _, err := pg.pool.Exec(ctx, m.SQL); err != nil {
			t.Fatalf("apply %s: %v", m.Name, err)
		}
		if _, err := pg.pool.Exec(ctx,
			`INSERT INTO schema_migration (version, name) VALUES ($1,$2)`, m.Version, m.Name); err != nil {
			t.Fatal(err)
		}
	}
	// Written the way a pre-0007 deployment wrote it: no node column yet.
	// Going through AppendMessage would assume today's schema, and the
	// point is that yesterday's evidence survives today's migration.
	msg := newMsg("BA", []byte("ZCZC EVIDENCE NNNN"))
	if _, err := pg.pool.Exec(ctx, `
		INSERT INTO message (id, direction, at, transport, peer, format, raw, sha256, size_bytes, status, diagnostics)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,'[]'::jsonb)`,
		msg.ID, msg.Direction, msg.At, msg.Transport, msg.Peer, msg.Format, msg.Raw, msg.SHA256, msg.Size, msg.Status); err != nil {
		t.Fatal(err)
	}

	if err := MigrateSchema(ctx, pg); err != nil {
		t.Fatalf("migrate onto a populated log: %v", err)
	}
	var relkind string
	if err := pg.pool.QueryRow(ctx,
		`SELECT relkind FROM pg_class WHERE relname = 'message'`).Scan(&relkind); err != nil {
		t.Fatal(err)
	}
	if relkind != "r" {
		t.Fatalf("relkind = %q; the migration rebuilt a table that held evidence", relkind)
	}
	got, err := pg.GetMessage(ctx, msg.ID)
	if err != nil || got == nil {
		t.Fatalf("the evidence is gone: %v %v", got, err)
	}
}

// freshDatabase creates (or recreates) a scratch database next to the test
// DSN's and returns a store connected to it.
func freshDatabase(t *testing.T, dsn, name string) *Postgres {
	t.Helper()
	ctx := context.Background()
	admin, err := OpenPostgres(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	if _, err := admin.pool.Exec(ctx, "DROP DATABASE IF EXISTS "+name); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.pool.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	pg, err := OpenPostgres(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pg.Close() })
	return pg
}
