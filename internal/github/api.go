package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	// DefaultBaseURL is the public API; tests point BaseURL at a fake.
	DefaultBaseURL = "https://api.github.com"
	apiVersion     = "2022-11-28"
	acceptJSON     = "application/vnd.github+json"
	// maxBody bounds what is read from a response: the contract and the
	// token responses are small, and an error body is read only for its
	// message.
	maxBody = 1 << 20
	// defaultTimeout bounds every call made with a client the caller did
	// not supply, so a stalled API never stalls the tool.
	defaultTimeout = 30 * time.Second
)

// StatusError is a response the API answered with a status the caller did
// not expect. It carries the status and the API's message, never the
// request that produced it.
type StatusError struct {
	Status  int
	Message string
}

func (e *StatusError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("status %d", e.Status)
	}
	return fmt.Sprintf("status %d: %s", e.Status, e.Message)
}

// isNotFound reports a 404 whose message is the one GitHub gives for a
// path that does not exist at a ref that does.
func isNotFound(err error) bool {
	var se *StatusError
	return errors.As(err, &se) && se.Status == http.StatusNotFound && se.Message == "Not Found"
}

// apiClient performs authenticated calls against one base URL with one
// token. The token travels only in the Authorization header; errors never
// carry it.
type apiClient struct {
	http  *http.Client
	base  string
	token string
}

func newAPIClient(client *http.Client, base, token string) *apiClient {
	if client == nil {
		client = &http.Client{Timeout: defaultTimeout}
	}
	if base == "" {
		base = DefaultBaseURL
	}
	return &apiClient{http: client, base: strings.TrimRight(base, "/"), token: token}
}

// apiCall is one request. With no method it is a GET when body is nil and a
// JSON POST otherwise; PATCH and DELETE name their method.
type apiCall struct {
	method string
	path   string
	body   []byte
	// want is the status that carries the expected response.
	want int
	// limit bounds what is read of the expected response; zero means
	// maxBody.
	limit int64
}

func (a apiCall) bodyLimit() int64 {
	if a.limit > 0 {
		return a.limit
	}
	return maxBody
}

func (a apiCall) verb() string {
	switch {
	case a.method != "":
		return a.method
	case a.body == nil:
		return http.MethodGet
	}
	return http.MethodPost
}

// do sends the call with the API headers and decodes the JSON body on the
// expected status; a nil out means the response carries no body to read.
// out is any because it is handed to json.Decoder.Decode, which takes any.
// A different status comes back as *StatusError.
func (c *apiClient) do(ctx context.Context, call apiCall, out any) error {
	req, err := c.request(ctx, call)
	if err != nil {
		return fmt.Errorf("%s %s: %w", call.verb(), call.path, err)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", call.verb(), call.path, err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != call.want {
		return fmt.Errorf("%s %s: %w", call.verb(), call.path, c.statusError(res))
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, call.bodyLimit())).Decode(out); err != nil {
		return fmt.Errorf("%s %s: decode response: %w", call.verb(), call.path, err)
	}
	return nil
}

func (c *apiClient) request(ctx context.Context, call apiCall) (*http.Request, error) {
	var body io.Reader
	if call.body != nil {
		body = bytes.NewReader(call.body)
	}
	req, err := http.NewRequestWithContext(ctx, call.verb(), c.base+call.path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", acceptJSON)
	req.Header.Set("X-GitHub-Api-Version", apiVersion)
	if call.body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req, nil
}

// statusError reads the API's message from an unexpected response. A
// message that echoes the credential is withheld: the status is enough.
func (c *apiClient) statusError(res *http.Response) *StatusError {
	var payload struct {
		Message string `json:"message"`
	}
	// A body that is not the API's JSON error leaves the message empty;
	// the status alone is the error, so the decode failure is not one.
	_ = json.NewDecoder(io.LimitReader(res.Body, maxBody)).Decode(&payload)
	if c.token != "" && strings.Contains(payload.Message, c.token) {
		payload.Message = ""
	}
	return &StatusError{Status: res.StatusCode, Message: payload.Message}
}

// splitRepo parses "owner/name".
func splitRepo(repo string) (owner, name string, err error) {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return "", "", fmt.Errorf("github: repository %q is not owner/name", repo)
	}
	return owner, name, nil
}
