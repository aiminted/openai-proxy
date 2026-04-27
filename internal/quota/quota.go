package quota

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

type Tracker struct {
	db  *pgxpool.Pool
	rdb *redis.Client
}

func New(db *pgxpool.Pool, rdb *redis.Client) *Tracker {
	return &Tracker{db: db, rdb: rdb}
}

type Totals struct {
	Tokens  int64
	CostUSD float64
}

func (t *Tracker) Get(ctx context.Context, keyID uuid.UUID) (Totals, error) {
	tokKey := "qt:tok:" + keyID.String()
	costKey := "qt:cost:" + keyID.String()

	var tot Totals
	if v, err := t.rdb.Get(ctx, tokKey).Int64(); err == nil {
		tot.Tokens = v
		if c, err := t.rdb.Get(ctx, costKey).Float64(); err == nil {
			tot.CostUSD = c
			return tot, nil
		}
	}

	row := t.db.QueryRow(ctx, `SELECT total_tokens, total_cost_usd FROM quota_totals WHERE key_id = $1`, keyID)
	if err := row.Scan(&tot.Tokens, &tot.CostUSD); err != nil {
		return tot, err
	}
	_ = t.rdb.Set(ctx, tokKey, tot.Tokens, 0).Err()
	_ = t.rdb.Set(ctx, costKey, fmt.Sprintf("%f", tot.CostUSD), 0).Err()
	return tot, nil
}

// Allow checks whether the key is below its quotas. Either limit being nil means unlimited for that dimension.
func (t *Tracker) Allow(ctx context.Context, keyID uuid.UUID, tokenQuota *int64, dollarQuota *float64) (bool, error) {
	if tokenQuota == nil && dollarQuota == nil {
		return true, nil
	}
	tot, err := t.Get(ctx, keyID)
	if err != nil {
		return false, err
	}
	if tokenQuota != nil && tot.Tokens >= *tokenQuota {
		return false, nil
	}
	if dollarQuota != nil && tot.CostUSD >= *dollarQuota {
		return false, nil
	}
	return true, nil
}

func (t *Tracker) Add(ctx context.Context, keyID uuid.UUID, tokens int64, costUSD float64) error {
	if tokens == 0 && costUSD == 0 {
		return nil
	}
	_, err := t.db.Exec(ctx, `
		INSERT INTO quota_totals (key_id, total_tokens, total_cost_usd, updated_at)
		VALUES ($1, $2, $3, now())
		ON CONFLICT (key_id) DO UPDATE
		SET total_tokens   = quota_totals.total_tokens + EXCLUDED.total_tokens,
		    total_cost_usd = quota_totals.total_cost_usd + EXCLUDED.total_cost_usd,
		    updated_at = now()
	`, keyID, tokens, costUSD)
	if err != nil {
		return err
	}
	_ = t.rdb.IncrBy(ctx, "qt:tok:"+keyID.String(), tokens).Err()
	_ = t.rdb.IncrByFloat(ctx, "qt:cost:"+keyID.String(), costUSD).Err()
	return nil
}
