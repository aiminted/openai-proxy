package admin

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	cookieName = "openai_proxy_session"
)

type Auth struct {
	password []byte
	secret   []byte
	ttl      time.Duration
	secure   bool
}

func NewAuth(password, secret string, ttl time.Duration, secure bool) *Auth {
	return &Auth{
		password: []byte(password),
		secret:   []byte(secret),
		ttl:      ttl,
		secure:   secure,
	}
}

func (a *Auth) Verify(submitted string) bool {
	return subtle.ConstantTimeCompare([]byte(submitted), a.password) == 1
}

func (a *Auth) IssueCookie(w http.ResponseWriter) {
	exp := time.Now().Add(a.ttl).Unix()
	value := signValue(a.secret, exp)
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		Secure:   a.secure,
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Now().Add(a.ttl),
		MaxAge:   int(a.ttl.Seconds()),
	})
}

func (a *Auth) Clear(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   a.secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

func (a *Auth) IsAuthenticated(r *http.Request) bool {
	c, err := r.Cookie(cookieName)
	if err != nil || c.Value == "" {
		return false
	}
	exp, ok := verifyValue(a.secret, c.Value)
	if !ok {
		return false
	}
	return time.Unix(exp, 0).After(time.Now())
}

// Middleware redirects unauthenticated requests to /login.
func (a *Auth) Middleware(loginPath string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !a.IsAuthenticated(r) {
			http.Redirect(w, r, loginPath, http.StatusSeeOther)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// APIMiddleware returns 401 instead of redirecting (for /admin/api/*).
func (a *Auth) APIMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !a.IsAuthenticated(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func signValue(secret []byte, exp int64) string {
	expStr := strconv.FormatInt(exp, 10)
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(expStr))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return expStr + "." + sig
}

func verifyValue(secret []byte, value string) (int64, bool) {
	parts := strings.SplitN(value, ".", 2)
	if len(parts) != 2 {
		return 0, false
	}
	exp, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return 0, false
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(parts[0]))
	expected := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if subtle.ConstantTimeCompare([]byte(expected), []byte(parts[1])) != 1 {
		return 0, false
	}
	return exp, true
}

