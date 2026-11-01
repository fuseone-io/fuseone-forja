package github_test

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/fuseone-io/fuseone-forja/internal/github"
)

var pilotContract = []byte("version: 1\nimage: golang:1.25\ngates:\n  - name: build\n    run: make build\n")

func TestContractReader_readsFromDefaultBranchCommit_shaPinned(t *testing.T) {
	t.Parallel()
	fake := newFakeGitHub(t, "ghs_test_token", pilotContract)
	reader := &github.ContractReader{Tokens: &countingTokens{value: "ghs_test_token"}, BaseURL: fake.URL()}

	src, err := reader.Read(context.Background(), fakeRepo)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if src.SHA != fakeSHA {
		t.Errorf("SHA = %q, want the branch commit %q", src.SHA, fakeSHA)
	}
	if !bytes.Equal(src.Body, pilotContract) {
		t.Errorf("Body = %q, want the decoded contract %q", src.Body, pilotContract)
	}
}

func TestContractReader_contractAbsent_errContractMissing(t *testing.T) {
	t.Parallel()
	fake := newFakeGitHub(t, "ghs_test_token", nil)
	reader := &github.ContractReader{Tokens: &countingTokens{value: "ghs_test_token"}, BaseURL: fake.URL()}

	_, err := reader.Read(context.Background(), fakeRepo)
	if !errors.Is(err, github.ErrContractMissing) {
		t.Fatalf("Read error = %v, want ErrContractMissing", err)
	}
	if !strings.Contains(err.Error(), fakeSHA) || !strings.Contains(err.Error(), fakeRepo) {
		t.Fatalf("error %q does not name the commit and the repository", err)
	}
}

// The API answers 404 both for a missing file and for a ref it no longer
// knows; only the first is a missing contract.
func TestContractReader_commitGoneBetweenCalls_notReportedAsMissing(t *testing.T) {
	t.Parallel()
	fake := newFakeGitHub(t, "ghs_test_token", pilotContract)
	fake.vanished = true
	reader := &github.ContractReader{Tokens: &countingTokens{value: "ghs_test_token"}, BaseURL: fake.URL()}

	_, err := reader.Read(context.Background(), fakeRepo)
	if err == nil {
		t.Fatal("Read succeeded for a commit the API no longer knows")
	}
	if errors.Is(err, github.ErrContractMissing) {
		t.Fatalf("a vanished commit was reported as a missing contract: %v", err)
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("error %q does not carry the status", err)
	}
}

// A contents body whose encoding is not base64 carries no file: the reader
// must say so rather than hand back an empty contract.
func TestContractReader_contentsNotBase64_errorNamesTheEncoding(t *testing.T) {
	t.Parallel()
	fake := newFakeGitHub(t, "ghs_test_token", pilotContract)
	fake.encoding = "none"
	reader := &github.ContractReader{Tokens: &countingTokens{value: "ghs_test_token"}, BaseURL: fake.URL()}

	src, err := reader.Read(context.Background(), fakeRepo)
	if err == nil {
		t.Fatalf("Read succeeded with encoding none: body %q", src.Body)
	}
	if errors.Is(err, github.ErrContractMissing) {
		t.Fatalf("an undecodable body is not a missing contract: %v", err)
	}
	if !strings.Contains(err.Error(), "none") || !strings.Contains(err.Error(), fakeSHA) {
		t.Errorf("error %q does not name the encoding and the commit", err)
	}
}

func TestContractReader_tokenMintedPerCallAndScopedToRepository(t *testing.T) {
	t.Parallel()
	fake := newFakeGitHub(t, "ghs_test_token", pilotContract)
	tokens := &countingTokens{value: "ghs_test_token"}
	reader := &github.ContractReader{Tokens: tokens, BaseURL: fake.URL()}

	for i := 0; i < 2; i++ {
		if _, err := reader.Read(context.Background(), fakeRepo); err != nil {
			t.Fatalf("Read %d: %v", i, err)
		}
	}
	if got, want := tokens.mints(), []string{fakeRepo, fakeRepo}; !reflect.DeepEqual(got, want) {
		t.Fatalf("mints = %q, want one per Read, each for the repository: %q", got, want)
	}
}

// Reading the contract needs nothing beyond reading contents: a token that
// could write would be a wider credential than the call deserves.
func TestContractReader_mintsContentsRead(t *testing.T) {
	t.Parallel()
	fake := newFakeGitHub(t, "ghs_test_token", pilotContract)
	tokens := &countingTokens{value: "ghs_test_token"}
	reader := &github.ContractReader{Tokens: tokens, BaseURL: fake.URL()}

	if _, err := reader.Read(context.Background(), fakeRepo); err != nil {
		t.Fatalf("Read: %v", err)
	}
	assertEveryMint(t, tokens, github.Permissions{"contents": "read"})
}

func TestContractReader_wrongToken_statusErrorWithoutTheToken(t *testing.T) {
	t.Parallel()
	fake := newFakeGitHub(t, "ghs_the_real_one", pilotContract)
	reader := &github.ContractReader{Tokens: &countingTokens{value: "ghs_a_stale_one"}, BaseURL: fake.URL()}

	_, err := reader.Read(context.Background(), fakeRepo)
	if err == nil {
		t.Fatal("Read succeeded with a token the API rejects")
	}
	if errors.Is(err, github.ErrContractMissing) {
		t.Fatalf("a 401 is not a missing contract: %v", err)
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error %q does not carry the status", err)
	}
	if strings.Contains(err.Error(), "ghs_a_stale_one") {
		t.Errorf("error %q carries the token", err)
	}
}

func TestContractReader_repositoryNotOwnerName_rejectedBeforeAnyCall(t *testing.T) {
	t.Parallel()
	tokens := &countingTokens{value: "ghs_test_token"}
	reader := &github.ContractReader{Tokens: tokens, BaseURL: "http://127.0.0.1:1"}

	for _, repo := range []string{"fuseone-forja", "a/b/c", "/name", "owner/"} {
		if _, err := reader.Read(context.Background(), repo); err == nil {
			t.Errorf("Read(%q) succeeded, want an error", repo)
		}
	}
	if len(tokens.mints()) != 0 {
		t.Fatalf("a malformed repository minted %d tokens", len(tokens.mints()))
	}
}
