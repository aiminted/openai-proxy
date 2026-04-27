package ui

import (
	"html/template"
	"strings"
	"testing"
)

func TestTemplatesParse(t *testing.T) {
	funcs := template.FuncMap{
		"fmtTime":     func(any) string { return "" },
		"fmtDollar":   func(any) string { return "" },
		"fmtInt":      func(any) string { return "" },
		"fmtIntPtr":   func(any) string { return "" },
		"fmtInt64Ptr": func(any) string { return "" },
		"fmtFloatPtr": func(any) string { return "" },
	}
	if _, err := template.New("").Funcs(funcs).ParseFS(assets, "templates/*.html"); err != nil {
		t.Fatalf("parse failed: %v", err)
	}
}

func TestStaticFiles(t *testing.T) {
	for _, name := range []string{"static/style.css", "static/recent.js"} {
		b, err := assets.ReadFile(name)
		if err != nil {
			t.Errorf("missing %s: %v", name, err)
			continue
		}
		if len(b) == 0 {
			t.Errorf("%s is empty", name)
		}
		if !strings.HasSuffix(name, ".css") && !strings.HasSuffix(name, ".js") {
			t.Errorf("unexpected file: %s", name)
		}
	}
}
