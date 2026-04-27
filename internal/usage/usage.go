package usage

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Record struct {
	KeyID         uuid.UUID
	RequestID     string
	Endpoint      string
	Model         string
	Streaming     bool
	InputTokens   int
	OutputTokens  int
	CachedTokens  int
	CostUSD       float64
	Status        int
	Duration      time.Duration
	ErrorMessage  string
}

type Recorder struct {
	db *pgxpool.Pool
}

func NewRecorder(db *pgxpool.Pool) *Recorder {
	return &Recorder{db: db}
}

func (r *Recorder) Record(ctx context.Context, rec Record) error {
	_, err := r.db.Exec(ctx, `
		INSERT INTO usage_records (
			key_id, request_id, endpoint, model, streaming,
			input_tokens, output_tokens, cached_tokens,
			cost_usd, status, duration_ms, error_message
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
	`,
		rec.KeyID, nullableText(rec.RequestID), rec.Endpoint, nullableText(rec.Model), rec.Streaming,
		rec.InputTokens, rec.OutputTokens, rec.CachedTokens,
		rec.CostUSD, rec.Status, int(rec.Duration.Milliseconds()), nullableText(rec.ErrorMessage),
	)
	return err
}

func nullableText(s string) any {
	if s == "" {
		return nil
	}
	return s
}
