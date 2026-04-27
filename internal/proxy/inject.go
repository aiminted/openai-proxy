package proxy

import (
	"bytes"
	"encoding/json"
	"io"
)

// PreparedBody is the result of preprocessing a request body so we can reason
// about streaming flags and the requested model without losing the payload.
type PreparedBody struct {
	Body      []byte
	IsStream  bool
	Model     string
	Modified  bool
	WasJSON   bool
}

// PrepareBody reads the body, attempts to parse it as JSON, captures `model` and
// `stream`, and forces `stream_options.include_usage = true` when streaming so
// we can attribute usage. If the body is not JSON (multipart, binary, empty),
// it is returned unchanged.
func PrepareBody(r io.Reader) (*PreparedBody, error) {
	if r == nil {
		return &PreparedBody{}, nil
	}
	raw, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	pb := &PreparedBody{Body: raw}
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

// Reader returns a fresh io.Reader over the prepared body.
func (pb *PreparedBody) Reader() io.Reader {
	return bytes.NewReader(pb.Body)
}
