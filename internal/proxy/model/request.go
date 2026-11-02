package model

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
)

// maxRequestBytes is the Messages API's own limit on one request body.
const maxRequestBytes = 32 << 20

// The refusals of a request body, in the Messages API error shape. They
// name the rule and echo nothing of the request.
const (
	invalidBody  = `{"type":"error","error":{"type":"invalid_request_error","message":"forja: request body refused by the model proxy"}}`
	tooLargeBody = `{"type":"error","error":{"type":"request_too_large","message":"forja: request body over the model proxy's limit"}}`
)

var (
	errTooLarge = errors.New("model: request body too large")
	errRefused  = errors.New("model: request body refused")
)

// betas are the anthropic-beta values Claude Code sends in --bare mode, as
// recorded in docs/contracts/claude-code-headless/messages-request.md.
// Every other value is dropped: a beta is what enables a server-side
// feature, and some of those reach hosts the workspace names.
var betas = map[string]bool{
	"claude-code-20250219":            true,
	"interleaved-thinking-2025-05-14": true,
	"context-management-2025-06-27":   true,
	"per-turn-control-2026-07-01":     true,
	"advisor-tool-2026-03-01":         true,
	"thinking-token-count-2026-05-13": true,
	"effort-2025-11-24":               true,
}

// inlineSources are the content block sources whose bytes are in the body.
// Any other source, a URL or a file id, has the upstream fetch it.
var inlineSources = map[string]bool{"base64": true, "text": true, "content": true}

// checked is a request body the proxy accepted, in the form it forwards.
type checked struct {
	body      []byte
	maxTokens int64
}

// readBody reads r's body whole, bounded, and checks it.
func readBody(w http.ResponseWriter, r *http.Request) (checked, error) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRequestBytes))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return checked{}, errTooLarge
		}
		return checked{}, errRefused
	}
	return inspect(raw)
}

// inspect decides whether a body may go upstream, and returns the body the
// proxy re-encoded from what it read: a key that appears twice reaches the
// upstream once, as the proxy saw it.
func inspect(raw []byte) (checked, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	// The body's schema is the upstream's and open: the proxy checks a few
	// fields and forwards the rest as it parsed it.
	var body map[string]any
	if dec.Decode(&body) != nil || body == nil {
		return checked{}, errRefused
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return checked{}, errRefused
	}
	maxTokens, ok := positive(body["max_tokens"])
	if _, servers := body["mcp_servers"]; !ok || servers || !clientTools(body["tools"]) {
		return checked{}, errRefused
	}
	if !inlineMessages(body["messages"]) || !inlineBlocks(body["system"]) {
		return checked{}, errRefused
	}
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	if enc.Encode(body) != nil {
		return checked{}, errRefused
	}
	return checked{body: bytes.TrimSuffix(out.Bytes(), []byte("\n")), maxTokens: maxTokens}, nil
}

func positive(v any) (int64, bool) {
	n, ok := v.(json.Number)
	if !ok {
		return 0, false
	}
	i, err := n.Int64()
	return i, err == nil && i > 0
}

// clientTools reports whether every tool is one the client runs: no type,
// or the custom type. A server tool runs at the upstream, which may reach
// out on the model's behalf.
func clientTools(v any) bool {
	if v == nil {
		return true
	}
	tools, ok := v.([]any)
	if !ok {
		return false
	}
	for _, t := range tools {
		tool, ok := t.(map[string]any)
		if !ok {
			return false
		}
		if typ, typed := tool["type"]; typed && typ != "custom" {
			return false
		}
	}
	return true
}

func inlineMessages(v any) bool {
	messages, ok := v.([]any)
	if !ok {
		return v == nil
	}
	for _, m := range messages {
		if msg, ok := m.(map[string]any); ok && !inlineBlocks(msg["content"]) {
			return false
		}
	}
	return true
}

// inlineBlocks reports whether every source in a list of content blocks,
// and in the blocks nested in a tool result or a content document, carries
// its bytes in the body. Anything that is not a list of blocks has no
// source.
func inlineBlocks(v any) bool {
	blocks, ok := v.([]any)
	if !ok {
		return true
	}
	for _, b := range blocks {
		block, ok := b.(map[string]any)
		if !ok {
			continue
		}
		if !inlineSource(block) || !inlineBlocks(block["content"]) {
			return false
		}
	}
	return true
}

func inlineSource(block map[string]any) bool {
	v, has := block["source"]
	if !has {
		return true
	}
	source, ok := v.(map[string]any)
	if !ok {
		return false
	}
	typ, _ := source["type"].(string)
	return inlineSources[typ] && inlineBlocks(source["content"])
}

// allowedBetas is the comma-joined list of the allowlisted betas among the
// values of every anthropic-beta line, in order, or "" when none is.
func allowedBetas(lines []string) string {
	var kept []string
	for _, line := range lines {
		for _, beta := range strings.Split(line, ",") {
			if beta = strings.TrimSpace(beta); betas[beta] {
				kept = append(kept, beta)
			}
		}
	}
	return strings.Join(kept, ",")
}
