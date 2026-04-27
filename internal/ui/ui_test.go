package ui

import (
	"bytes"
	"strings"
	"testing"

	"github.com/aiminted/openai-proxy/internal/keys"
)

func TestRendersDashboardContent(t *testing.T) {
	h, err := New(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	tpl := h.pages["dashboard.html"]
	if tpl == nil {
		t.Fatal("dashboard.html not loaded")
	}
	if err := tpl.ExecuteTemplate(&buf, "dashboard.html", map[string]any{
		"Keys":      []keys.KeyWithUsage{},
		"Flash":     "",
		"IssuedKey": "",
	}); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, marker := range []string{"새 API 키 발급", "발급된 키", "owner"} {
		if !strings.Contains(out, marker) {
			t.Errorf("dashboard missing %q\n--- output ---\n%s", marker, out)
		}
	}
}

func TestRendersKeyDetailContent(t *testing.T) {
	h, err := New(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	tpl := h.pages["key_detail.html"]
	if tpl == nil {
		t.Fatal("key_detail.html not loaded")
	}
	if err := tpl.ExecuteTemplate(&buf, "key_detail.html", map[string]any{
		"Key": &keys.KeyWithUsage{Key: keys.Key{Owner: "alice", Prefix: "sk-pxy-abc"}},
	}); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, marker := range []string{"alice", "sk-pxy-abc", "최근 요청"} {
		if !strings.Contains(out, marker) {
			t.Errorf("key_detail missing %q", marker)
		}
	}
}

func TestRendersLogin(t *testing.T) {
	h, err := New(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
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
	for _, name := range []string{"static/style.css", "static/recent.js"} {
		b, err := assets.ReadFile(name)
		if err != nil || len(b) == 0 {
			t.Errorf("missing or empty: %s (%v)", name, err)
		}
	}
}
