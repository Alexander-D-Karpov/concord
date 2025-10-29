package db

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"go.uber.org/zap"
)

type queryInfo struct {
	SQL  string
	Args []interface{}
}

type contextKeyType string

const (
	queryInfoKey  contextKeyType = "query_info"
	queryStartKey contextKeyType = "query_start"
)

type SlowQueryLogger struct {
	logger    *zap.Logger
	threshold time.Duration
}

func NewSlowQueryLogger(logger *zap.Logger, threshold time.Duration) *SlowQueryLogger {
	return &SlowQueryLogger{
		logger:    logger,
		threshold: threshold,
	}
}

func (s *SlowQueryLogger) TraceQueryStart(ctx context.Context, conn *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	info := queryInfo{
		SQL:  data.SQL,
		Args: data.Args,
	}
	ctx = context.WithValue(ctx, queryInfoKey, info)
	ctx = context.WithValue(ctx, queryStartKey, time.Now())
	return ctx
}

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
