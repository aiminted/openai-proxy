package keys

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"golang.org/x/crypto/argon2"
)

var (
	ErrNotFound = errors.New("key not found")
	ErrInvalid  = errors.New("invalid key")
	ErrInactive = errors.New("key inactive")
	ErrExpired  = errors.New("key expired")
)

type Key struct {
	ID           uuid.UUID
	Prefix       string
	Owner        string
	Note         string
	ExpiresAt    *time.Time
	RPMLimit     *int
	TokenQuota   *int64
	DollarQuota  *float64
	Active       bool
	CreatedAt    time.Time
	LastUsedAt   *time.Time
}

type KeyWithUsage struct {
	Key
	TotalTokens  int64
	TotalCostUSD float64
}

type Service struct {
	db        *pgxpool.Pool
	rdb       *redis.Client
	prefix    string
	cacheTTL  time.Duration
}

func NewService(db *pgxpool.Pool, rdb *redis.Client, keyPrefix string, cacheTTL time.Duration) *Service {
	return &Service{db: db, rdb: rdb, prefix: keyPrefix, cacheTTL: cacheTTL}
}

// prefixOf returns the leading id-prefix of a plain key (e.g. "sk-pxy-abc12345").
// Stored alongside the hash so the row can be looked up without scanning.
func (s *Service) prefixOf(plain string) string {
	cut := len(s.prefix) + 8
	if len(plain) < cut {
		return plain
	}
	return plain[:cut]
}

type IssueParams struct {
	Owner        string
	Note         string
	ExpiresAt    *time.Time
	RPMLimit     *int
	TokenQuota   *int64
	DollarQuota  *float64
}

type IssuedKey struct {
	Key      Key
	Plain    string
}

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
	_, err = s.db.Exec(ctx, `
		INSERT INTO api_keys (id, prefix, hash, owner, note, expires_at, rpm_limit, token_quota, dollar_quota, active)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, TRUE)
	`, id, prefix, hash, p.Owner, p.Note, p.ExpiresAt, p.RPMLimit, p.TokenQuota, p.DollarQuota)
	if err != nil {
		return nil, fmt.Errorf("insert key: %w", err)
	}

	_, _ = s.db.Exec(ctx, `INSERT INTO quota_totals (key_id) VALUES ($1) ON CONFLICT DO NOTHING`, id)

	k := Key{
		ID:          id,
		Prefix:      prefix,
		Owner:       p.Owner,
		Note:        p.Note,
		ExpiresAt:   p.ExpiresAt,
		RPMLimit:    p.RPMLimit,
		TokenQuota:  p.TokenQuota,
		DollarQuota: p.DollarQuota,
		Active:      true,
		CreatedAt:   time.Now(),
	}
	return &IssuedKey{Key: k, Plain: plain}, nil
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

func (s *Service) List(ctx context.Context) ([]KeyWithUsage, error) {
	rows, err := s.db.Query(ctx, `
		SELECT k.id, k.prefix, k.owner, k.note, k.expires_at, k.rpm_limit, k.token_quota, k.dollar_quota, k.active, k.created_at, k.last_used_at,
		       COALESCE(q.total_tokens, 0), COALESCE(q.total_cost_usd, 0)
		FROM api_keys k
		LEFT JOIN quota_totals q ON q.key_id = k.id
		ORDER BY k.created_at DESC
	`)
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

type Stats struct {
	TotalKeys    int64
	ActiveKeys   int64
	TodayTokens  int64
	TodayCostUSD float64
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

func (s *Service) Get(ctx context.Context, id uuid.UUID) (*KeyWithUsage, error) {
	row := s.db.QueryRow(ctx, `
		SELECT k.id, k.prefix, k.owner, k.note, k.expires_at, k.rpm_limit, k.token_quota, k.dollar_quota, k.active, k.created_at, k.last_used_at,
		       COALESCE(q.total_tokens, 0), COALESCE(q.total_cost_usd, 0)
		FROM api_keys k
		LEFT JOIN quota_totals q ON q.key_id = k.id
		WHERE k.id = $1
	`, id)
	var k KeyWithUsage
	if err := row.Scan(&k.ID, &k.Prefix, &k.Owner, &k.Note, &k.ExpiresAt, &k.RPMLimit, &k.TokenQuota, &k.DollarQuota, &k.Active, &k.CreatedAt, &k.LastUsedAt, &k.TotalTokens, &k.TotalCostUSD); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
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
	return s.invalidateCache(ctx, id)
}

func (s *Service) Delete(ctx context.Context, id uuid.UUID) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM api_keys WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return s.invalidateCache(ctx, id)
}

func (s *Service) Update(ctx context.Context, id uuid.UUID, p IssueParams) error {
	tag, err := s.db.Exec(ctx, `
		UPDATE api_keys SET
			owner = $2,
			note = $3,
			expires_at = $4,
			rpm_limit = $5,
			token_quota = $6,
			dollar_quota = $7
		WHERE id = $1
	`, id, p.Owner, p.Note, p.ExpiresAt, p.RPMLimit, p.TokenQuota, p.DollarQuota)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return s.invalidateCache(ctx, id)
}

func (s *Service) invalidateCache(ctx context.Context, id uuid.UUID) error {
	var prefix string
	if err := s.db.QueryRow(ctx, `SELECT prefix FROM api_keys WHERE id = $1`, id).Scan(&prefix); err == nil {
		_ = s.rdb.Del(ctx, "verify:"+prefix).Err()
	}
	return nil
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

const (
	argonTime    = 1
	argonMemory  = 32 * 1024
	argonThreads = 2
	argonKeyLen  = 32
)

func hashKey(plain string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	sum := argon2.IDKey([]byte(plain), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("argon2id$%s$%s", hex.EncodeToString(salt), hex.EncodeToString(sum)), nil
}

func verifyHash(plain, encoded string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 3 || parts[0] != "argon2id" {
		return false
	}
	salt, err := hex.DecodeString(parts[1])
	if err != nil {
		return false
	}
	expected, err := hex.DecodeString(parts[2])
	if err != nil {
		return false
	}
	sum := argon2.IDKey([]byte(plain), salt, argonTime, argonMemory, argonThreads, uint32(len(expected)))
	return subtle.ConstantTimeCompare(sum, expected) == 1
}
