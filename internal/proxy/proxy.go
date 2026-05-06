// Package proxy is the OpenAI-compatible reverse proxy. One handler swaps
// the caller's sk-pxy-* key for the real OpenAI key, enforces per-key
// rate/quota limits, forwards the request, streams the response back, and
// records usage. Everything lives in this file because it is one handler
// and the helpers only make sense in that context.
package proxy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/aiminted/openai-proxy/internal/keys"
	"github.com/aiminted/openai-proxy/internal/pricing"
)

// captureMax is the trailing window of upstream response bytes we keep so we
// can extract the usage block (last SSE chunk or the JSON response body).
const captureMax = 2 * 1024 * 1024

type Deps struct {
	Upstream    *url.URL
	UpstreamKey func() string // looked up per request so rotations propagate
	Keys        *keys.Service
	Pricing     *pricing.Pricing
	Logger      *slog.Logger
	Timeout     time.Duration
}

type Proxy struct {
	deps   Deps
	client *http.Client
}

func New(d Deps) *Proxy {
	if d.Timeout == 0 {
		d.Timeout = 30 * time.Minute
	}
	return &Proxy{
		deps: d,
		client: &http.Client{
			Timeout: d.Timeout,
			Transport: &http.Transport{
				MaxIdleConns:          200,
				MaxIdleConnsPerHost:   100,
				IdleConnTimeout:       90 * time.Second,
				ResponseHeaderTimeout: 60 * time.Second,
			},
		},
	}
}

// ---------------------------------------------------------------------------
// request flow
// ---------------------------------------------------------------------------

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()

	key, err := p.authenticate(r)
	if err != nil {
		writeAuthError(w, err)
		return
	}

	if !p.checkLimits(r.Context(), w, key, r, start) {
		return
	}

	prepared, bodyReader, contentLength, err := readBody(r)
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "bad_request", "could not read request body")
		return
	}

	upReq, err := p.buildUpstreamRequest(r, bodyReader, contentLength, prepared.Modified)
	if err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "internal_error", "could not build upstream request")
		return
	}

	resp, err := p.client.Do(upReq)
	if err != nil {
		p.deps.Logger.Error("upstream request failed", "error", err, "path", r.URL.Path)
		writeOpenAIError(w, http.StatusBadGateway, "upstream_unreachable", "could not reach upstream")
		p.record(key, r, prepared.Model, prepared.IsStream, streamUsage{}, 0, http.StatusBadGateway, time.Since(start), err.Error())
		return
	}
	defer resp.Body.Close()

	tail := streamResponse(w, resp)
	usageData := extractUsage(prepared.IsStream, resp.Header.Get("Content-Type"), tail)
	model := prepared.Model
	if usageData.Model != "" {
		model = usageData.Model
	}
	cost := p.deps.Pricing.Cost(model, usageData.InputTokens, usageData.OutputTokens, usageData.CachedTokens)

	bg := context.Background()
	p.deps.Keys.TouchLastUsed(bg, key.ID)
	if usageData.InputTokens > 0 || usageData.OutputTokens > 0 {
		tokens := int64(usageData.InputTokens + usageData.OutputTokens)
		if err := p.deps.Keys.AddUsage(bg, key.ID, tokens, cost); err != nil {
			p.deps.Logger.Warn("usage add failed", "error", err, "key_id", key.ID)
		}
	}
	p.record(key, r, model, prepared.IsStream, usageData, cost, resp.StatusCode, time.Since(start), "")
}

func (p *Proxy) authenticate(r *http.Request) (*keys.Key, error) {
	plain, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || plain == "" {
		return nil, keys.ErrInvalid
	}
	return p.deps.Keys.Verify(r.Context(), plain)
}

func (p *Proxy) checkLimits(ctx context.Context, w http.ResponseWriter, key *keys.Key, r *http.Request, start time.Time) bool {
	if key.RPMLimit != nil {
		if ok, err := p.deps.Keys.AllowRate(ctx, key.ID, *key.RPMLimit); err != nil {
			p.deps.Logger.Error("ratelimit failed", "error", err)
		} else if !ok {
			writeOpenAIError(w, http.StatusTooManyRequests, "rate_limit_exceeded", "per-minute request limit exceeded")
			p.record(key, r, "", false, streamUsage{}, 0, http.StatusTooManyRequests, time.Since(start), "rate limit")
			return false
		}
	}
	if ok, err := p.deps.Keys.AllowQuota(ctx, key.ID, key.TokenQuota, key.DollarQuota); err != nil {
		p.deps.Logger.Error("quota check failed", "error", err)
	} else if !ok {
		writeOpenAIError(w, http.StatusTooManyRequests, "quota_exceeded", "token or dollar quota exceeded")
		p.record(key, r, "", false, streamUsage{}, 0, http.StatusTooManyRequests, time.Since(start), "quota exceeded")
		return false
	}
	return true
}

