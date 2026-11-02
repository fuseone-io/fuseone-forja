package main

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

const (
	testModelKey   = "sk-ant-forjad-test-key-9f8e7d"
	testModelToken = "c0ffee00c0ffee00c0ffee00c0ffee00c0ffee00c0ffee00c0ffee00c0ffee00"
)

// secretFiles writes the key and token files the control plane would copy
// into the model container, and returns their paths.
func secretFiles(t *testing.T, key, token string) (keyFile, tokenFile string) {
	t.Helper()
	dir := t.TempDir()
	keyFile, tokenFile = filepath.Join(dir, "key"), filepath.Join(dir, "token")
	for path, content := range map[string]string{keyFile: key, tokenFile: token} {
		if err := os.WriteFile(path, []byte(content), 0o400); err != nil {
			t.Fatal(err)
		}
	}
	return keyFile, tokenFile
}

// A misconfigured proxy exits 2 before anything reaches stdout, so the
// control plane never reads a readiness line from a proxy that is not
// serving, and the key is never echoed.
func TestRun_modelMisconfigured_exit2BeforeStdout(t *testing.T) {
	t.Parallel()
	keyFile, tokenFile := secretFiles(t, testModelKey, testModelToken)
	emptyKey, _ := secretFiles(t, "", testModelToken)
	missing := filepath.Join(t.TempDir(), "absent")
	base := func(extra ...string) []string {
		args := map[string]string{"-run": "r1", "-upstream": "https://api.anthropic.com", "-cap": "1000", "-key-file": keyFile, "-token-file": tokenFile}
		for i := 0; i+1 < len(extra); i += 2 {
			args[extra[i]] = extra[i+1]
		}
		out := []string{"model"}
		for k, v := range args {
			if v != "" {
				out = append(out, k, v)
			}
		}
		return out
	}
	tests := map[string][]string{
		"no run id":          base("-run", ""),
		"upstream with path": base("-upstream", "https://api.anthropic.com/v1"),
		"no cap":             base("-cap", ""),
		"zero cap":           base("-cap", "0"),
		"cap not a number":   base("-cap", "many"),
		"no key file":        base("-key-file", ""),
		"key file missing":   base("-key-file", missing),
		"key file empty":     base("-key-file", emptyKey),
		"no token file":      base("-token-file", ""),
		"token file missing": base("-token-file", missing),
		"token is the key":   base("-token-file", keyFile),
		"key as a flag":      append(base(), "-key", testModelKey),
		"extra argument":     append(base(), "extra"),
	}
	for name, args := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var stdout, stderr bytes.Buffer
			if code := run(args, &stdout, &stderr); code != 2 {
				t.Fatalf("run(%q) = %d, want 2; stderr %q", args, code, stderr.String())
			}
			if stdout.Len() != 0 {
				t.Errorf("stdout = %q, want nothing before the proxy serves", stdout.String())
			}
			if strings.Contains(stderr.String(), testModelKey) || strings.Contains(stderr.String(), testModelToken) {
				t.Errorf("stderr carries a secret: %q", stderr.String())
			}
		})
	}
}

// The subcommand is the proxy the model container runs: it says it is
// ready, forwards with the key read from its file, logs the usage and
// exits 0 when stopped.
func TestModelCommand_readyForwardsWithKeyFileThenCleanStop(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var gotKey string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotKey = r.Header.Get("x-api-key")
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"usage":{"input_tokens":3,"output_tokens":4}}`)
	}))
	defer up.Close()
	keyFile, tokenFile := secretFiles(t, testModelKey+"\n", testModelToken+"\n")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stdoutR, stdoutW := io.Pipe()
	var stderr bytes.Buffer
	exited := make(chan int, 1)
	go func() {
		exited <- model(ctx, []string{"-run", "run-7", "-upstream", up.URL, "-cap", "1000", "-key-file", keyFile, "-token-file", tokenFile, "-listen", "127.0.0.1:0"}, stdoutW, &stderr)
		_ = stdoutW.Close()
	}()
	lines := bufio.NewScanner(stdoutR)

	addr := readyAddr(t, lines)
	// The usage line is written before the client has the whole body, so
	// stdout is read while the request runs.
	next := make(chan string, 1)
	go func() {
		lines.Scan()
		next <- lines.Text()
		_, _ = io.Copy(io.Discard, stdoutR)
	}()
	req, _ := http.NewRequest(http.MethodPost, "http://"+addr+"/v1/messages", strings.NewReader("{}"))
	req.Header.Set("x-api-key", testModelToken)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, res.Body)
	_ = res.Body.Close()
	if line := <-next; !strings.Contains(line, `"event":"usage"`) || !strings.Contains(line, `"input":3`) {
		t.Fatalf("line after the request = %q, want its usage", line)
	}
	mu.Lock()
	if gotKey != testModelKey {
		t.Errorf("upstream x-api-key = %q, want the key file's content without its newline", gotKey)
	}
	mu.Unlock()
	cancel()
	if code := <-exited; code != 0 {
		t.Fatalf("model after cancel = %d, want 0; stderr %q", code, stderr.String())
	}
}
