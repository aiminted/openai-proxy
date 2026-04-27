package admin

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

const testSecret = "0123456789abcdef0123456789abcdef"

func TestVerifyPassword(t *testing.T) {
	a := NewAuth("hunter2", testSecret, time.Hour)
	if !a.VerifyPassword("hunter2") {
		t.Error("correct password rejected")
	}
	if a.VerifyPassword("wrong") {
		t.Error("wrong password accepted")
	}
}

func TestTokenRoundTrip(t *testing.T) {
	a := NewAuth("hunter2", testSecret, time.Hour)
	tok := a.IssueToken()
	if !strings.Contains(tok, ".") {
		t.Fatalf("malformed token: %q", tok)
	}
	r := &http.Request{Header: http.Header{"Authorization": {"Bearer " + tok}}}
	if err := a.ValidateBearer(r); err != nil {
		t.Errorf("valid bearer rejected: %v", err)
	}
}

func TestTokenTampered(t *testing.T) {
	a := NewAuth("hunter2", testSecret, time.Hour)
	tok := a.IssueToken()
	tampered := tok[:len(tok)-3] + "AAA"
	r := &http.Request{Header: http.Header{"Authorization": {"Bearer " + tampered}}}
	if err := a.ValidateBearer(r); err == nil {
		t.Error("tampered bearer accepted")
	}
}

func TestTokenExpired(t *testing.T) {
	a := NewAuth("hunter2", testSecret, -time.Second) // already expired
	tok := a.IssueToken()
	r := &http.Request{Header: http.Header{"Authorization": {"Bearer " + tok}}}
	if err := a.ValidateBearer(r); err == nil {
		t.Error("expired bearer accepted")
	}
}

func TestMissingBearerPrefix(t *testing.T) {
	a := NewAuth("hunter2", testSecret, time.Hour)
	r := &http.Request{Header: http.Header{"Authorization": {a.IssueToken()}}}
	if err := a.ValidateBearer(r); err == nil {
		t.Error("token without 'Bearer ' prefix accepted")
	}
}
