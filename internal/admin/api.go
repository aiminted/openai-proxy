package admin

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aiminted/openai-proxy/internal/keys"
)

type API struct {
	keys     *keys.Service
	upstream *keys.UpstreamStore
	db       *pgxpool.Pool
	auth     *Auth
	origins  []string
}

func NewAPI(k *keys.Service, upstream *keys.UpstreamStore, db *pgxpool.Pool, auth *Auth, allowedOrigins []string) *API {
	return &API{keys: k, upstream: upstream, db: db, auth: auth, origins: allowedOrigins}
}

func (a *API) Mount(mux *http.ServeMux) {
	mux.HandleFunc("POST /admin/api/login", a.login)
	mux.HandleFunc("GET /admin/api/me", a.me)

	authed := func(h http.HandlerFunc) http.Handler { return a.auth.Middleware(h) }
	mux.Handle("GET /admin/api/stats", authed(a.stats))
	mux.Handle("GET /admin/api/keys", authed(a.listKeys))
	mux.Handle("POST /admin/api/keys", authed(a.createKey))
	mux.Handle("GET /admin/api/keys/{id}", authed(a.getKey))
	mux.Handle("PATCH /admin/api/keys/{id}", authed(a.updateKey))
	mux.Handle("DELETE /admin/api/keys/{id}", authed(a.deleteKey))
	mux.Handle("POST /admin/api/keys/{id}/active", authed(a.toggleActive))
	mux.Handle("GET /admin/api/usage", authed(a.usageSummary))
	mux.Handle("GET /admin/api/usage/by-model", authed(a.usageByModel))
	mux.Handle("GET /admin/api/usage/recent", authed(a.usageRecentAll))
	mux.Handle("GET /admin/api/usage/{id}/recent", authed(a.usageRecent))
	mux.Handle("GET /admin/api/upstream-keys", authed(a.upstreamKeys))
	mux.Handle("POST /admin/api/upstream-keys", authed(a.rotateUpstreamKey))
}

// CORS wraps an admin-API mux. Allowed origins are exact-match strings
// (Origin header). Preflight requests get 204 with the right headers.
func (a *API) CORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" && a.originAllowed(origin) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
			w.Header().Set("Access-Control-Max-Age", "600")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *API) originAllowed(origin string) bool {
	for _, o := range a.origins {
		if o == "*" || o == origin {
			return true
		}
	}
	return false
}

type loginReq struct {
	Password string `json:"password"`
}

func (a *API) login(w http.ResponseWriter, r *http.Request) {
	var req loginReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	if !a.auth.VerifyPassword(req.Password) {
		// modest constant-ish delay to slow naive bruteforce + smooth timing.
		time.Sleep(200 * time.Millisecond)
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "invalid password"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"token":      a.auth.IssueToken(),
		"expires_in": int(a.auth.TTL().Seconds()),
	})
}

func (a *API) me(w http.ResponseWriter, r *http.Request) {
	if err := a.auth.ValidateBearer(r); err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"authenticated": false})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"authenticated": true})
}

func (a *API) stats(w http.ResponseWriter, r *http.Request) {
	st, err := a.keys.Stats(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"total_keys":      st.TotalKeys,
		"active_keys":     st.ActiveKeys,
		"today_tokens":    st.TodayTokens,
		"today_cost_usd":  st.TodayCostUSD,
	})
}

type keyDTO struct {
	ID            string   `json:"id"`
	Prefix        string   `json:"prefix"`
	Owner         string   `json:"owner"`
	Note          string   `json:"note"`
	ExpiresAt     *string  `json:"expires_at"`
	RPMLimit      *int     `json:"rpm_limit"`
	TokenQuota    *int64   `json:"token_quota"`
	DollarQuota   *float64 `json:"dollar_quota"`
	Active        bool     `json:"active"`
	CreatedAt     string   `json:"created_at"`
	LastUsedAt    *string  `json:"last_used_at"`
	TotalTokens   int64    `json:"total_tokens"`
	TotalCostUSD  float64  `json:"total_cost_usd"`
}

func toDTO(k keys.KeyWithUsage) keyDTO {
	d := keyDTO{
		ID:           k.ID.String(),
		Prefix:       k.Prefix,
		Owner:        k.Owner,
		Note:         k.Note,
		RPMLimit:     k.RPMLimit,
		TokenQuota:   k.TokenQuota,
		DollarQuota:  k.DollarQuota,
		Active:       k.Active,
		CreatedAt:    k.CreatedAt.Format(time.RFC3339),
		TotalTokens:  k.TotalTokens,
		TotalCostUSD: k.TotalCostUSD,
	}
	if k.ExpiresAt != nil {
		s := k.ExpiresAt.Format(time.RFC3339)
		d.ExpiresAt = &s
	}
	if k.LastUsedAt != nil {
		s := k.LastUsedAt.Format(time.RFC3339)
		d.LastUsedAt = &s
	}
	return d
}

