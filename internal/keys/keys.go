// Package keys is the data layer: everything that hangs off an API key —
// issuance, verification, listing, the per-minute rate limit counter, the
// running token/dollar quota, and the per-request usage log. They all share
// the same Postgres + Redis backend, and pretending each is a separate
// concern just multiplied packages without earning anything.
package keys

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

var (
	ErrNotFound = errors.New("key not found")
	ErrInvalid  = errors.New("invalid key")
	ErrInactive = errors.New("key inactive")
	ErrExpired  = errors.New("key expired")
)

// ---------------------------------------------------------------------------
// types
// ---------------------------------------------------------------------------

type Key struct {
	ID          uuid.UUID
	Prefix      string
	Owner       string
	Note        string
	ExpiresAt   *time.Time
	RPMLimit    *int
	TokenQuota  *int64
	DollarQuota *float64
	Active      bool
	CreatedAt   time.Time
	LastUsedAt  *time.Time
}

type KeyWithUsage struct {
	Key
	TotalTokens  int64
	TotalCostUSD float64
}

type IssueParams struct {
	Owner       string
	Note        string
	ExpiresAt   *time.Time
	RPMLimit    *int
	TokenQuota  *int64
	DollarQuota *float64
}

type IssuedKey struct {
	Key   Key
	Plain string
}

type Stats struct {
	TotalKeys    int64
	ActiveKeys   int64
	TodayTokens  int64
	TodayCostUSD float64
}

