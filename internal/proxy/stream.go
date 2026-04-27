package proxy

import (
	"bufio"
	"bytes"
	"encoding/json"
)

// StreamUsage is the usage block extracted from the final SSE chunk.
type StreamUsage struct {
	InputTokens  int
	OutputTokens int
	CachedTokens int
	Model        string
}

// ParseSSEUsage extracts the OpenAI usage block from a complete SSE buffer.
// It scans every `data:` line and returns the last one that contains a usage
// object (OpenAI emits this only on the very last chunk when
// stream_options.include_usage is true).
func ParseSSEUsage(buf []byte) (StreamUsage, bool) {
	var found bool
	var out StreamUsage
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
		var doc struct {
			Model string `json:"model"`
			Usage *struct {
				PromptTokens         int `json:"prompt_tokens"`
				CompletionTokens     int `json:"completion_tokens"`
				PromptTokensDetails  *struct {
					CachedTokens int `json:"cached_tokens"`
				} `json:"prompt_tokens_details"`
			} `json:"usage"`
		}
		if err := json.Unmarshal(payload, &doc); err != nil {
			continue
		}
		if doc.Usage == nil {
			if doc.Model != "" {
				out.Model = doc.Model
			}
			continue
		}
		out.InputTokens = doc.Usage.PromptTokens
		out.OutputTokens = doc.Usage.CompletionTokens
		if doc.Usage.PromptTokensDetails != nil {
			out.CachedTokens = doc.Usage.PromptTokensDetails.CachedTokens
		}
		if doc.Model != "" {
			out.Model = doc.Model
		}
		found = true
	}
	return out, found
}

// ParseJSONUsage pulls usage info from a non-streaming JSON response body.
func ParseJSONUsage(body []byte) (StreamUsage, bool) {
	var doc struct {
		Model string `json:"model"`
		Usage *struct {
			PromptTokens         int `json:"prompt_tokens"`
			CompletionTokens     int `json:"completion_tokens"`
			TotalTokens          int `json:"total_tokens"`
			PromptTokensDetails  *struct {
				CachedTokens int `json:"cached_tokens"`
			} `json:"prompt_tokens_details"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return StreamUsage{}, false
	}
	if doc.Usage == nil {
		return StreamUsage{Model: doc.Model}, false
	}
	out := StreamUsage{
		InputTokens:  doc.Usage.PromptTokens,
		OutputTokens: doc.Usage.CompletionTokens,
		Model:        doc.Model,
	}
	if doc.Usage.PromptTokensDetails != nil {
		out.CachedTokens = doc.Usage.PromptTokensDetails.CachedTokens
	}
	return out, true
}
