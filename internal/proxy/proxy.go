package proxy

import (
	"context"
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
	"github.com/aiminted/openai-proxy/internal/quota"
	"github.com/aiminted/openai-proxy/internal/ratelimit"
	"github.com/aiminted/openai-proxy/internal/usage"
)

// captureMax is the trailing window of upstream response bytes we keep so we
// can extract the usage block (last SSE chunk or the JSON response body).
const captureMax = 2 * 1024 * 1024

type Deps struct {
	Upstream    *url.URL
	UpstreamKey string
	Keys        *keys.Service
	Limiter     *ratelimit.Limiter
	Quota       *quota.Tracker
	Pricing     *pricing.Pricing
	Recorder    *usage.Recorder
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
		p.record(key, r, prepared.Model, prepared.IsStream, StreamUsage{}, 0, http.StatusBadGateway, time.Since(start), err.Error())
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
		if err := p.deps.Quota.Add(bg, key.ID, tokens, cost); err != nil {
			p.deps.Logger.Warn("quota add failed", "error", err, "key_id", key.ID)
		}
	}
	p.record(key, r, model, prepared.IsStream, usageData, cost, resp.StatusCode, time.Since(start), "")
}

func (p *Proxy) authenticate(r *http.Request) (*keys.Key, error) {
	auth := r.Header.Get("Authorization")
	plain, ok := strings.CutPrefix(auth, "Bearer ")
	if !ok || plain == "" {
		return nil, keys.ErrInvalid
	}
	return p.deps.Keys.Verify(r.Context(), plain)
}

func (p *Proxy) checkLimits(ctx context.Context, w http.ResponseWriter, key *keys.Key, r *http.Request, start time.Time) bool {
	if key.RPMLimit != nil {
		ok, _, err := p.deps.Limiter.Allow(ctx, key.ID, *key.RPMLimit)
		if err != nil {
			p.deps.Logger.Error("ratelimit failed", "error", err)
		} else if !ok {
			writeOpenAIError(w, http.StatusTooManyRequests, "rate_limit_exceeded", "per-minute request limit exceeded")
			p.record(key, r, "", false, StreamUsage{}, 0, http.StatusTooManyRequests, time.Since(start), "rate limit")
			return false
		}
	}
	if ok, err := p.deps.Quota.Allow(ctx, key.ID, key.TokenQuota, key.DollarQuota); err != nil {
		p.deps.Logger.Error("quota check failed", "error", err)
	} else if !ok {
		writeOpenAIError(w, http.StatusTooManyRequests, "quota_exceeded", "token or dollar quota exceeded")
		p.record(key, r, "", false, StreamUsage{}, 0, http.StatusTooManyRequests, time.Since(start), "quota exceeded")
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
	req.Header.Set("Authorization", "Bearer "+p.deps.UpstreamKey)
	req.Header.Del("X-Forwarded-For")
	req.Header.Del("X-Forwarded-Host")
	// Remove Accept-Encoding so Go's Transport handles gzip transparently:
	// it negotiates compression with the upstream and decompresses the body
	// for us. Without this, the tail buffer captures raw gzip bytes and the
	// usage block can't be extracted from chat/embeddings responses.
	req.Header.Del("Accept-Encoding")
	req.Host = p.deps.Upstream.Host
	req.ContentLength = contentLength
	if bodyModified {
		req.Header.Set("Content-Length", strconv.FormatInt(contentLength, 10))
	}
	return req, nil
}

func (p *Proxy) record(key *keys.Key, r *http.Request, model string, streaming bool, u StreamUsage, cost float64, status int, dur time.Duration, errMsg string) {
	err := p.deps.Recorder.Record(context.Background(), usage.Record{
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

// readBody parses the request body when it's JSON so we can inject
// stream_options.include_usage; non-JSON bodies (multipart uploads, binary)
// stream straight through without buffering.
func readBody(r *http.Request) (*PreparedBody, io.Reader, int64, error) {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		return &PreparedBody{}, r.Body, r.ContentLength, nil
	}
	pb, err := PrepareBody(r.Body)
	if err != nil {
		return nil, nil, 0, err
	}
	return pb, pb.Reader(), int64(len(pb.Body)), nil
}

// streamResponse copies headers + body from upstream to the client, flushing
// after every chunk for SSE, while keeping the trailing window for usage.
func streamResponse(w http.ResponseWriter, resp *http.Response) *tailBuffer {
	for k, vs := range resp.Header {
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

func extractUsage(isStream bool, contentType string, tail *tailBuffer) StreamUsage {
	if isStream || strings.Contains(contentType, "event-stream") {
		u, _ := ParseSSEUsage(tail.Bytes())
		return u
	}
	u, _ := ParseJSONUsage(tail.Bytes())
	return u
}

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
