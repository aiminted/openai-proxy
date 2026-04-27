package proxy

import (
	"encoding/json"
	"net/http"
)

// flushWriter wraps a ResponseWriter and flushes after every Write so SSE chunks
// reach the client immediately.
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

// tailBuffer is a fixed-capacity sliding window over written bytes. Only the
// most recent `cap` bytes are retained, which is enough to inspect a JSON
// response or the trailing usage chunk of an SSE stream.
type tailBuffer struct {
	cap int
	buf []byte
}

func newTailBuffer(cap int) *tailBuffer {
	return &tailBuffer{cap: cap, buf: make([]byte, 0, 4096)}
}

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
	body := map[string]any{
		"error": map[string]any{
			"message": message,
			"type":    code,
			"code":    code,
		},
	}
	_ = json.NewEncoder(w).Encode(body)
}