// UsageRecord is one line in the per-request log.
type UsageRecord struct {
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

type Service struct {
	db       *pgxpool.Pool
	rdb      *redis.Client
	prefix   string
	cacheTTL time.Duration
}

func NewService(db *pgxpool.Pool, rdb *redis.Client, keyPrefix string, cacheTTL time.Duration) *Service {
	return &Service{db: db, rdb: rdb, prefix: keyPrefix, cacheTTL: cacheTTL}
}

// ---------------------------------------------------------------------------
// issue / verify
// ---------------------------------------------------------------------------

func (s *Service) Issue(ctx context.Context, p IssueParams) (*IssuedKey, error) {
	if strings.TrimSpace(p.Owner) == "" {
		return nil, errors.New("owner required")
	}

	random := make([]byte, 24)
	if _, err := rand.Read(random); err != nil {
		return nil, err
	}
	plain := s.prefix + base64.RawURLEncoding.EncodeToString(random)
	prefix := s.prefixOf(plain)

	hash, err := hashKey(plain)
	if err != nil {
		return nil, err
	}

	id := uuid.New()
	if _, err = s.db.Exec(ctx, `
		INSERT INTO api_keys (id, prefix, hash, owner, note, expires_at, rpm_limit, token_quota, dollar_quota, active)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, TRUE)
	`, id, prefix, hash, p.Owner, p.Note, p.ExpiresAt, p.RPMLimit, p.TokenQuota, p.DollarQuota); err != nil {
		return nil, fmt.Errorf("insert key: %w", err)
	}
	_, _ = s.db.Exec(ctx, `INSERT INTO quota_totals (key_id) VALUES ($1) ON CONFLICT DO NOTHING`, id)

	return &IssuedKey{
		Plain: plain,
		Key: Key{
			ID: id, Prefix: prefix, Owner: p.Owner, Note: p.Note,
			ExpiresAt: p.ExpiresAt, RPMLimit: p.RPMLimit,
			TokenQuota: p.TokenQuota, DollarQuota: p.DollarQuota,
			Active: true, CreatedAt: time.Now(),
		},
	}, nil
}

func (s *Service) Verify(ctx context.Context, plain string) (*Key, error) {
	if !strings.HasPrefix(plain, s.prefix) {
		return nil, ErrInvalid
	}
	prefix := s.prefixOf(plain)
	cacheKey := "verify:" + prefix
	if buf, err := s.rdb.Get(ctx, cacheKey).Bytes(); err == nil {
		var entry cacheEntry
		if json.Unmarshal(buf, &entry) == nil &&
			subtle.ConstantTimeCompare([]byte(entry.Plain), []byte(plain)) == 1 {
			return entry.Key, validateKey(entry.Key)
		}
	}

	row := s.db.QueryRow(ctx, `
		SELECT id, prefix, hash, owner, note, expires_at, rpm_limit, token_quota, dollar_quota, active, created_at, last_used_at
		FROM api_keys WHERE prefix = $1
	`, prefix)
	var k Key
	var hash string
	if err := row.Scan(&k.ID, &k.Prefix, &hash, &k.Owner, &k.Note, &k.ExpiresAt, &k.RPMLimit, &k.TokenQuota, &k.DollarQuota, &k.Active, &k.CreatedAt, &k.LastUsedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if !verifyHash(plain, hash) {
		return nil, ErrInvalid
	}
	if buf, err := json.Marshal(cacheEntry{Key: &k, Plain: plain}); err == nil {
		_ = s.rdb.Set(ctx, cacheKey, buf, s.cacheTTL).Err()
	}
	return &k, validateKey(&k)
}

func (s *Service) TouchLastUsed(ctx context.Context, id uuid.UUID) {
	_, _ = s.db.Exec(ctx, `UPDATE api_keys SET last_used_at = now() WHERE id = $1`, id)
}

// ---------------------------------------------------------------------------
// CRUD
// ---------------------------------------------------------------------------

func (s *Service) List(ctx context.Context) ([]KeyWithUsage, error) {
	rows, err := s.db.Query(ctx, listKeysSQL)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []KeyWithUsage
	for rows.Next() {
		var k KeyWithUsage
		if err := rows.Scan(&k.ID, &k.Prefix, &k.Owner, &k.Note, &k.ExpiresAt, &k.RPMLimit, &k.TokenQuota, &k.DollarQuota, &k.Active, &k.CreatedAt, &k.LastUsedAt, &k.TotalTokens, &k.TotalCostUSD); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

func (s *Service) Get(ctx context.Context, id uuid.UUID) (*KeyWithUsage, error) {
	var k KeyWithUsage
	err := s.db.QueryRow(ctx, `
		SELECT k.id, k.prefix, k.owner, k.note, k.expires_at, k.rpm_limit, k.token_quota, k.dollar_quota, k.active, k.created_at, k.last_used_at,
		       COALESCE(q.total_tokens, 0), COALESCE(q.total_cost_usd, 0)
		FROM api_keys k
		LEFT JOIN quota_totals q ON q.key_id = k.id
		WHERE k.id = $1
	`, id).Scan(
		&k.ID, &k.Prefix, &k.Owner, &k.Note, &k.ExpiresAt, &k.RPMLimit, &k.TokenQuota, &k.DollarQuota,
		&k.Active, &k.CreatedAt, &k.LastUsedAt, &k.TotalTokens, &k.TotalCostUSD)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &k, nil
}

func (s *Service) SetActive(ctx context.Context, id uuid.UUID, active bool) error {
	tag, err := s.db.Exec(ctx, `UPDATE api_keys SET active = $1 WHERE id = $2`, active, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	s.invalidateCache(ctx, id)
	return nil
}

func (s *Service) Delete(ctx context.Context, id uuid.UUID) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM api_keys WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	s.invalidateCache(ctx, id)
	return nil
}

func (s *Service) Update(ctx context.Context, id uuid.UUID, p IssueParams) error {
	tag, err := s.db.Exec(ctx, `
		UPDATE api_keys SET owner = $2, note = $3, expires_at = $4,
		    rpm_limit = $5, token_quota = $6, dollar_quota = $7
		WHERE id = $1
	`, id, p.Owner, p.Note, p.ExpiresAt, p.RPMLimit, p.TokenQuota, p.DollarQuota)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	s.invalidateCache(ctx, id)
	return nil
}

func (s *Service) Stats(ctx context.Context) (Stats, error) {
	var st Stats
	err := s.db.QueryRow(ctx, `
		SELECT
			(SELECT COUNT(*)::bigint FROM api_keys),
			(SELECT COUNT(*)::bigint FROM api_keys WHERE active),
			COALESCE((SELECT SUM(input_tokens + output_tokens)::bigint FROM usage_records WHERE created_at >= CURRENT_DATE), 0),
			COALESCE((SELECT SUM(cost_usd) FROM usage_records WHERE created_at >= CURRENT_DATE), 0)
	`).Scan(&st.TotalKeys, &st.ActiveKeys, &st.TodayTokens, &st.TodayCostUSD)
	return st, err
}

// ---------------------------------------------------------------------------
// per-key enforcement: rate limit + token/dollar quota
// ---------------------------------------------------------------------------

// AllowRate increments the per-minute counter for the key and returns false
// once the count exceeds limit. limit <= 0 means unlimited.
func (s *Service) AllowRate(ctx context.Context, keyID uuid.UUID, limit int) (bool, error) {
	if limit <= 0 {
		return true, nil
	}
	bucket := time.Now().UTC().Unix() / 60
	rk := fmt.Sprintf("rl:%s:%d", keyID, bucket)
	pipe := s.rdb.TxPipeline()
	incr := pipe.Incr(ctx, rk)
	pipe.Expire(ctx, rk, 70*time.Second)
	if _, err := pipe.Exec(ctx); err != nil {
		return false, err
	}
	return int(incr.Val()) <= limit, nil
}

// AllowQuota returns false when cumulative tokens or cost exceed the quotas
// configured on the key. nil pointers mean "unlimited" for that dimension.
func (s *Service) AllowQuota(ctx context.Context, keyID uuid.UUID, tokenQuota *int64, dollarQuota *float64) (bool, error) {
	if tokenQuota == nil && dollarQuota == nil {
		return true, nil
	}
	tokens, cost, err := s.quotaTotals(ctx, keyID)
	if err != nil {
		return false, err
	}
	if tokenQuota != nil && tokens >= *tokenQuota {
		return false, nil
	}
	if dollarQuota != nil && cost >= *dollarQuota {
		return false, nil
	}
	return true, nil
}

// AddUsage updates the per-key running totals (DB authoritative, Redis hot
// cache). Called after each successful upstream response.
func (s *Service) AddUsage(ctx context.Context, keyID uuid.UUID, tokens int64, costUSD float64) error {
	if tokens == 0 && costUSD == 0 {
		return nil
	}
	if _, err := s.db.Exec(ctx, `
		INSERT INTO quota_totals (key_id, total_tokens, total_cost_usd, updated_at)
		VALUES ($1, $2, $3, now())
		ON CONFLICT (key_id) DO UPDATE
		SET total_tokens   = quota_totals.total_tokens   + EXCLUDED.total_tokens,
		    total_cost_usd = quota_totals.total_cost_usd + EXCLUDED.total_cost_usd,
		    updated_at = now()
	`, keyID, tokens, costUSD); err != nil {
		return err
	}
	_ = s.rdb.IncrBy(ctx, "qt:tok:"+keyID.String(), tokens).Err()
	_ = s.rdb.IncrByFloat(ctx, "qt:cost:"+keyID.String(), costUSD).Err()
	return nil
}

// RecordRequest writes one row to usage_records. Best-effort: errors are
// returned but should not block the user-facing response.
func (s *Service) RecordRequest(ctx context.Context, r UsageRecord) error {
	_, err := s.db.Exec(ctx, `
		INSERT INTO usage_records (
			key_id, request_id, endpoint, model, streaming,
			input_tokens, output_tokens, cached_tokens,
			cost_usd, status, duration_ms, error_message
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
	`,
		r.KeyID, nullableText(r.RequestID), r.Endpoint, nullableText(r.Model), r.Streaming,
		r.InputTokens, r.OutputTokens, r.CachedTokens,
		r.CostUSD, r.Status, int(r.Duration.Milliseconds()), nullableText(r.ErrorMessage),
	)
	return err
}

func (s *Service) quotaTotals(ctx context.Context, keyID uuid.UUID) (int64, float64, error) {
	tokKey := "qt:tok:" + keyID.String()
	costKey := "qt:cost:" + keyID.String()
	if t, err := s.rdb.Get(ctx, tokKey).Int64(); err == nil {
		if c, err := s.rdb.Get(ctx, costKey).Float64(); err == nil {
			return t, c, nil
		}
	}
	var t int64
	var c float64
	if err := s.db.QueryRow(ctx, `SELECT total_tokens, total_cost_usd FROM quota_totals WHERE key_id = $1`, keyID).Scan(&t, &c); err != nil {
		return 0, 0, err
	}
	_ = s.rdb.Set(ctx, tokKey, t, 0).Err()
	_ = s.rdb.Set(ctx, costKey, fmt.Sprintf("%f", c), 0).Err()
	return t, c, nil
}

// ---------------------------------------------------------------------------
// internals
// ---------------------------------------------------------------------------

const listKeysSQL = `
	SELECT k.id, k.prefix, k.owner, k.note, k.expires_at, k.rpm_limit, k.token_quota, k.dollar_quota, k.active, k.created_at, k.last_used_at,
	       COALESCE(q.total_tokens, 0), COALESCE(q.total_cost_usd, 0)
	FROM api_keys k
	LEFT JOIN quota_totals q ON q.key_id = k.id
	ORDER BY k.created_at DESC`

// prefixOf returns the leading id-prefix of a plain key (e.g. "sk-pxy-abc12345").
// Stored alongside the hash so the row can be looked up without scanning.
func (s *Service) prefixOf(plain string) string {
	cut := len(s.prefix) + 8
	if len(plain) < cut {
		return plain
	}
	return plain[:cut]
}

func (s *Service) invalidateCache(ctx context.Context, id uuid.UUID) {
	var prefix string
	if err := s.db.QueryRow(ctx, `SELECT prefix FROM api_keys WHERE id = $1`, id).Scan(&prefix); err == nil {
		_ = s.rdb.Del(ctx, "verify:"+prefix).Err()
	}
}

type cacheEntry struct {
	Key   *Key   `json:"key"`
	Plain string `json:"plain"`
}

func validateKey(k *Key) error {
	switch {
	case k == nil:
		return ErrInvalid
	case !k.Active:
		return ErrInactive
	case k.ExpiresAt != nil && k.ExpiresAt.Before(time.Now()):
		return ErrExpired
	}
	return nil
}

func nullableText(s string) any {
	if s == "" {
		return nil
	}
	return s
}
