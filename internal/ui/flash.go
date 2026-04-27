package ui

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

// flash stores a single short-lived value (e.g. an issued API key) in a signed
// cookie so we can survive the post-redirect-get flow without putting the
// secret in the URL (which would leak into browser history and access logs).
//
// Format: base64url(value).expiry.hmac. After the dashboard renders the
// value once, it clears the cookie so a refresh doesn't keep showing it.
type flash struct {
	secret []byte
	secure bool
}

const flashCookie = "openai_proxy_flash"

func newFlash(secret string, secure bool) *flash {
	return &flash{secret: []byte(secret), secure: secure}
}

func (f *flash) set(w http.ResponseWriter, value string, ttl time.Duration) {
	exp := time.Now().Add(ttl).Unix()
	v := base64.RawURLEncoding.EncodeToString([]byte(value))
	expStr := strconv.FormatInt(exp, 10)
	mac := hmac.New(sha256.New, f.secret)
	mac.Write([]byte(v + "." + expStr))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	http.SetCookie(w, &http.Cookie{
		Name:     flashCookie,
		Value:    v + "." + expStr + "." + sig,
		Path:     "/",
		HttpOnly: true,
		Secure:   f.secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(ttl.Seconds()),
	})
}

// pop reads the flash cookie if present, validates it, and clears it.
func (f *flash) pop(w http.ResponseWriter, r *http.Request) string {
	c, err := r.Cookie(flashCookie)
	if err != nil || c.Value == "" {
		return ""
	}
	parts := strings.SplitN(c.Value, ".", 3)
	if len(parts) != 3 {
		f.clear(w)
		return ""
	}
	exp, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || time.Unix(exp, 0).Before(time.Now()) {
		f.clear(w)
		return ""
	}
	mac := hmac.New(sha256.New, f.secret)
	mac.Write([]byte(parts[0] + "." + parts[1]))
	expected := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if subtle.ConstantTimeCompare([]byte(expected), []byte(parts[2])) != 1 {
		f.clear(w)
		return ""
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		f.clear(w)
		return ""
	}
	f.clear(w)
	return string(raw)
}

func (f *flash) clear(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     flashCookie,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   f.secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}
