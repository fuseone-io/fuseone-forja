package model_test

import (
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
)

// post sends body to /v1/messages with the run token, lets edit change the
// request, and returns the answer with its body read.
func post(t *testing.T, p *proxyUnderTest, body string, edit func(*http.Request)) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, "http://"+p.addr+"/v1/messages", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("x-api-key", runToken)
	if edit != nil {
		edit(req)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /v1/messages: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	answer, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read answer: %v", err)
	}
	return res, string(answer)
}

// A body that would have the model API reach a host of the workspace's
// choosing, or that the proxy cannot meter, is refused before the upstream:
// the workspace's egress ends at the proxies.
func TestProxy_bodyTheProxyCannotVouchFor_400NoUpstream(t *testing.T) {
	t.Parallel()
	const msg = `"messages":[{"role":"user","content":"hi"}]`
	tests := map[string]string{
		"not json":                 `max_tokens=10`,
		"not an object":            `[{"max_tokens":10}]`,
		"trailing data":            `{"max_tokens":10,` + msg + `}{"mcp_servers":[]}`,
		"no max_tokens":            `{` + msg + `}`,
		"zero max_tokens":          `{"max_tokens":0,` + msg + `}`,
		"negative max_tokens":      `{"max_tokens":-5,` + msg + `}`,
		"fractional max_tokens":    `{"max_tokens":1.5,` + msg + `}`,
		"max_tokens as a string":   `{"max_tokens":"10",` + msg + `}`,
		"mcp_servers":              `{"max_tokens":10,` + msg + `,"mcp_servers":[{"type":"url","url":"https://attacker.example/x","name":"x"}]}`,
		"mcp_servers null":         `{"max_tokens":10,` + msg + `,"mcp_servers":null}`,
		"web_fetch server tool":    `{"max_tokens":10,` + msg + `,"tools":[{"type":"web_fetch_20250910","name":"web_fetch"}]}`,
		"web_search server tool":   `{"max_tokens":10,` + msg + `,"tools":[{"name":"Read","input_schema":{"type":"object"}},{"type":"web_search_20250305","name":"web_search"}]}`,
		"mcp toolset":              `{"max_tokens":10,` + msg + `,"tools":[{"type":"mcp_toolset","mcp_server_name":"x"}]}`,
		"tool type not a string":   `{"max_tokens":10,` + msg + `,"tools":[{"type":7,"name":"x"}]}`,
		"tools not a list":         `{"max_tokens":10,` + msg + `,"tools":{"type":"web_fetch_20250910"}}`,
		"image from a url":         `{"max_tokens":10,"messages":[{"role":"user","content":[{"type":"image","source":{"type":"url","url":"https://attacker.example/i.png?d=tree"}}]}]}`,
		"document from a url":      `{"max_tokens":10,"messages":[{"role":"user","content":[{"type":"document","source":{"type":"url","url":"https://attacker.example/d.pdf"}}]}]}`,
		"file reference":           `{"max_tokens":10,"messages":[{"role":"user","content":[{"type":"document","source":{"type":"file","file_id":"file_0"}}]}]}`,
		"url inside a tool result": `{"max_tokens":10,"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":[{"type":"image","source":{"type":"url","url":"https://attacker.example/i"}}]}]}]}`,
		"url inside a document":    `{"max_tokens":10,"messages":[{"role":"user","content":[{"type":"document","source":{"type":"content","content":[{"type":"image","source":{"type":"url","url":"https://attacker.example/i"}}]}}]}]}`,
		"url in the system prompt": `{"max_tokens":10,` + msg + `,"system":[{"type":"image","source":{"type":"url","url":"https://attacker.example/i"}}]}`,
		"source not an object":     `{"max_tokens":10,"messages":[{"role":"user","content":[{"type":"image","source":"https://attacker.example/i"}]}]}`,
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			up := newUpstream(t, replySSE(domain.TokenUsage{Input: 1, Output: 1}))
			p := startProxy(t, up, 1_000_000)

			res, answer := post(t, p, body, nil)
			if res.StatusCode != http.StatusBadRequest || len(up.requests()) != 0 {
				t.Fatalf("%s = %d with %d upstream calls, want 400 and none", name, res.StatusCode, len(up.requests()))
			}
			if !strings.Contains(answer, `"invalid_request_error"`) || strings.Contains(answer, "attacker") {
				t.Fatalf("refusal body = %s, want an invalid_request_error that echoes nothing of the request", answer)
			}
		})
	}
}

