package github_test

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
	"github.com/fuseone-io/fuseone-forja/internal/github"
)

const (
	deliveryToken = "ghs_delivery_token_0042"
	deliveryHead  = "9c1b2a3d4e5f60718293a4b5c6d7e8f901234567"
	// evidenceBody stands for the run's report; no error may carry it.
	evidenceBody = "## Forja evidence\n\nRefs #7\n\nsecret-looking stdout tail\n"
)

func pullRequests(fake *fakeDelivery) (*github.PullRequests, *countingTokens) {
	tokens := &countingTokens{value: deliveryToken}
	return &github.PullRequests{Tokens: tokens, BaseURL: fake.URL()}, tokens
}

func openRequest() github.PullRequestRequest {
	return github.PullRequestRequest{Repository: fakeRepo, Title: "Write a proof file", Head: agentBranch, Base: "main", Body: evidenceBody}
}

func TestPullRequests_open_postsHeadBaseBodyMintsPullRequestsWrite(t *testing.T) {
	t.Parallel()
	fake := newFakeDelivery(t, deliveryToken)
	fake.branches[agentBranch] = true
	prs, tokens := pullRequests(fake)

	got, err := prs.Open(context.Background(), openRequest())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	want := domain.PullRequest{Number: 41, URL: "https://github.com/" + fakeRepo + "/pull/41"}
	if got != want {
		t.Fatalf("Open = %+v, want %+v", got, want)
	}
	bodies := fake.pullBodies()
	wantBody := map[string]any{"title": "Write a proof file", "head": agentBranch, "base": "main", "body": evidenceBody}
	if len(bodies) != 1 || !reflect.DeepEqual(bodies[0], wantBody) {
		t.Fatalf("POST bodies = %+v, want [%+v]", bodies, wantBody)
	}
	if !reflect.DeepEqual(tokens.mints(), []string{fakeRepo}) {
		t.Fatalf("mints = %q, want one for %s", tokens.mints(), fakeRepo)
	}
	assertEveryMint(t, tokens, github.WritePullRequests)
}

// A refusal comes back as the status and the API's message, never as the
// report that was sent nor the token.
func TestPullRequests_refused_statusErrorWithoutBodyOrToken(t *testing.T) {
	t.Parallel()
	tests := map[string]func(*fakeDelivery){
		"head already has one": func(f *fakeDelivery) { f.branches[agentBranch], f.open[agentBranch] = true, 40 },
		"head missing":         func(*fakeDelivery) {},
	}
	for name, setUp := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fake := newFakeDelivery(t, deliveryToken)
			setUp(fake)
			prs, _ := pullRequests(fake)

			_, err := prs.Open(context.Background(), openRequest())
			var se *github.StatusError
			if !errors.As(err, &se) || se.Status != http.StatusUnprocessableEntity {
				t.Fatalf("Open error = %v, want a *StatusError 422", err)
			}
			assertNoDeliveryLeak(t, err)
		})
	}
}

func assertNoDeliveryLeak(t *testing.T, err error) {
	t.Helper()
	for _, leak := range []string{"Forja evidence", "secret-looking", deliveryToken} {
		if strings.Contains(err.Error(), leak) {
			t.Fatalf("error %q carries %q", err, leak)
		}
	}
}

func TestPullRequests_badInput_refusedBeforeAnyMint(t *testing.T) {
	t.Parallel()
	fake := newFakeDelivery(t, deliveryToken)
	prs, tokens := pullRequests(fake)
	for name, req := range map[string]github.PullRequestRequest{
		"malformed repo": {Repository: "a/b/c", Title: "t", Head: agentBranch, Base: "main"},
		"no head":        {Repository: fakeRepo, Title: "t", Base: "main"},
		"no base":        {Repository: fakeRepo, Title: "t", Head: agentBranch},
		"no title":       {Repository: fakeRepo, Head: agentBranch, Base: "main"},
	} {
		if _, err := prs.Open(context.Background(), req); err == nil {
			t.Errorf("%s: Open = nil error", name)
		}
	}
	if n := len(tokens.mints()); n != 0 {
		t.Fatalf("bad input minted %d tokens", n)
	}
}

func statusRequest() github.StatusRequest {
	return github.StatusRequest{
		Repository: fakeRepo, SHA: deliveryHead, State: github.StateSuccess, Context: "forja/gates",
		Description: "every gate passed", TargetURL: "https://github.com/" + fakeRepo + "/pull/41",
	}
}

func TestStatuses_publish_postsContextStateTargetMintsStatusesWrite(t *testing.T) {
	t.Parallel()
	fake := newFakeDelivery(t, deliveryToken)
	fake.commits[deliveryHead] = true
	tokens := &countingTokens{value: deliveryToken}
	statuses := &github.CommitStatuses{Tokens: tokens, BaseURL: fake.URL()}

	if err := statuses.Publish(context.Background(), statusRequest()); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	calls := fake.statusCalls()
	want := map[string]any{"state": "success", "context": "forja/gates", "description": "every gate passed", "target_url": "https://github.com/" + fakeRepo + "/pull/41"}
	if len(calls) != 1 || calls[0].SHA != deliveryHead || !reflect.DeepEqual(calls[0].Body, want) {
		t.Fatalf("statuses = %+v, want one on %s with %+v", calls, deliveryHead, want)
	}
	assertEveryMint(t, tokens, github.WriteStatuses)
}

func TestStatuses_unknownCommit_statusErrorWithoutToken(t *testing.T) {
	t.Parallel()
	fake := newFakeDelivery(t, deliveryToken)
	statuses := &github.CommitStatuses{Tokens: &countingTokens{value: deliveryToken}, BaseURL: fake.URL()}

	err := statuses.Publish(context.Background(), statusRequest())
	var se *github.StatusError
	if !errors.As(err, &se) || se.Status != http.StatusUnprocessableEntity {
		t.Fatalf("Publish error = %v, want a *StatusError 422", err)
	}
	assertNoDeliveryLeak(t, err)
}

func TestStatuses_badInput_refusedBeforeAnyMint(t *testing.T) {
	t.Parallel()
	fake := newFakeDelivery(t, deliveryToken)
	tokens := &countingTokens{value: deliveryToken}
	statuses := &github.CommitStatuses{Tokens: tokens, BaseURL: fake.URL()}
	for name, mutate := range map[string]func(*github.StatusRequest){
		"sha not a commit name": func(r *github.StatusRequest) { r.SHA = "main" },
		"sha with a path":       func(r *github.StatusRequest) { r.SHA = deliveryHead + "/../x" },
		"unknown state":         func(r *github.StatusRequest) { r.State = "green" },
		"no context":            func(r *github.StatusRequest) { r.Context = "" },
		"malformed repo":        func(r *github.StatusRequest) { r.Repository = "a/b/c" },
	} {
		req := statusRequest()
		mutate(&req)
		if err := statuses.Publish(context.Background(), req); err == nil {
			t.Errorf("%s: Publish = nil error", name)
		}
	}
	if n := len(tokens.mints()); n != 0 {
		t.Fatalf("bad input minted %d tokens", n)
	}
}
