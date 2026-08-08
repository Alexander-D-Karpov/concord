package db

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"go.uber.org/zap"
)

// queryInfo carries the SQL text and arguments of an in-flight query from
// TraceQueryStart to TraceQueryEnd via the context.
type queryInfo struct {
	SQL  string
	Args []interface{}
}

// contextKeyType is a private context-key type, avoiding collisions with keys
// defined in other packages.
type contextKeyType string

const (
	// queryInfoKey holds the queryInfo for the current query in the context.
	queryInfoKey contextKeyType = "query_info"
	// queryStartKey holds the query start time (time.Time) in the context.
	queryStartKey contextKeyType = "query_start"
)

// SlowQueryLogger is a pgx QueryTracer that warns whenever a query's execution
// time exceeds threshold, logging the SQL and its arguments.
type SlowQueryLogger struct {
	logger    *zap.Logger
	threshold time.Duration
}

// NewSlowQueryLogger returns a tracer that logs queries slower than threshold.
func NewSlowQueryLogger(logger *zap.Logger, threshold time.Duration) *SlowQueryLogger {
	return &SlowQueryLogger{
		logger:    logger,
		threshold: threshold,
	}
}

// TraceQueryStart records the SQL, args, and current time into the returned
// context so TraceQueryEnd can measure the query's duration. Part of the pgx
// QueryTracer interface.
func (s *SlowQueryLogger) TraceQueryStart(ctx context.Context, conn *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	info := queryInfo{
		SQL:  data.SQL,
		Args: data.Args,
	}
	ctx = context.WithValue(ctx, queryInfoKey, info)
	ctx = context.WithValue(ctx, queryStartKey, time.Now())
	return ctx
}

// TraceQueryEnd computes elapsed time from the start recorded by
// TraceQueryStart and, if it exceeds the threshold, logs a warning with the SQL
// and arguments. It returns silently when no start time is present in the
// context. Part of the pgx QueryTracer interface.
func (s *SlowQueryLogger) TraceQueryEnd(ctx context.Context, conn *pgx.Conn, data pgx.TraceQueryEndData) {
	start, ok := ctx.Value(queryStartKey).(time.Time)
	if !ok {
		return
	}

	duration := time.Since(start)
	if duration > s.threshold {
		info, ok := ctx.Value(queryInfoKey).(queryInfo)
		if !ok {
			s.logger.Warn("slow query detected",
				zap.Duration("duration", duration),
			)
			return
		}

		s.logger.Warn("slow query detected",
			zap.Duration("duration", duration),
			zap.String("sql", info.SQL),
			zap.Any("args", info.Args),
		)
	}
}
