package ui

import (
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/aiminted/openai-proxy/internal/admin"
	"github.com/aiminted/openai-proxy/internal/keys"
)

//go:embed templates/*.html static/*
var assets embed.FS

type Handler struct {
	tpl  *template.Template
	keys *keys.Service
	auth *admin.Auth
}

func New(k *keys.Service, auth *admin.Auth) (*Handler, error) {
	funcs := template.FuncMap{
		"fmtTime": func(t *time.Time) string {
			if t == nil {
				return "—"
			}
			return t.Local().Format("2006-01-02 15:04")
		},
		"fmtDollar": func(v float64) string {
			return fmt.Sprintf("$%.4f", v)
		},
		"fmtInt": func(v int64) string {
			return formatInt(v)
		},
		"fmtIntPtr": func(v *int) string {
			if v == nil {
				return "—"
			}
			return strconv.Itoa(*v)
		},
		"fmtInt64Ptr": func(v *int64) string {
			if v == nil {
				return "—"
			}
			return formatInt(*v)
		},
		"fmtFloatPtr": func(v *float64) string {
			if v == nil {
				return "—"
			}
			return fmt.Sprintf("$%.2f", *v)
		},
	}
	tpl, err := template.New("").Funcs(funcs).ParseFS(assets, "templates/*.html")
	if err != nil {
		return nil, err
	}
	return &Handler{tpl: tpl, keys: k, auth: auth}, nil
}

func (h *Handler) Mount(mux *http.ServeMux) {
	staticFS, _ := fs.Sub(assets, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticFS))))

	mux.HandleFunc("GET /login", h.loginGet)
	mux.HandleFunc("POST /login", h.loginPost)
	mux.HandleFunc("POST /logout", h.logout)

	mux.Handle("GET /{$}", h.auth.Middleware("/login", http.HandlerFunc(h.dashboard)))
	mux.Handle("GET /keys/{id}", h.auth.Middleware("/login", http.HandlerFunc(h.keyDetail)))
	mux.Handle("POST /keys", h.auth.Middleware("/login", http.HandlerFunc(h.createKey)))
	mux.Handle("POST /keys/{id}/active", h.auth.Middleware("/login", http.HandlerFunc(h.toggleActive)))
	mux.Handle("POST /keys/{id}/delete", h.auth.Middleware("/login", http.HandlerFunc(h.deleteKey)))
}

func (h *Handler) loginGet(w http.ResponseWriter, r *http.Request) {
	if h.auth.IsAuthenticated(r) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	h.render(w, "login.html", map[string]any{"Error": r.URL.Query().Get("error")})
}

func (h *Handler) loginPost(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	pw := r.FormValue("password")
	if !h.auth.Verify(pw) {
		http.Redirect(w, r, "/login?error=invalid", http.StatusSeeOther)
		return
	}
	h.auth.IssueCookie(w)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	h.auth.Clear(w)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (h *Handler) dashboard(w http.ResponseWriter, r *http.Request) {
	list, err := h.keys.List(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	flash := r.URL.Query().Get("flash")
	plain := r.URL.Query().Get("issued")
	h.render(w, "dashboard.html", map[string]any{
		"Keys":     list,
		"Flash":    flash,
		"IssuedKey": plain,
	})
}

func (h *Handler) keyDetail(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	k, err := h.keys.Get(r.Context(), id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h.render(w, "key_detail.html", map[string]any{"Key": k})
}

func (h *Handler) createKey(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	params := keys.IssueParams{
		Owner: strings.TrimSpace(r.FormValue("owner")),
		Note:  r.FormValue("note"),
	}
	if v := strings.TrimSpace(r.FormValue("expires_at")); v != "" {
		t, err := time.Parse("2006-01-02", v)
		if err == nil {
			params.ExpiresAt = &t
		}
	}
	if v := strings.TrimSpace(r.FormValue("rpm_limit")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			params.RPMLimit = &n
		}
	}
	if v := strings.TrimSpace(r.FormValue("token_quota")); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			params.TokenQuota = &n
		}
	}
	if v := strings.TrimSpace(r.FormValue("dollar_quota")); v != "" {
		if n, err := strconv.ParseFloat(v, 64); err == nil && n > 0 {
			params.DollarQuota = &n
		}
	}
	if params.Owner == "" {
		http.Redirect(w, r, "/?flash=owner+required", http.StatusSeeOther)
		return
	}
	issued, err := h.keys.Issue(r.Context(), params)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/?issued="+issued.Plain, http.StatusSeeOther)
}

func (h *Handler) toggleActive(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	active := r.FormValue("active") == "true"
	if err := h.keys.SetActive(r.Context(), id, active); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (h *Handler) deleteKey(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	if err := h.keys.Delete(r.Context(), id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (h *Handler) render(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.tpl.ExecuteTemplate(w, name, data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func formatInt(n int64) string {
	if n == 0 {
		return "0"
	}
	negative := n < 0
	if negative {
		n = -n
	}
	s := strconv.FormatInt(n, 10)
	var out []byte
	for i, c := range []byte(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, c)
	}
	if negative {
		return "-" + string(out)
	}
	return string(out)
}
