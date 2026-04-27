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

	"github.com/in-jun/openai-proxy/internal/keys"
)

type API struct {
	keys *keys.Service
	db   *pgxpool.Pool
}

func NewAPI(k *keys.Service, db *pgxpool.Pool) *API {
	return &API{keys: k, db: db}
}

func (a *API) Mount(mux *http.ServeMux) {
	mux.HandleFunc("GET /admin/api/keys", a.listKeys)
	mux.HandleFunc("POST /admin/api/keys", a.createKey)
	mux.HandleFunc("GET /admin/api/keys/{id}", a.getKey)
	mux.HandleFunc("PATCH /admin/api/keys/{id}", a.updateKey)
	mux.HandleFunc("DELETE /admin/api/keys/{id}", a.deleteKey)
	mux.HandleFunc("POST /admin/api/keys/{id}/active", a.toggleActive)
	mux.HandleFunc("GET /admin/api/usage", a.usageSummary)
	mux.HandleFunc("GET /admin/api/usage/{id}/recent", a.usageRecent)
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

