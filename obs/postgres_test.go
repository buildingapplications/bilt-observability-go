package obs

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
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
