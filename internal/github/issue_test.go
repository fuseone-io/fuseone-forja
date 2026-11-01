package github_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/fuseone-io/fuseone-forja/internal/github"
)

const (
	issueTitle = "forja.plan: checkout no plano de controle"
	issueBody  = "## What to build\n\nClona a branch base.\n"
)

func fakeWithIssue(t *testing.T) *fakeGitHub {
	t.Helper()
	fake := newFakeGitHub(t, "ghs_test_token", pilotContract)
	body := issueBody
	fake.issue = fakeIssue{number: 7, title: issueTitle, body: &body}
	fake.pullRequest = 25
	return fake
}

func issueReader(fake *fakeGitHub, token string) (*github.IssueReader, *countingTokens) {
	tokens := &countingTokens{value: token}
	return &github.IssueReader{Tokens: tokens, BaseURL: fake.URL()}, tokens
}

func TestIssueReader_issue_titleAndBodyDecodedTokenMintedForRepository(t *testing.T) {
	t.Parallel()
	fake := fakeWithIssue(t)
	reader, tokens := issueReader(fake, "ghs_test_token")

	got, err := reader.Read(context.Background(), fakeRepo, 7)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got.Number != 7 || got.Title != issueTitle || got.Body != issueBody {
		t.Errorf("Issue = %+v, want number 7 with the fake's title and body", got)
	}
	if mints := tokens.mints(); !reflect.DeepEqual(mints, []string{fakeRepo}) {
		t.Errorf("mints = %q, want one for %q", mints, fakeRepo)
	}
	assertEveryMint(t, tokens, github.ReadIssues)
}

// The API represents an issue created without a body as null, never as "".
func TestIssueReader_bodyNull_emptyString(t *testing.T) {
	t.Parallel()
	fake := fakeWithIssue(t)
	fake.issue.body = nil
	reader, _ := issueReader(fake, "ghs_test_token")

	got, err := reader.Read(context.Background(), fakeRepo, 7)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got.Body != "" || got.Title != issueTitle {
		t.Errorf("Issue = %+v, want an empty body and the title", got)
	}
}

func TestIssueReader_pullRequest_errNotAnIssueWithoutTitleOrBody(t *testing.T) {
	t.Parallel()
	fake := fakeWithIssue(t)
	reader, _ := issueReader(fake, "ghs_test_token")

	_, err := reader.Read(context.Background(), fakeRepo, 25)
	if !errors.Is(err, github.ErrNotAnIssue) {
		t.Fatalf("Read(25) error = %v, want ErrNotAnIssue", err)
	}
	assertNamesRepoAndNumberOnly(t, err, "25")
}

func TestIssueReader_unknownNumber_errIssueNotFoundNamesRepoAndNumber(t *testing.T) {
	t.Parallel()
	fake := fakeWithIssue(t)
	reader, _ := issueReader(fake, "ghs_test_token")

	_, err := reader.Read(context.Background(), fakeRepo, 404)
	if !errors.Is(err, github.ErrIssueNotFound) {
		t.Fatalf("Read(404) error = %v, want ErrIssueNotFound", err)
	}
	assertNamesRepoAndNumberOnly(t, err, "404")
}

func TestIssueReader_serverError_statusErrorNamesRepoAndNumber(t *testing.T) {
	t.Parallel()
	fake := fakeWithIssue(t)
	fake.issuesDown = true
	reader, _ := issueReader(fake, "ghs_test_token")

	_, err := reader.Read(context.Background(), fakeRepo, 7)
	if err == nil {
		t.Fatal("Read succeeded on a 500")
	}
	if errors.Is(err, github.ErrIssueNotFound) || errors.Is(err, github.ErrNotAnIssue) {
		t.Fatalf("a 500 was classified as %v", err)
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error %q does not carry the status", err)
	}
	assertNamesRepoAndNumberOnly(t, err, "7")
}

// assertNamesRepoAndNumberOnly checks the error carries the identifiers and
// none of the issue's text.
func assertNamesRepoAndNumberOnly(t *testing.T, err error, number string) {
	t.Helper()
	text := err.Error()
	for _, want := range []string{fakeRepo, number} {
		if !strings.Contains(text, want) {
			t.Errorf("error %q does not name %q", text, want)
		}
	}
	for _, leak := range []string{issueTitle, "What to build", "ghs_"} {
		if strings.Contains(text, leak) {
			t.Errorf("error %q carries %q", text, leak)
		}
	}
}

func TestIssueReader_wrongToken_statusErrorWithoutTheToken(t *testing.T) {
	t.Parallel()
	fake := fakeWithIssue(t)
	reader, _ := issueReader(fake, "ghs_a_stale_one")

	_, err := reader.Read(context.Background(), fakeRepo, 7)
	if err == nil || errors.Is(err, github.ErrIssueNotFound) {
		t.Fatalf("Read with a rejected token: %v", err)
	}
	if !strings.Contains(err.Error(), "401") || strings.Contains(err.Error(), "ghs_a_stale_one") {
		t.Errorf("error %q should carry the status and not the token", err)
	}
}

func TestIssueReader_onlyGetRequests(t *testing.T) {
	t.Parallel()
	fake := fakeWithIssue(t)
	reader, _ := issueReader(fake, "ghs_test_token")

	if _, err := reader.Read(context.Background(), fakeRepo, 7); err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got := fake.requests(); !reflect.DeepEqual(got, []string{"GET"}) {
		t.Fatalf("requests = %q, want exactly one GET", got)
	}
}

func TestIssueReader_noTokenSourceOrBadInput_rejectedBeforeAnyCall(t *testing.T) {
	t.Parallel()
	tokens := &countingTokens{value: "ghs_test_token"}
	readers := map[string]*github.IssueReader{
		"no token source": {BaseURL: "http://127.0.0.1:1"},
		"malformed repo":  {Tokens: tokens, BaseURL: "http://127.0.0.1:1"},
		"number zero":     {Tokens: tokens, BaseURL: "http://127.0.0.1:1"},
	}
	inputs := map[string]struct {
		repo   string
		number int
	}{
		"no token source": {fakeRepo, 7},
		"malformed repo":  {"a/b/c", 7},
		"number zero":     {fakeRepo, 0},
	}
	for name, reader := range readers {
		in := inputs[name]
		if _, err := reader.Read(context.Background(), in.repo, in.number); err == nil {
			t.Errorf("%s: Read succeeded", name)
		}
	}
	if len(tokens.mints()) != 0 {
		t.Fatalf("bad input minted %d tokens", len(tokens.mints()))
	}
}
