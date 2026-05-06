package keys

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// UpstreamStore manages the real OpenAI key the proxy uses. Plaintext lives
// only in memory (atomic snapshot) and on the wire to OpenAI; on disk it is
// always AES-GCM encrypted with a key from the KEY_ENCRYPTION_KEY env. A
// background refresher pulls the latest active row from Postgres so that
// rotations made via the admin UI propagate to all replicas without restart.
type UpstreamStore struct {
	db    *pgxpool.Pool
	gcm   cipher.AEAD
	cache atomic.Pointer[upstreamSnapshot]
}

type upstreamSnapshot struct {
	plain     string
	prefix    string
	createdAt time.Time
}

type UpstreamKey struct {
	ID         int64
	Prefix     string
	Note       string
	Active     bool
	CreatedAt  time.Time
	RetiredAt  *time.Time
}

func NewUpstreamStore(db *pgxpool.Pool, encryptionKeyHex string) (*UpstreamStore, error) {
	keyBytes, err := hex.DecodeString(encryptionKeyHex)
	if err != nil {
		return nil, fmt.Errorf("KEY_ENCRYPTION_KEY: must be hex (got %d chars): %w", len(encryptionKeyHex), err)
	}
	if len(keyBytes) != 32 {
		return nil, fmt.Errorf("KEY_ENCRYPTION_KEY: must decode to 32 bytes, got %d", len(keyBytes))
	}
	block, err := aes.NewCipher(keyBytes)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &UpstreamStore{db: db, gcm: gcm}, nil
}

// Bootstrap loads the active upstream key into the in-memory cache. If the
// table is empty and a fallback plaintext is provided (typically from the
// OPENAI_API_KEY env var on first boot), it is encrypted and inserted as the
// first active row. Returns an error if neither source has a key.
func (s *UpstreamStore) Bootstrap(ctx context.Context, fallbackPlain string) error {
	if err := s.refresh(ctx); err == nil {
		return nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	// no active row in DB — try fallback
	if fallbackPlain == "" {
		return errors.New("no upstream key configured: set OPENAI_API_KEY or insert via admin UI")
	}
	if err := s.Set(ctx, fallbackPlain, "imported from env on first boot"); err != nil {
		return fmt.Errorf("bootstrap insert: %w", err)
	}
	return s.refresh(ctx)
}

// StartRefresher polls the DB every interval so a key rotation done elsewhere
// (admin UI on another replica) eventually propagates here. Returns when ctx
// is done.
func (s *UpstreamStore) StartRefresher(ctx context.Context, interval time.Duration, onError func(error)) {
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if err := s.refresh(ctx); err != nil && !errors.Is(err, pgx.ErrNoRows) {
					if onError != nil {
						onError(err)
					}
				}
			}
		}
	}()
}

// Current returns the plaintext key currently active. Safe to call on the hot
// path; reads an atomic pointer.
func (s *UpstreamStore) Current() string {
	if snap := s.cache.Load(); snap != nil {
		return snap.plain
	}
	return ""
}

// CurrentMeta returns display-safe metadata about the active key.
func (s *UpstreamStore) CurrentMeta() (prefix string, createdAt time.Time, ok bool) {
	if snap := s.cache.Load(); snap != nil {
		return snap.prefix, snap.createdAt, true
	}
	return "", time.Time{}, false
}

// Set encrypts a new key, retires the previous active row in the same tx, and
// inserts the new one as active. Refreshes the in-memory cache before
// returning so the caller sees the change immediately.
func (s *UpstreamStore) Set(ctx context.Context, plain, note string) error {
	plain = strings.TrimSpace(plain)
	if plain == "" {
		return errors.New("upstream key is empty")
	}
	if !strings.HasPrefix(plain, "sk-") {
		return errors.New("upstream key should start with sk-")
	}

	enc, err := s.encrypt(plain)
	if err != nil {
		return err
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx,
		`UPDATE upstream_keys SET active = FALSE, retired_at = now() WHERE active`); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO upstream_keys (encrypted, prefix, note, active) VALUES ($1, $2, $3, TRUE)`,
		enc, maskPrefix(plain), note); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	return s.refresh(ctx)
}

// History returns all upstream keys (active + retired) for audit.
// Plaintext is never returned; only masked prefixes.
func (s *UpstreamStore) History(ctx context.Context) ([]UpstreamKey, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id, prefix, note, active, created_at, retired_at
		FROM upstream_keys
		ORDER BY created_at DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UpstreamKey
	for rows.Next() {
		var k UpstreamKey
		if err := rows.Scan(&k.ID, &k.Prefix, &k.Note, &k.Active, &k.CreatedAt, &k.RetiredAt); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

func (s *UpstreamStore) refresh(ctx context.Context) error {
	row := s.db.QueryRow(ctx, `
		SELECT encrypted, prefix, created_at FROM upstream_keys WHERE active LIMIT 1
	`)
	var enc []byte
	var prefix string
	var createdAt time.Time
	if err := row.Scan(&enc, &prefix, &createdAt); err != nil {
		return err
	}
	plain, err := s.decrypt(enc)
	if err != nil {
		return fmt.Errorf("decrypt active upstream key: %w", err)
	}
	s.cache.Store(&upstreamSnapshot{plain: plain, prefix: prefix, createdAt: createdAt})
	return nil
}

func (s *UpstreamStore) encrypt(plain string) ([]byte, error) {
	nonce := make([]byte, s.gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	ciphertext := s.gcm.Seal(nonce, nonce, []byte(plain), nil)
	return ciphertext, nil
}

func (s *UpstreamStore) decrypt(blob []byte) (string, error) {
	ns := s.gcm.NonceSize()
	if len(blob) < ns {
		return "", errors.New("ciphertext too short")
	}
	nonce, ciphertext := blob[:ns], blob[ns:]
	plain, err := s.gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

// maskPrefix is a display-only fingerprint: first 12 chars + "…" + last 4.
func maskPrefix(plain string) string {
	if len(plain) <= 16 {
		return strings.Repeat("•", len(plain))
	}
	return plain[:12] + "…" + plain[len(plain)-4:]
}
