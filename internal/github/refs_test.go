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

const (
	claimRef   = "refs/forja/claims/12"
	writeToken = "ghs_write_token"
)

var claimBody = []byte(`{"version":1,"run_id":"run-1"}`)

func newRefs(t *testing.T, fake *fakeGit) (*github.Refs, *countingTokens) {
	t.Helper()
	tokens := &countingTokens{value: writeToken}
	return &github.Refs{Tokens: tokens, BaseURL: fake.URL()}, tokens
}

func TestRefs_mintsContentsWrite(t *testing.T) {
	t.Parallel()
	fake := newFakeGit(t, writeToken)
	refs, tokens := newRefs(t, fake)
	ctx := context.Background()

	sha, err := refs.CommitFile(ctx, fakeRepo, github.FileCommit{Path: "claim.json", Body: claimBody, Message: "claim"})
	if err != nil {
		t.Fatalf("CommitFile: %v", err)
	}
	steps := []func() error{
		func() error { return refs.CreateRef(ctx, fakeRepo, claimRef, sha) },
		func() error { _, err := refs.GetRef(ctx, fakeRepo, claimRef); return err },
		func() error { _, err := refs.ReadFile(ctx, fakeRepo, sha, "claim.json"); return err },
		func() error { return refs.UpdateRef(ctx, fakeRepo, claimRef, sha) },
		func() error { return refs.DeleteRef(ctx, fakeRepo, claimRef) },
	}
	for i, step := range steps {
		if err := step(); err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
	}
	if got := len(tokens.mints()); got != len(steps)+1 {
		t.Fatalf("minted %d tokens, want one per call (%d)", got, len(steps)+1)
	}
	assertEveryMint(t, tokens, github.Permissions{"contents": "write"})
}

func TestRefs_getRef_existingAndMissing(t *testing.T) {
	t.Parallel()
	fake := newFakeGit(t, writeToken)
	sha := fake.seed(claimRef, "", map[string][]byte{"claim.json": claimBody})
	refs, _ := newRefs(t, fake)

	got, err := refs.GetRef(context.Background(), fakeRepo, claimRef)
	if err != nil {
		t.Fatalf("GetRef: %v", err)
	}
	if got.SHA != sha || got.Type != "commit" {
		t.Fatalf("GetRef = %+v, want sha %s of type commit", got, sha)
	}
	_, err = refs.GetRef(context.Background(), fakeRepo, "refs/forja/claims/13")
	if !errors.Is(err, github.ErrRefMissing) {
		t.Fatalf("GetRef of an absent ref = %v, want ErrRefMissing", err)
	}
}

func TestRefs_createRef_existing_errRefExists(t *testing.T) {
	t.Parallel()
	fake := newFakeGit(t, writeToken)
	held := fake.seed(claimRef, "", map[string][]byte{"claim.json": claimBody})
	other := fake.seed("", "", map[string][]byte{"claim.json": []byte(`{"run_id":"run-2"}`)})
	refs, _ := newRefs(t, fake)

	err := refs.CreateRef(context.Background(), fakeRepo, claimRef, other)
	if !errors.Is(err, github.ErrRefExists) {
		t.Fatalf("CreateRef on a held ref = %v, want ErrRefExists", err)
	}
	if sha, _ := fake.refSHA(claimRef); sha != held {
		t.Fatalf("the held ref moved to %s", sha)
	}
	if err := refs.CreateRef(context.Background(), fakeRepo, "refs/forja/claims/13", other); err != nil {
		t.Fatalf("CreateRef on a free ref: %v", err)
	}
	if sha, _ := fake.refSHA("refs/forja/claims/13"); sha != other {
		t.Fatalf("free ref points at %s, want %s", sha, other)
	}
}

func TestRefs_updateRef_notFastForward_errNotFastForward(t *testing.T) {
	t.Parallel()
	fake := newFakeGit(t, writeToken)
	held := fake.seed(claimRef, "", map[string][]byte{"claim.json": claimBody})
	child := fake.seed("", held, map[string][]byte{"claim.json": []byte(`{"run_id":"run-2"}`)})
	unrelated := fake.seed("", "", map[string][]byte{"claim.json": []byte(`{"run_id":"run-3"}`)})
	refs, _ := newRefs(t, fake)

	err := refs.UpdateRef(context.Background(), fakeRepo, claimRef, unrelated)
	if !errors.Is(err, github.ErrNotFastForward) {
		t.Fatalf("UpdateRef to an unrelated commit = %v, want ErrNotFastForward", err)
	}
	if sha, _ := fake.refSHA(claimRef); sha != held {
		t.Fatalf("a refused update moved the ref to %s", sha)
	}
	if err := refs.UpdateRef(context.Background(), fakeRepo, claimRef, child); err != nil {
		t.Fatalf("UpdateRef to a descendant: %v", err)
	}
	if sha, _ := fake.refSHA(claimRef); sha != child {
		t.Fatalf("ref points at %s after a fast-forward to %s", sha, child)
	}
	err = refs.UpdateRef(context.Background(), fakeRepo, "refs/forja/claims/13", child)
	if !errors.Is(err, github.ErrRefMissing) {
		t.Fatalf("UpdateRef of an absent ref = %v, want ErrRefMissing", err)
	}
}

