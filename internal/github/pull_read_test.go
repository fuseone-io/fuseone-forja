package github_test

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
	"github.com/fuseone-io/fuseone-forja/internal/github"
)

const pullsToken = "ghs_pulls_read_token_21"

func pullReader(fake *fakePulls) (*github.PullRequests, *countingTokens) {
	tokens := &countingTokens{value: pullsToken}
	return &github.PullRequests{Tokens: tokens, BaseURL: fake.URL()}, tokens
}

func TestPullRequests_read_headBaseAndFilesDecodedMintsPullRequestsRead(t *testing.T) {
	t.Parallel()
	fake := newFakePulls(t, pullsToken)
	fake.files = []fakeFile{{name: "README.md"}, {name: "docs/x.md"}}
	fake.changedFiles = 2
	reader, tokens := pullReader(fake)

	got, err := reader.Read(context.Background(), fakeRepo, 57)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	want := domain.PullRequestDiff{
		Number: 57, URL: "https://github.com/" + fakeRepo + "/pull/57", HeadRepository: fakeRepo,
		HeadRef: "forja/12", HeadSHA: deliveryHead, BaseRef: "main",
		Files: []domain.ChangedFile{{Path: "README.md"}, {Path: "docs/x.md"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Read = %+v, want %+v", got, want)
	}
	if !reflect.DeepEqual(tokens.mints(), []string{fakeRepo}) {
		t.Fatalf("mints = %q, want one for %s", tokens.mints(), fakeRepo)
	}
	assertEveryMint(t, tokens, github.ReadPullRequests)
}

// The files come 100 to a page; every page is read until a short one, in
// order.
func TestPullRequests_read_twoPagesJoinedInOrder(t *testing.T) {
	t.Parallel()
	fake := newFakePulls(t, pullsToken)
	fake.withFiles(150)
	reader, _ := pullReader(fake)

	got, err := reader.Read(context.Background(), fakeRepo, 57)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(got.Files) != 150 || got.Files[0].Path != "f0.go" || got.Files[100].Path != "f100.go" || got.Files[149].Path != "f149.go" {
		t.Fatalf("Files = %d entries, want f0.go..f149.go in order", len(got.Files))
	}
	if pages := fake.filePages(); !reflect.DeepEqual(pages, []string{"per_page=100&page=1", "per_page=100&page=2"}) {
		t.Fatalf("pages asked = %q, want two of 100", pages)
	}
}

// A full last page still asks for the next one, which is empty.
func TestPullRequests_read_exactlyOnePage_emptyNextPageEnds(t *testing.T) {
	t.Parallel()
	fake := newFakePulls(t, pullsToken)
	fake.withFiles(100)
	reader, _ := pullReader(fake)

	got, err := reader.Read(context.Background(), fakeRepo, 57)
	if err != nil || len(got.Files) != 100 {
		t.Fatalf("Read = %d files, %v; want 100", len(got.Files), err)
	}
	if pages := fake.filePages(); len(pages) != 2 {
		t.Fatalf("pages asked = %q, want the full one and the empty one", pages)
	}
}

func TestPullRequests_read_rename_bothPaths(t *testing.T) {
	t.Parallel()
	fake := newFakePulls(t, pullsToken)
	fake.files = []fakeFile{{name: "docs/contracts/streaming-and-usage.md", previous: "internal/proxy/model/usage.go"}}
	fake.changedFiles = 1
	reader, _ := pullReader(fake)

	got, err := reader.Read(context.Background(), fakeRepo, 57)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	want := []domain.ChangedFile{{Path: "docs/contracts/streaming-and-usage.md", PreviousPath: "internal/proxy/model/usage.go"}}
	if !reflect.DeepEqual(got.Files, want) {
		t.Fatalf("Files = %+v, want %+v", got.Files, want)
	}
}

// The API lists 3000 files at most; a pull request that changes more is
// an error, never a diff missing the rest.
func TestPullRequests_read_listedFewerThanChanged_errorNotADiff(t *testing.T) {
	t.Parallel()
	tests := map[string]func(*fakePulls){
		"over the 3000 cap": func(f *fakePulls) { f.withFiles(3001) },
		"one short":         func(f *fakePulls) { f.withFiles(5); f.changedFiles = 6 },
		// The pull request moved between the two calls: the files are not
		// the diff of the head that was read.
		"more listed than changed": func(f *fakePulls) { f.withFiles(5); f.changedFiles = 4 },
	}
	for name, setUp := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fake := newFakePulls(t, pullsToken)
			setUp(fake)
			reader, _ := pullReader(fake)

			got, err := reader.Read(context.Background(), fakeRepo, 57)
			if !errors.Is(err, github.ErrFilesTruncated) {
				t.Fatalf("Read = %d files, %v; want ErrFilesTruncated", len(got.Files), err)
			}
			if !strings.Contains(err.Error(), strconv.Itoa(fake.changedFiles)) {
				t.Errorf("error %q does not say how many files changed", err)
			}
			if got.Files != nil || got.HeadSHA != "" {
				t.Errorf("Read returned %+v alongside the error, want the zero diff", got)
			}
		})
	}
}

