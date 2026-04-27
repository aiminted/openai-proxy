package admin

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
)

var ErrUnauthorized = errors.New("unauthorized")

// Auth issues and validates short-lived signed Bearer tokens. The shape is
// "<exp-unix>.<base64url-hmac>", same idea as the previous session cookie but
// carried in the Authorization header so the SPA admin can talk to the API
// across origins without cookie/CSRF complications.
type Auth struct {
	password []byte
	secret   []byte
	ttl      time.Duration
}

func NewAuth(password, secret string, ttl time.Duration) *Auth {
	return &Auth{password: []byte(password), secret: []byte(secret), ttl: ttl}
}

func (a *Auth) TTL() time.Duration { return a.ttl }

func (a *Auth) VerifyPassword(submitted string) bool {
	return subtle.ConstantTimeCompare([]byte(submitted), a.password) == 1
}

func (a *Auth) IssueToken() string {
	exp := time.Now().Add(a.ttl).Unix()
	expStr := strconv.FormatInt(exp, 10)
	mac := hmac.New(sha256.New, a.secret)
	mac.Write([]byte(expStr))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return expStr + "." + sig
}

func (a *Auth) ValidateBearer(r *http.Request) error {
	tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || tok == "" {
		return ErrUnauthorized
	}
	parts := strings.SplitN(tok, ".", 2)
	if len(parts) != 2 {
		return ErrUnauthorized
	}
	exp, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return ErrUnauthorized
	}
	mac := hmac.New(sha256.New, a.secret)
	mac.Write([]byte(parts[0]))
	expected := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if subtle.ConstantTimeCompare([]byte(expected), []byte(parts[1])) != 1 {
		return ErrUnauthorized
	}
	if time.Unix(exp, 0).Before(time.Now()) {
		return ErrUnauthorized
	}
	return nil
}

func (a *Auth) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := a.ValidateBearer(r); err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
			return
		}
		next.ServeHTTP(w, r)
	})
}