func (p *Proxy) buildUpstreamRequest(r *http.Request, body io.Reader, contentLength int64, bodyModified bool) (*http.Request, error) {
	target := *p.deps.Upstream
	target.Path = singleJoiningSlash(p.deps.Upstream.Path, r.URL.Path)
	target.RawQuery = r.URL.RawQuery

	req, err := http.NewRequestWithContext(r.Context(), r.Method, target.String(), body)
	if err != nil {
		return nil, err
	}
	copyHeaders(req.Header, r.Header)
	req.Header.Set("Authorization", "Bearer "+p.deps.UpstreamKey())
	req.Header.Del("X-Forwarded-For")
	req.Header.Del("X-Forwarded-Host")
	// Let Go's Transport handle gzip transparently — without this, the tail
	// buffer captures raw gzip bytes and usage parsing silently fails.
	req.Header.Del("Accept-Encoding")
	req.Host = p.deps.Upstream.Host
	req.ContentLength = contentLength
	if bodyModified {
		req.Header.Set("Content-Length", strconv.FormatInt(contentLength, 10))
	}
	return req, nil
}

func (p *Proxy) record(key *keys.Key, r *http.Request, model string, streaming bool, u streamUsage, cost float64, status int, dur time.Duration, errMsg string) {
	err := p.deps.Keys.RecordRequest(context.Background(), keys.UsageRecord{
		KeyID:        key.ID,
		RequestID:    r.Header.Get("X-Request-Id"),
		Endpoint:     r.URL.Path,
		Model:        model,
		Streaming:    streaming,
		InputTokens:  u.InputTokens,
		OutputTokens: u.OutputTokens,
		CachedTokens: u.CachedTokens,
		CostUSD:      cost,
		Status:       status,
		Duration:     dur,
		ErrorMessage: errMsg,
	})
	if err != nil {
		p.deps.Logger.Warn("usage record failed", "error", err, "key_id", key.ID)
	}
}

// ---------------------------------------------------------------------------
// request body: read/inject stream_options
// ---------------------------------------------------------------------------

// preparedBody is the result of preprocessing a request body. For JSON we
// inspect the model and stream flag and force include_usage so streaming
// responses still expose token counts.
type preparedBody struct {
	Body     []byte
	IsStream bool
	Model    string
	Modified bool
	WasJSON  bool
}

func (pb *preparedBody) Reader() io.Reader { return bytes.NewReader(pb.Body) }

// PrepareBody is exported for tests.
func PrepareBody(r io.Reader) (*preparedBody, error) {
	if r == nil {
		return &preparedBody{}, nil
	}
	raw, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	pb := &preparedBody{Body: raw}
	if len(raw) == 0 {
		return pb, nil
	}

	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return pb, nil
	}
	pb.WasJSON = true

	if m, ok := doc["model"].(string); ok {
		pb.Model = m
	}
	if s, ok := doc["stream"].(bool); ok && s {
		pb.IsStream = true
		opts, _ := doc["stream_options"].(map[string]any)
		if opts == nil {
			opts = map[string]any{}
		}
		if iu, _ := opts["include_usage"].(bool); !iu {
			opts["include_usage"] = true
			doc["stream_options"] = opts
			pb.Modified = true
		}
	}

	if pb.Modified {
		out, err := json.Marshal(doc)
		if err != nil {
			return nil, err
		}
		pb.Body = out
	}
	return pb, nil
}

// readBody parses JSON request bodies so we can inject stream_options.
// Non-JSON (multipart, binary) flows straight through with no buffering.
func readBody(r *http.Request) (*preparedBody, io.Reader, int64, error) {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		return &preparedBody{}, r.Body, r.ContentLength, nil
	}
	pb, err := PrepareBody(r.Body)
	if err != nil {
		return nil, nil, 0, err
	}
	return pb, pb.Reader(), int64(len(pb.Body)), nil
}

// ---------------------------------------------------------------------------
// response: stream with tee + extract usage
// ---------------------------------------------------------------------------

// streamUsage is the usage block extracted from the response.
type streamUsage struct {
	InputTokens  int
	OutputTokens int
	CachedTokens int
	Model        string
}

// streamResponse copies headers + body from upstream to the client, flushing
// after every chunk so SSE works, while keeping a trailing window for usage.
func streamResponse(w http.ResponseWriter, resp *http.Response) *tailBuffer {
	for k, vs := range resp.Header {
		// Drop upstream CORS headers; our own CORS middleware sets them and
		// duplicating both values trips browser preflight checks.
		if upstreamSkipHeaders[http.CanonicalHeaderKey(k)] {
			continue
		}
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)

	flusher, _ := w.(http.Flusher)
	tail := newTailBuffer(captureMax)
	_, _ = io.Copy(io.MultiWriter(&flushWriter{w: w, f: flusher}, tail), resp.Body)
	return tail
}

func extractUsage(isStream bool, contentType string, tail *tailBuffer) streamUsage {
	if isStream || strings.Contains(contentType, "event-stream") {
		u, _ := ParseSSEUsage(tail.Bytes())
		return u
	}
	u, _ := ParseJSONUsage(tail.Bytes())
	return u
}

