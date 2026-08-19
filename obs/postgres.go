package obs

import (
	"context"
	"strings"

	"github.com/exaring/otelpgx"
	"github.com/jackc/pgx/v5/pgxpool"
)

// InstrumentPgxPool adds query, connect and pool-acquire spans to cfg. Call it
// before pgxpool.NewWithConfig — the pool reads the tracer once, at construction.
//
// Pool-stat metrics are deliberately left out: otelpgx.RecordStats reports
// idle/total/acquired connections as UpDownCounters, and Init exports delta
// temporality, which turns each of those levels into a net change.
func InstrumentPgxPool(cfg *pgxpool.Config) {
	// The pool type-asserts ConnConfig.Tracer for its acquire hooks, so this one
	// assignment also produces the acquire span — the only span covering the
	// pool's liveness ping, which is issued down in pgconn where no tracer runs.
	cfg.ConnConfig.Tracer = otelpgx.NewTracer(
		// Without this the span name is the whole SQL text; the name func on its
		// own only feeds db.operation.name.
		otelpgx.WithTrimSQLInSpanName(),
		otelpgx.WithSpanNameCtxFunc(sqlcSpanName),
	)
}

// sqlcSpanName reads the query name off sqlc's `-- name: Foo :one` header. The
// otelpgx default names the span after the whole SQL text, and its own trim
// returns "--" for a sqlc statement.
func sqlcSpanName(_ context.Context, stmt string) string {
	for line := range strings.Lines(stmt) {
		line = strings.TrimSpace(line)
		if name, ok := strings.CutPrefix(line, "-- name: "); ok {
			for field := range strings.FieldsSeq(name) {
				return field
			}
		}
		if line == "" || strings.HasPrefix(line, "--") {
			continue
		}
		for field := range strings.FieldsSeq(line) {
			return strings.ToUpper(field)
		}
	}
	return "UNKNOWN"
}