// What Claude Code sends, and the content blocks a run needs, still pass:
// the check refuses what reaches out, not what merely has a type.
func TestProxy_bodyOfClientToolsAndInlineContent_forwarded(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"untyped tools": `{"max_tokens":10,"messages":[{"role":"user","content":"hi"}],` +
			`"tools":[{"name":"Bash","description":"execute shell commands","input_schema":{"type":"object","properties":{"source":{"type":"string"}}}}]}`,
		"custom tool": `{"max_tokens":10,"messages":[{"role":"user","content":"hi"}],"tools":[{"type":"custom","name":"Read","input_schema":{"type":"object"}}]}`,
		"inline image in a tool result": `{"max_tokens":10,"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t",` +
			`"content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"iVBORw0KGgo="}}]}]}]}`,
		"text and content documents": `{"max_tokens":10,"messages":[{"role":"user","content":[` +
			`{"type":"document","source":{"type":"text","media_type":"text/plain","data":"forja"}},` +
			`{"type":"document","source":{"type":"content","content":[{"type":"text","text":"forja"}]}}]}]}`,
		"tool input naming a url source": `{"max_tokens":10,"messages":[{"role":"user","content":"hi"},` +
			`{"role":"assistant","content":[{"type":"tool_use","id":"t","name":"Bash","input":{"source":{"type":"url","url":"https://example.com"}}}]}]}`,
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			up := newUpstream(t, replySSE(domain.TokenUsage{Input: 1, Output: 1}))
			p := startProxy(t, up, 1_000_000)

			if res, answer := post(t, p, body, nil); res.StatusCode != http.StatusOK || len(up.requests()) != 1 {
				t.Fatalf("%s = %d %s with %d upstream calls, want 200 and one", name, res.StatusCode, answer, len(up.requests()))
			}
		})
	}
}

// A key repeated in the body is read one way by the proxy and maybe another
// by the upstream; the upstream is sent the body the proxy checked, so the
// copy that was never checked never reaches it.
func TestProxy_duplicateKeys_upstreamGetsTheCheckedBody(t *testing.T) {
	t.Parallel()
	up := newUpstream(t, replySSE(domain.TokenUsage{Input: 1, Output: 1}))
	p := startProxy(t, up, 1_000_000)
	body := `{"max_tokens":10,"messages":[{"role":"user","content":"hi"}],` +
		`"tools":[{"type":"web_fetch_20250910","name":"web_fetch"}],"tools":[{"name":"Read","input_schema":{"type":"object"}}]}`

	if res, _ := post(t, p, body, nil); res.StatusCode != http.StatusOK || len(up.requests()) != 1 {
		t.Fatalf("status %d with %d upstream calls, want 200 and one", res.StatusCode, len(up.requests()))
	}
	got, _ := io.ReadAll(up.requests()[0].Body)
	if strings.Contains(string(got), "web_fetch") {
		t.Fatalf("upstream body = %s, want only the tools the proxy checked", got)
	}
	var sent struct {
		MaxTokens int64 `json:"max_tokens"`
		Tools     []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(got, &sent); err != nil || sent.MaxTokens != 10 || len(sent.Tools) != 1 || sent.Tools[0].Name != "Read" {
		t.Fatalf("upstream body = %s (%v), want max_tokens 10 and the Read tool", got, err)
	}
}

// The upstream reads the anthropic-beta values Claude Code sends and none
// other: a beta is what turns on a server-side feature that reaches out.
func TestProxy_betas_onlyClaudeCodesForwarded(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		sent []string
		want []string
	}{
		"claude code's list": {
			sent: []string{"claude-code-20250219,interleaved-thinking-2025-05-14,context-management-2025-06-27,per-turn-control-2026-07-01,advisor-tool-2026-03-01,thinking-token-count-2026-05-13,effort-2025-11-24"},
			want: []string{"claude-code-20250219,interleaved-thinking-2025-05-14,context-management-2025-06-27,per-turn-control-2026-07-01,advisor-tool-2026-03-01,thinking-token-count-2026-05-13,effort-2025-11-24"},
		},
		"unknown betas dropped": {
			sent: []string{"claude-code-20250219, mcp-client-2025-04-04,effort-2025-11-24", "web-fetch-2025-09-10"},
			want: []string{"claude-code-20250219,effort-2025-11-24"},
		},
		"second header line kept": {
			sent: []string{"files-api-2025-04-14", "effort-2025-11-24"},
			want: []string{"effort-2025-11-24"},
		},
		"none allowed":  {sent: []string{"mcp-client-2025-04-04"}, want: nil},
		"other case":    {sent: []string{"Effort-2025-11-24"}, want: nil},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			up := newUpstream(t, replySSE(domain.TokenUsage{Input: 1, Output: 1}))
			p := startProxy(t, up, 1_000_000)
			post(t, p, `{"max_tokens":10,"messages":[]}`, func(r *http.Request) {
				for _, v := range tt.sent {
					r.Header.Add("anthropic-beta", v)
				}
			})

			if len(up.requests()) != 1 {
				t.Fatalf("upstream got %d requests, want one", len(up.requests()))
			}
			if got := up.requests()[0].Header.Values("anthropic-beta"); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("upstream anthropic-beta = %q, want %q", got, tt.want)
			}
		})
	}
}

