package ui

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/aiminted/openai-proxy/internal/keys"
)

func newTestHandler(t *testing.T) *Handler {
	t.Helper()
	h, err := New(nil, nil, "0123456789abcdef0123456789abcdef", false)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestRendersDashboardContent(t *testing.T) {
	h := newTestHandler(t)
	var buf bytes.Buffer
	tpl := h.pages["dashboard.html"]
	if tpl == nil {
		t.Fatal("dashboard.html not loaded")
	}
	err := tpl.ExecuteTemplate(&buf, "dashboard.html", map[string]any{
		"Keys":      []keys.KeyWithUsage{},
		"Stats":     keys.Stats{TotalKeys: 3, ActiveKeys: 2, TodayTokens: 1234, TodayCostUSD: 0.0123},
		"IssuedKey": "",
	})
	if err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, marker := range []string{
		"새 API 키 발급", "키 목록", "owner",
		"total keys", "active", "tokens today", "cost today",
		"1,234", "$0.0123",
		"key-search", "hide-inactive",
	} {
		if !strings.Contains(out, marker) {
			t.Errorf("dashboard missing %q", marker)
		}
	}
}

func TestDashboardShowsIssuedKeyOnce(t *testing.T) {
	h := newTestHandler(t)
	var buf bytes.Buffer
	err := h.pages["dashboard.html"].ExecuteTemplate(&buf, "dashboard.html", map[string]any{
		"Keys":      []keys.KeyWithUsage{},
		"Stats":     keys.Stats{},
		"IssuedKey": "sk-pxy-test-1234",
	})
	if err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "sk-pxy-test-1234") || !strings.Contains(out, "한 번만 표시") {
		t.Errorf("issued key panel missing\n%s", out)
	}
}

func TestRendersKeyDetailContent(t *testing.T) {
	h := newTestHandler(t)
	var buf bytes.Buffer
	tpl := h.pages["key_detail.html"]
	err := tpl.ExecuteTemplate(&buf, "key_detail.html", map[string]any{
		"Key": &keys.KeyWithUsage{Key: keys.Key{Owner: "alice", Prefix: "sk-pxy-abc"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, marker := range []string{"alice", "sk-pxy-abc", "최근 요청", "edit-form", "변경 사항 저장"} {
		if !strings.Contains(out, marker) {
			t.Errorf("key_detail missing %q", marker)
		}
	}
}

func TestRendersLogin(t *testing.T) {
	h := newTestHandler(t)
	var buf bytes.Buffer
	if err := h.pages["login.html"].ExecuteTemplate(&buf, "login.html", map[string]any{"Error": "invalid"}); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "admin password") || !strings.Contains(out, "invalid") {
		t.Errorf("login missing content\n%s", out)
	}
}

func TestStaticFiles(t *testing.T) {
	for _, name := range []string{"static/style.css", "static/app.js"} {
		b, err := assets.ReadFile(name)
		if err != nil || len(b) == 0 {
			t.Errorf("missing or empty: %s (%v)", name, err)
		}
	}
}

func TestFlashRoundTrip(t *testing.T) {
	f := newFlash("0123456789abcdef0123456789abcdef", false)
	rec := &headerRecorder{header: map[string][]string{}}
	f.set(rec, "sk-pxy-secret", 60*time.Second)
	c := rec.cookies()["openai_proxy_flash"]
	if c == "" {
		t.Fatal("no flash cookie set")
	}
	req := newReqWithCookie("openai_proxy_flash", c)
	rec2 := &headerRecorder{header: map[string][]string{}}
	got := f.pop(rec2, req)
	if got != "sk-pxy-secret" {
		t.Errorf("flash round-trip got %q want %q", got, "sk-pxy-secret")
	}
	// after pop, cookie must be cleared
	cleared := rec2.cookies()["openai_proxy_flash"]
	if cleared != "" && !strings.Contains(rec2.headerString("Set-Cookie"), "Max-Age=0") {
		t.Errorf("flash not cleared after pop, set-cookie=%q", rec2.headerString("Set-Cookie"))
	}
}

func TestFlashTamperedRejected(t *testing.T) {
	f := newFlash("0123456789abcdef0123456789abcdef", false)
	rec := &headerRecorder{header: map[string][]string{}}
	f.set(rec, "sk-pxy-real", 60*time.Second)
	cookie := rec.cookies()["openai_proxy_flash"]
	tampered := cookie[:len(cookie)-3] + "AAA"
	req := newReqWithCookie("openai_proxy_flash", tampered)
	rec2 := &headerRecorder{header: map[string][]string{}}
	if got := f.pop(rec2, req); got != "" {
		t.Errorf("tampered flash should not validate, got %q", got)
	}
}
