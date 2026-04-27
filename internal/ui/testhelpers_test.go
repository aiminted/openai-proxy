package ui

import (
	"net/http"
	"net/url"
	"strings"
)

// headerRecorder is a minimal http.ResponseWriter that captures Set-Cookie
// headers so flash tests can inspect what would be sent to the client without
// spinning up an httptest.Server.
type headerRecorder struct {
	header     http.Header
	statusCode int
}

func (h *headerRecorder) Header() http.Header { return h.header }
func (h *headerRecorder) Write(b []byte) (int, error) { return len(b), nil }
func (h *headerRecorder) WriteHeader(code int) { h.statusCode = code }

func (h *headerRecorder) cookies() map[string]string {
	out := map[string]string{}
	for _, raw := range h.header.Values("Set-Cookie") {
		parts := strings.SplitN(raw, ";", 2)
		nv := strings.SplitN(parts[0], "=", 2)
		if len(nv) == 2 {
			out[nv[0]] = nv[1]
		}
	}
	return out
}

func (h *headerRecorder) headerString(name string) string {
	return strings.Join(h.header.Values(name), "\n")
}

func newReqWithCookie(name, value string) *http.Request {
	u, _ := url.Parse("http://x/")
	req := &http.Request{URL: u, Header: http.Header{}}
	req.AddCookie(&http.Cookie{Name: name, Value: value})
	return req
}
