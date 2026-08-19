package obs

import (
	"context"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel"
)

func TestSqlcSpanName(t *testing.T) {
	cases := []struct {
		name string
		stmt string
		want string
	}{
		{
			name: "sqlc header",
			stmt: "-- name: CreatePinnedFrame :one\nINSERT INTO figma_project_pinned_frames (\n    project_id\n) VALUES ($1)\n",
			want: "CreatePinnedFrame",
		},
		{
			name: "sqlc header with leading blank line",
			stmt: "\n-- name: GetFile :many\nSELECT 1\n",
			want: "GetFile",
		},
		{
			name: "hand-written statement",
			stmt: "SELECT id FROM figma_files WHERE bilt_file_id = $1",
			want: "SELECT",
		},
		{
			name: "leading comment that is not a sqlc header",
			stmt: "-- keep in sync with the migration\nupdate figma_files set name = $1",
			want: "UPDATE",
		},
		{
			name: "empty",
			stmt: "",
			want: "UNKNOWN",
		},
		{
			name: "comments only",
			stmt: "-- nothing here\n\n",
			want: "UNKNOWN",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := sqlcSpanName(context.Background(), c.stmt); got != c.want {
				t.Errorf("got %q want %q", got, c.want)
			}
		})
	}
}

// The pool reads the acquire tracer off ConnConfig.Tracer by type assertion, so
// a tracer that stopped satisfying pgxpool.AcquireTracer would silently lose the
// acquire span rather than fail to compile.
func TestInstrumentPgxPoolSetsAcquireTracer(t *testing.T) {
	cfg, err := pgxpool.ParseConfig("postgres://u:p@127.0.0.1:5432/db")
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	InstrumentPgxPool(cfg)

	if cfg.ConnConfig.Tracer == nil {
		t.Fatal("query tracer not set")
	}
	if _, ok := cfg.ConnConfig.Tracer.(pgxpool.AcquireTracer); !ok {
		t.Error("tracer does not satisfy pgxpool.AcquireTracer")
	}
}

// Naming spans after the sqlc query takes both tracer options, and dropping
// either one leaves every other test here passing.
func TestInstrumentPgxPoolNamesSpansAfterTheQuery(t *testing.T) {
	exporter := newTestTracer(t)

	cfg, err := pgxpool.ParseConfig("postgres://u:p@127.0.0.1:5432/db")
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	InstrumentPgxPool(cfg)

	tracer, ok := cfg.ConnConfig.Tracer.(pgx.QueryTracer)
	if !ok {
		t.Fatal("tracer does not trace queries")
	}

	ctx, parent := otel.Tracer("test").Start(context.Background(), "parent")
	ctx = tracer.TraceQueryStart(ctx, nil, pgx.TraceQueryStartData{
		SQL: "-- name: CreatePinnedFrame :one\nINSERT INTO figma_project_pinned_frames (project_id) VALUES ($1)\n",
	})
	tracer.TraceQueryEnd(ctx, nil, pgx.TraceQueryEndData{})
	parent.End()

	var names []string
	for _, s := range exporter.GetSpans() {
		names = append(names, s.Name)
	}
	if !slices.Contains(names, "query CreatePinnedFrame") {
		t.Errorf("span names = %v, want one named %q", names, "query CreatePinnedFrame")
	}
}