// A body past the Messages API's own limit is refused without the
// upstream: the proxy reads every body whole before it decides.
func TestProxy_bodyOverLimit_413NoUpstream(t *testing.T) {
	t.Parallel()
	up := newUpstream(t, replySSE(domain.TokenUsage{Input: 1, Output: 1}))
	p := startProxy(t, up, 1<<40)
	body := `{"max_tokens":10,"messages":[{"role":"user","content":"` + strings.Repeat("a", 32<<20) + `"}]}`

	if res, _ := post(t, p, body, nil); res.StatusCode != http.StatusRequestEntityTooLarge || len(up.requests()) != 0 {
		t.Fatalf("status %d with %d upstream calls, want 413 and none", res.StatusCode, len(up.requests()))
	}
}

// A request whose max_tokens the cap left cannot cover is the cap reached:
// refused without the upstream, with the cap line, and sticky. One that
// fits exactly still goes.
func TestProxy_maxTokensPastCapLeft_capReached(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		maxTokens string
		want      int
	}{
		"fits exactly":      {"100", http.StatusOK},
		"one past":          {"101", http.StatusTooManyRequests},
		"past int64 total":  {"9223372036854775807", http.StatusTooManyRequests},
		"past int64 itself": {"9223372036854775808", http.StatusBadRequest},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			up := newUpstream(t, replySSE(domain.TokenUsage{}))
			p := startProxy(t, up, 100)

			res, answer := post(t, p, `{"max_tokens":`+tt.maxTokens+`,"messages":[]}`, nil)
			if res.StatusCode != tt.want {
				t.Fatalf("max_tokens %s under a cap of 100 = %d %s, want %d", tt.maxTokens, res.StatusCode, answer, tt.want)
			}
			if tt.want != http.StatusTooManyRequests {
				return
			}
			if answer != capBody || len(up.requests()) != 0 || !readUsage(t, p).Capped {
				t.Fatalf("refusal = %q with %d upstream calls, capped %v; want the cap body, none and the cap line", answer, len(up.requests()), readUsage(t, p).Capped)
			}
			assertCapRefusal(t, p, 2)
		})
	}
}

// Requests sent together while the total is still 0 are forwarded one at a
// time, so the first response's usage is counted before the next is
// admitted: the workspace cannot spend past the cap by sending in parallel.
func TestProxy_parallelRequests_capCountedBetweenThem(t *testing.T) {
	t.Parallel()
	one := domain.TokenUsage{Input: 60, Output: 40}
	up := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		// Hold the answer long enough for every request to reach the proxy.
		time.Sleep(300 * time.Millisecond)
		replySSE(one)(w, r)
	})
	p := startProxy(t, up, one.Total())

	const n = 5
	statuses := make(chan int, n)
	var wg sync.WaitGroup
	for range n {
		wg.Go(func() {
			res, _ := post(t, p, `{"max_tokens":40,"messages":[]}`, nil)
			statuses <- res.StatusCode
		})
	}
	wg.Wait()
	close(statuses)
	count := map[int]int{}
	for s := range statuses {
		count[s]++
	}
	if len(up.requests()) != 1 || count[http.StatusOK] != 1 || count[http.StatusTooManyRequests] != n-1 {
		t.Fatalf("upstream got %d of %d parallel requests, statuses %v; want one forwarded and the rest refused at the cap", len(up.requests()), n, count)
	}
	if report := readUsage(t, p); report.Tokens != one || !report.Capped {
		t.Fatalf("report = %+v, want one response's %+v and capped", report, one)
	}
}