func TestRefs_deleteRef_missing_errRefMissing(t *testing.T) {
	t.Parallel()
	fake := newFakeGit(t, writeToken)
	fake.seed(claimRef, "", map[string][]byte{"claim.json": claimBody})
	refs, _ := newRefs(t, fake)

	if err := refs.DeleteRef(context.Background(), fakeRepo, claimRef); err != nil {
		t.Fatalf("DeleteRef: %v", err)
	}
	if _, present := fake.refSHA(claimRef); present {
		t.Fatal("the ref is still there after DeleteRef")
	}
	err := refs.DeleteRef(context.Background(), fakeRepo, claimRef)
	if !errors.Is(err, github.ErrRefMissing) {
		t.Fatalf("second DeleteRef = %v, want ErrRefMissing", err)
	}
}

func TestRefs_commitFile_postsBlobTreeCommitWithParent(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		parent bool
	}{
		{name: "root commit", parent: false},
		{name: "child commit", parent: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fake := newFakeGit(t, writeToken)
			refs, _ := newRefs(t, fake)
			var parent string
			if tt.parent {
				parent = fake.seed("", "", map[string][]byte{"claim.json": []byte(`{"run_id":"old"}`)})
			}

			sha, err := refs.CommitFile(context.Background(), fakeRepo, github.FileCommit{
				Path: "claim.json", Body: claimBody, Message: "forja: claim issue 12", Parent: parent,
			})
			if err != nil {
				t.Fatalf("CommitFile: %v", err)
			}
			wantParents := []string{}
			if tt.parent {
				wantParents = []string{parent}
			}
			assertClaimCommit(t, fake, sha, wantParents)
			body, err := refs.ReadFile(context.Background(), fakeRepo, sha, "claim.json")
			if err != nil || !bytes.Equal(body, claimBody) {
				t.Fatalf("ReadFile at the new commit = %q, %v; want the committed body", body, err)
			}
		})
	}
}

// assertClaimCommit checks the three objects were posted in order, that the
// commit has exactly the parents wanted and that parents travelled as an
// array even when empty.
func assertClaimCommit(t *testing.T, fake *fakeGit, sha string, wantParents []string) {
	t.Helper()
	wantOrder := []string{
		"POST /repos/fuseone-io/fuseone-forja/git/blobs",
		"POST /repos/fuseone-io/fuseone-forja/git/trees",
		"POST /repos/fuseone-io/fuseone-forja/git/commits",
	}
	if got := fake.requestLog(); !reflect.DeepEqual(got, wantOrder) {
		t.Fatalf("requests = %q, want %q", got, wantOrder)
	}
	if got := fake.parentsOf(sha); !reflect.DeepEqual(got, wantParents) {
		t.Fatalf("parents of %s = %q, want %q", sha, got, wantParents)
	}
	if reqs := fake.commitRequests(); len(reqs) != 1 || reqs[0].Parents == nil || reqs[0].Message != "forja: claim issue 12" {
		t.Fatalf("commit request = %+v, want parents on the wire as an array and the message", reqs)
	}
}

func TestRefs_readFile_decodesContentsAtSHA(t *testing.T) {
	t.Parallel()
	fake := newFakeGit(t, writeToken)
	sha := fake.seed("", "", map[string][]byte{"claim.json": claimBody})
	refs, _ := newRefs(t, fake)

	body, err := refs.ReadFile(context.Background(), fakeRepo, sha, "claim.json")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !bytes.Equal(body, claimBody) {
		t.Fatalf("ReadFile = %q, want %q", body, claimBody)
	}
	if _, err := refs.ReadFile(context.Background(), fakeRepo, sha, "other.json"); err == nil {
		t.Fatal("ReadFile of an absent path succeeded")
	}
	_, err = refs.ReadFile(context.Background(), fakeRepo, strings.Repeat("0", 40), "claim.json")
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("ReadFile at an unknown commit = %v, want a 404", err)
	}
}

func TestRefs_refOutsideRefsNamespace_rejectedBeforeAnyCall(t *testing.T) {
	t.Parallel()
	refs := &github.Refs{Tokens: &countingTokens{value: writeToken}, BaseURL: "http://127.0.0.1:1"}
	ctx := context.Background()
	for _, ref := range []string{"forja/claims/12", "", "refs/", "refs/forja/../heads/main"} {
		if _, err := refs.GetRef(ctx, fakeRepo, ref); err == nil {
			t.Errorf("GetRef(%q) succeeded", ref)
		}
		if err := refs.CreateRef(ctx, fakeRepo, ref, strings.Repeat("a", 40)); err == nil {
			t.Errorf("CreateRef(%q) succeeded", ref)
		}
	}
	if _, err := refs.GetRef(ctx, "not-a-repo", claimRef); err == nil {
		t.Error("GetRef with a malformed repository succeeded")
	}
}

func TestRefs_wrongToken_statusErrorWithoutTheToken(t *testing.T) {
	t.Parallel()
	fake := newFakeGit(t, "ghs_the_real_one")
	refs := &github.Refs{Tokens: &countingTokens{value: "ghs_a_stale_one"}, BaseURL: fake.URL()}

	_, err := refs.GetRef(context.Background(), fakeRepo, claimRef)
	if err == nil || errors.Is(err, github.ErrRefMissing) {
		t.Fatalf("GetRef with a rejected token = %v, want a status error that is not ErrRefMissing", err)
	}
	if !strings.Contains(err.Error(), "401") || strings.Contains(err.Error(), "ghs_a_stale_one") {
		t.Fatalf("error %q must carry the status and not the token", err)
	}
}