func (a *API) listKeys(w http.ResponseWriter, r *http.Request) {
	list, err := a.keys.List(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	out := make([]keyDTO, 0, len(list))
	for _, k := range list {
		out = append(out, toDTO(k))
	}
	writeJSON(w, http.StatusOK, out)
}

type createReq struct {
	Owner       string   `json:"owner"`
	Note        string   `json:"note"`
	ExpiresAt   *string  `json:"expires_at"`
	RPMLimit    *int     `json:"rpm_limit"`
	TokenQuota  *int64   `json:"token_quota"`
	DollarQuota *float64 `json:"dollar_quota"`
}

func (a *API) createKey(w http.ResponseWriter, r *http.Request) {
	var req createReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	params, err := buildIssueParams(req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	issued, err := a.keys.Issue(r.Context(), params)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"id":     issued.Key.ID.String(),
		"prefix": issued.Key.Prefix,
		"key":    issued.Plain,
		"owner":  issued.Key.Owner,
	})
}

func (a *API) getKey(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	k, err := a.keys.Get(r.Context(), id)
	if err != nil {
		if errors.Is(err, keys.ErrNotFound) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, toDTO(*k))
}

func (a *API) updateKey(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	var req createReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	params, err := buildIssueParams(req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := a.keys.Update(r.Context(), id, params); err != nil {
		if errors.Is(err, keys.ErrNotFound) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) deleteKey(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	if err := a.keys.Delete(r.Context(), id); err != nil {
		if errors.Is(err, keys.ErrNotFound) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) toggleActive(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	var body struct {
		Active bool `json:"active"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	if err := a.keys.SetActive(r.Context(), id, body.Active); err != nil {
		if errors.Is(err, keys.ErrNotFound) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type usageSummaryRow struct {
	Day          string  `json:"day"`
	Tokens       int64   `json:"tokens"`
	CostUSD      float64 `json:"cost_usd"`
	Requests     int64   `json:"requests"`
}

func (a *API) usageSummary(w http.ResponseWriter, r *http.Request) {
	days := 30
	if v := r.URL.Query().Get("days"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 365 {
			days = n
		}
	}
	rows, err := a.db.Query(r.Context(), `
		SELECT to_char(date_trunc('day', created_at), 'YYYY-MM-DD'),
		       COALESCE(SUM(input_tokens + output_tokens), 0),
		       COALESCE(SUM(cost_usd), 0),
		       COUNT(*)
		FROM usage_records
		WHERE created_at >= now() - make_interval(days => $1)
		GROUP BY 1
		ORDER BY 1
	`, days)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	var out []usageSummaryRow
	for rows.Next() {
		var u usageSummaryRow
		if err := rows.Scan(&u.Day, &u.Tokens, &u.CostUSD, &u.Requests); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		out = append(out, u)
	}
	writeJSON(w, http.StatusOK, out)
}

type recentRow struct {
	CreatedAt    string  `json:"created_at"`
	Endpoint     string  `json:"endpoint"`
	Model        string  `json:"model"`
	Streaming    bool    `json:"streaming"`
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	CostUSD      float64 `json:"cost_usd"`
	Status       int     `json:"status"`
	DurationMS   int     `json:"duration_ms"`
}

func (a *API) usageRecent(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 500 {
			limit = n
		}
	}
	rows, err := a.db.Query(r.Context(), `
		SELECT to_char(created_at, 'YYYY-MM-DD"T"HH24:MI:SS"Z"'),
		       endpoint, COALESCE(model, ''), streaming,
		       input_tokens, output_tokens, cost_usd, status, duration_ms
		FROM usage_records
		WHERE key_id = $1
		ORDER BY created_at DESC
		LIMIT $2
	`, id, limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	var out []recentRow
	for rows.Next() {
		var u recentRow
		if err := rows.Scan(&u.CreatedAt, &u.Endpoint, &u.Model, &u.Streaming, &u.InputTokens, &u.OutputTokens, &u.CostUSD, &u.Status, &u.DurationMS); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		out = append(out, u)
	}
	writeJSON(w, http.StatusOK, out)
}

type modelRow struct {
	Model    string  `json:"model"`
	Tokens   int64   `json:"tokens"`
	CostUSD  float64 `json:"cost_usd"`
	Requests int64   `json:"requests"`
}

func (a *API) usageByModel(w http.ResponseWriter, r *http.Request) {
	days := 30
	if v := r.URL.Query().Get("days"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 365 {
			days = n
		}
	}
	rows, err := a.db.Query(r.Context(), `
		SELECT COALESCE(NULLIF(model, ''), '(unknown)'),
		       COALESCE(SUM(input_tokens + output_tokens), 0),
		       COALESCE(SUM(cost_usd), 0),
		       COUNT(*)
		FROM usage_records
		WHERE created_at >= now() - make_interval(days => $1)
		GROUP BY 1
		ORDER BY 2 DESC
		LIMIT 20
	`, days)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	var out []modelRow
	for rows.Next() {
		var m modelRow
		if err := rows.Scan(&m.Model, &m.Tokens, &m.CostUSD, &m.Requests); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		out = append(out, m)
	}
	writeJSON(w, http.StatusOK, out)
}

type recentAllRow struct {
	CreatedAt    string  `json:"created_at"`
	KeyID        string  `json:"key_id"`
	KeyPrefix    string  `json:"key_prefix"`
	KeyOwner     string  `json:"key_owner"`
	Endpoint     string  `json:"endpoint"`
	Model        string  `json:"model"`
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	CostUSD      float64 `json:"cost_usd"`
	Status       int     `json:"status"`
	DurationMS   int     `json:"duration_ms"`
}

func (a *API) usageRecentAll(w http.ResponseWriter, r *http.Request) {
	limit := 30
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}
	rows, err := a.db.Query(r.Context(), `
		SELECT to_char(u.created_at, 'YYYY-MM-DD"T"HH24:MI:SS"Z"'),
		       u.key_id, k.prefix, k.owner,
		       u.endpoint, COALESCE(u.model, ''),
		       u.input_tokens, u.output_tokens, u.cost_usd, u.status, u.duration_ms
		FROM usage_records u
		JOIN api_keys k ON k.id = u.key_id
		ORDER BY u.created_at DESC
		LIMIT $1
	`, limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	var out []recentAllRow
	for rows.Next() {
		var u recentAllRow
		if err := rows.Scan(&u.CreatedAt, &u.KeyID, &u.KeyPrefix, &u.KeyOwner, &u.Endpoint, &u.Model,
			&u.InputTokens, &u.OutputTokens, &u.CostUSD, &u.Status, &u.DurationMS); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		out = append(out, u)
	}
	writeJSON(w, http.StatusOK, out)
}

type upstreamKeyDTO struct {
	ID         int64   `json:"id"`
	Prefix     string  `json:"prefix"`
	Note       string  `json:"note"`
	Active     bool    `json:"active"`
	CreatedAt  string  `json:"created_at"`
	RetiredAt  *string `json:"retired_at"`
}

func (a *API) upstreamKeys(w http.ResponseWriter, r *http.Request) {
	hist, err := a.upstream.History(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	out := make([]upstreamKeyDTO, 0, len(hist))
	for _, k := range hist {
		d := upstreamKeyDTO{
			ID: k.ID, Prefix: k.Prefix, Note: k.Note, Active: k.Active,
			CreatedAt: k.CreatedAt.Format(time.RFC3339),
		}
		if k.RetiredAt != nil {
			s := k.RetiredAt.Format(time.RFC3339)
			d.RetiredAt = &s
		}
		out = append(out, d)
	}
	writeJSON(w, http.StatusOK, out)
}

type rotateUpstreamReq struct {
	Key  string `json:"key"`
	Note string `json:"note"`
}

func (a *API) rotateUpstreamKey(w http.ResponseWriter, r *http.Request) {
	var req rotateUpstreamReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	if err := a.upstream.Set(r.Context(), req.Key, req.Note); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func buildIssueParams(req createReq) (keys.IssueParams, error) {
	p := keys.IssueParams{
		Owner:       strings.TrimSpace(req.Owner),
		Note:        req.Note,
		RPMLimit:    req.RPMLimit,
		TokenQuota:  req.TokenQuota,
		DollarQuota: req.DollarQuota,
	}
	if p.Owner == "" {
		return p, errors.New("owner required")
	}
	if req.ExpiresAt != nil && *req.ExpiresAt != "" {
		t, err := time.Parse(time.RFC3339, *req.ExpiresAt)
		if err != nil {
			return p, errors.New("expires_at must be RFC3339")
		}
		p.ExpiresAt = &t
	}
	return p, nil
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