// ParseSSEUsage extracts usage from an SSE buffer. Exported for tests.
func ParseSSEUsage(buf []byte) (streamUsage, bool) {
	var found bool
	var out streamUsage
	scanner := bufio.NewScanner(bytes.NewReader(buf))
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		payload := bytes.TrimSpace(line[5:])
		if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
			continue
		}
		var doc usageEnvelope
		if err := json.Unmarshal(payload, &doc); err != nil {
			continue
		}
		if doc.Model != "" {
			out.Model = doc.Model
		}
		if doc.Usage == nil {
			continue
		}
		out.InputTokens = doc.Usage.PromptTokens
		out.OutputTokens = doc.Usage.CompletionTokens
		if doc.Usage.PromptTokensDetails != nil {
			out.CachedTokens = doc.Usage.PromptTokensDetails.CachedTokens
		}
		found = true
	}
	return out, found
}

// ParseJSONUsage extracts usage from a non-streaming JSON response. Exported for tests.
func ParseJSONUsage(body []byte) (streamUsage, bool) {
	var doc usageEnvelope
	if err := json.Unmarshal(body, &doc); err != nil {
		return streamUsage{}, false
	}
	if doc.Usage == nil {
		return streamUsage{Model: doc.Model}, false
	}
	out := streamUsage{
		InputTokens:  doc.Usage.PromptTokens,
		OutputTokens: doc.Usage.CompletionTokens,
		Model:        doc.Model,
	}
	if doc.Usage.PromptTokensDetails != nil {
		out.CachedTokens = doc.Usage.PromptTokensDetails.CachedTokens
	}
	return out, true
}

type usageEnvelope struct {
	Model string `json:"model"`
	Usage *struct {
		PromptTokens         int `json:"prompt_tokens"`
		CompletionTokens     int `json:"completion_tokens"`
		PromptTokensDetails  *struct {
			CachedTokens int `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
	} `json:"usage"`
}

// ---------------------------------------------------------------------------
// header policy
// ---------------------------------------------------------------------------

var hopByHop = map[string]bool{
	"Connection":          true,
	"Proxy-Connection":    true,
	"Keep-Alive":          true,
	"Te":                  true,
	"Trailer":             true,
	"Transfer-Encoding":   true,
	"Upgrade":             true,
	"Proxy-Authorization": true,
	"Authorization":       true,
	"Host":                true,
}

// upstreamSkipHeaders are response headers we never forward from upstream.
// CORS in particular: our middleware sets them based on our allowlist; the
// upstream's values would duplicate and break browsers.
var upstreamSkipHeaders = map[string]bool{
	"Access-Control-Allow-Origin":      true,
	"Access-Control-Allow-Methods":     true,
	"Access-Control-Allow-Headers":     true,
	"Access-Control-Allow-Credentials": true,
	"Access-Control-Expose-Headers":    true,
	"Access-Control-Max-Age":           true,
}

func copyHeaders(dst, src http.Header) {
	for k, vs := range src {
		if hopByHop[http.CanonicalHeaderKey(k)] {
			continue
		}
		for _, v := range vs {
			dst.Add(k, v)
		}
	}
}

func singleJoiningSlash(a, b string) string {
	switch {
	case strings.HasSuffix(a, "/") && strings.HasPrefix(b, "/"):
		return a + b[1:]
	case !strings.HasSuffix(a, "/") && !strings.HasPrefix(b, "/"):
		return a + "/" + b
	}
	return a + b
}

// ---------------------------------------------------------------------------
// response writer helpers
// ---------------------------------------------------------------------------

// flushWriter wraps a ResponseWriter and flushes after every Write so SSE
// chunks reach the client immediately.
type flushWriter struct {
	w http.ResponseWriter
	f http.Flusher
}

func (fw *flushWriter) Write(p []byte) (int, error) {
	n, err := fw.w.Write(p)
	if fw.f != nil {
		fw.f.Flush()
	}
	return n, err
}

// tailBuffer is a fixed-capacity sliding window: the last `cap` bytes are
// retained, enough to inspect a JSON response body or the trailing usage
// chunk of an SSE stream.
type tailBuffer struct {
	cap int
	buf []byte
}

func newTailBuffer(c int) *tailBuffer { return &tailBuffer{cap: c, buf: make([]byte, 0, 4096)} }

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.cap {
		t.buf = t.buf[len(t.buf)-t.cap:]
	}
	return len(p), nil
}

func (t *tailBuffer) Bytes() []byte { return t.buf }

func writeOpenAIError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{"message": message, "type": code, "code": code},
	})
}

func writeAuthError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, keys.ErrInactive):
		writeOpenAIError(w, http.StatusUnauthorized, "key_inactive", "this key has been revoked")
	case errors.Is(err, keys.ErrExpired):
		writeOpenAIError(w, http.StatusUnauthorized, "key_expired", "this key has expired")
	case errors.Is(err, keys.ErrNotFound), errors.Is(err, keys.ErrInvalid):
		writeOpenAIError(w, http.StatusUnauthorized, "invalid_api_key", "invalid api key")
	default:
		writeOpenAIError(w, http.StatusInternalServerError, "internal_error", "auth backend error")
	}
}
