// Command fakemodel is a stand-in for the Messages API that the workspace
// tests run on the default bridge, where a run's model proxy reaches it as
// its upstream. Its responses copy the stream Claude Code accepted in
// docs/contracts/claude-code-headless/streaming-and-usage.md and, for the
// tool turn, Claude Code's own Bash tool_use sequence.
//
// It answers POST /v1/messages only when x-api-key equals FAKE_KEY. With
// FAKE_BASH set, a request with no assistant turn yet gets one Bash
// tool_use running that command; every other request gets an end_turn
// text. It prints one JSON line per response with the usage it sent, so a
// test can sum what the proxy should have metered.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sync"
)

type usage struct {
	Input         int64 `json:"input"`
	CacheCreation int64 `json:"cache_creation"`
	CacheRead     int64 `json:"cache_read"`
	Output        int64 `json:"output"`
}

type fake struct {
	key, bash string
	mu        sync.Mutex
	served    int64
}

func main() {
	f := &fake{key: os.Getenv("FAKE_KEY"), bash: os.Getenv("FAKE_BASH")}
	http.HandleFunc("/v1/messages", f.messages)
	fmt.Println(`{"listening":":8000"}`)
	log.Fatal(http.ListenAndServe(":8000", nil))
}

func (f *fake) messages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.Header.Get("x-api-key") != f.key {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	var body struct {
		Messages []struct {
			Role string `json:"role"`
		} `json:"messages"`
	}
	raw, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(raw, &body)
	turns := 0
	for _, m := range body.Messages {
		if m.Role == "assistant" {
			turns++
		}
	}
	f.mu.Lock()
	n := f.served
	f.served++
	f.mu.Unlock()
	u := usage{Input: 100 + n, CacheCreation: 10, CacheRead: 20, Output: 7}
	toolUse := f.bash != "" && turns == 0
	if toolUse {
		u.Output = 11
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	for _, ev := range events(n, u, toolUse, f.bash) {
		_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev[0], ev[1])
		w.(http.Flusher).Flush()
	}
	line, _ := json.Marshal(u)
	fmt.Println(string(line))
}

func events(n int64, u usage, toolUse bool, bash string) [][2]string {
	start := fmt.Sprintf(`{"type":"message_start","message":{"id":"msg_forja_fake_%d","type":"message","role":"assistant","model":"claude-opus-5-5","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":%d,"output_tokens":1,"cache_creation_input_tokens":%d,"cache_read_input_tokens":%d}}}`,
		n, u.Input, u.CacheCreation, u.CacheRead)
	stop := "end_turn"
	block := [][2]string{
		{"content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`},
		{"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"forja-fake-done"}}`},
	}
	if toolUse {
		stop = "tool_use"
		input, _ := json.Marshal(map[string]string{"command": bash, "description": "Commit a proof file"})
		partial, _ := json.Marshal(string(input))
		block = [][2]string{
			{"content_block_start", fmt.Sprintf(`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_forja_%04d","name":"Bash","input":{}}}`, n)},
			{"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":""}}`},
			{"content_block_delta", fmt.Sprintf(`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":%s}}`, partial)},
		}
	}
	out := [][2]string{{"message_start", start}}
	out = append(out, block...)
	return append(out,
		[2]string{"content_block_stop", `{"type":"content_block_stop","index":0}`},
		[2]string{"message_delta", fmt.Sprintf(`{"type":"message_delta","delta":{"stop_reason":%q,"stop_sequence":null},"usage":{"output_tokens":%d}}`, stop, u.Output)},
		[2]string{"message_stop", `{"type":"message_stop"}`},
	)
}