// A page whose patches make it larger than a contract or an error body is
// still read whole.
func TestPullRequests_read_largePatches_pageReadWhole(t *testing.T) {
	t.Parallel()
	fake := newFakePulls(t, pullsToken)
	fake.withFiles(100)
	for i := range fake.files {
		fake.files[i].patch = strings.Repeat("+x\n", 10_000)
	}
	reader, _ := pullReader(fake)

	got, err := reader.Read(context.Background(), fakeRepo, 57)
	if err != nil || len(got.Files) != 100 {
		t.Fatalf("Read = %d files, %v; want 100", len(got.Files), err)
	}
}

// A head whose repository was deleted answers head.repo null: the diff
// says so with an empty HeadRepository, never a guess.
func TestPullRequests_read_headRepoNull_emptyHeadRepository(t *testing.T) {
	t.Parallel()
	fake := newFakePulls(t, pullsToken)
	fake.headRepo = nil
	reader, _ := pullReader(fake)

	got, err := reader.Read(context.Background(), fakeRepo, 57)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got.HeadRepository != "" || got.HeadRef != "forja/12" || got.HeadSHA != deliveryHead {
		t.Fatalf("Read = %+v, want no head repository with the ref and sha kept", got)
	}
}

func TestPullRequests_read_unknownNumber_errPullRequestNotFound(t *testing.T) {
	t.Parallel()
	fake := newFakePulls(t, pullsToken)
	reader, _ := pullReader(fake)

	_, err := reader.Read(context.Background(), fakeRepo, 404)
	if !errors.Is(err, github.ErrPullRequestNotFound) {
		t.Fatalf("Read(404) = %v, want ErrPullRequestNotFound", err)
	}
	for _, want := range []string{fakeRepo, "404"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
}

// Errors name the repository and the number, and carry neither the pull
// request's title, body, paths or patches nor the token.
func TestPullRequests_read_failures_noContentInErrors(t *testing.T) {
	t.Parallel()
	tests := map[string]func(*fakePulls){
		"truncated":   func(f *fakePulls) { f.files = []fakeFile{{name: "secret/path.go", patch: "+leaked"}}; f.changedFiles = 2 },
		"files down":  func(f *fakePulls) { f.filesDown = true },
		"wrong token": func(f *fakePulls) { f.token = "ghs_other" },
	}
	for name, setUp := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fake := newFakePulls(t, pullsToken)
			setUp(fake)
			reader, _ := pullReader(fake)

			_, err := reader.Read(context.Background(), fakeRepo, 57)
			if err == nil {
				t.Fatal("Read succeeded")
			}
			for _, leak := range []string{fake.title, "private-looking", "secret/path.go", "leaked", pullsToken} {
				if strings.Contains(err.Error(), leak) {
					t.Errorf("error %q carries %q", err, leak)
				}
			}
			if !strings.Contains(err.Error(), fakeRepo) || !strings.Contains(err.Error(), "57") {
				t.Errorf("error %q does not name %s#57", err, fakeRepo)
			}
		})
	}
}

func TestPullRequests_read_badInput_refusedBeforeAnyMint(t *testing.T) {
	t.Parallel()
	fake := newFakePulls(t, pullsToken)
	reader, tokens := pullReader(fake)
	for name, in := range map[string]struct {
		repo   string
		number int
	}{
		"malformed repo": {"a/b/c", 57},
		"number zero":    {fakeRepo, 0},
		"negative":       {fakeRepo, -3},
	} {
		if _, err := reader.Read(context.Background(), in.repo, in.number); err == nil {
			t.Errorf("%s: Read = nil error", name)
		}
	}
	if n := len(tokens.mints()); n != 0 {
		t.Fatalf("bad input minted %d tokens", n)
	}
}
