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

func labelRequest() github.LabelRequest {
	return github.LabelRequest{Repository: fakeRepo, Number: 41, Labels: []string{"forja/human-merge"}}
}

func TestPullRequests_label_postsLabelsMintsPullRequestsWrite(t *testing.T) {
	t.Parallel()
	fake := newFakeDelivery(t, deliveryToken)
	fake.open[agentBranch] = 41
	prs, tokens := pullRequests(fake)

	if err := prs.Label(context.Background(), labelRequest()); err != nil {
		t.Fatalf("Label: %v", err)
	}
	if got := fake.labelsOf(41); !reflect.DeepEqual(got, []string{"forja/human-merge"}) {
		t.Fatalf("labels on 41 = %q, want forja/human-merge", got)
	}
	if !reflect.DeepEqual(tokens.mints(), []string{fakeRepo}) {
		t.Fatalf("mints = %q, want one for %s", tokens.mints(), fakeRepo)
	}
	assertEveryMint(t, tokens, github.WritePullRequests)
}

func TestPullRequests_labelRefused_statusErrorWithoutToken(t *testing.T) {
	t.Parallel()
	fake := newFakeDelivery(t, deliveryToken)
	fake.open[agentBranch] = 40
	prs, _ := pullRequests(fake)

	err := prs.Label(context.Background(), labelRequest())
	var se *github.StatusError
	if !errors.As(err, &se) || se.Status != http.StatusNotFound {
		t.Fatalf("Label error = %v, want a *StatusError 404", err)
	}
	assertNoDeliveryLeak(t, err)
	if got := fake.labelsOf(40); len(got) != 0 {
		t.Fatalf("labels on 40 = %q, want none", got)
	}
}

func TestPullRequests_labelBadInput_refusedBeforeAnyMint(t *testing.T) {
	t.Parallel()
	fake := newFakeDelivery(t, deliveryToken)
	fake.open[agentBranch] = 41
	prs, tokens := pullRequests(fake)
	for name, mutate := range map[string]func(*github.LabelRequest){
		"malformed repo":  func(r *github.LabelRequest) { r.Repository = "a/b/c" },
		"no number":       func(r *github.LabelRequest) { r.Number = 0 },
		"negative number": func(r *github.LabelRequest) { r.Number = -41 },
		"no labels":       func(r *github.LabelRequest) { r.Labels = nil },
		"empty label":     func(r *github.LabelRequest) { r.Labels = []string{"forja/human-merge", ""} },
	} {
		req := labelRequest()
		mutate(&req)
		if err := prs.Label(context.Background(), req); err == nil {
			t.Errorf("%s: Label = nil error", name)
		}
	}
	if n := len(tokens.mints()); n != 0 {
		t.Fatalf("bad input minted %d tokens", n)
	}
}

// forjaLogin is the login a Forja App's pull requests are authored by.
const forjaLogin = "forja-dev[bot]"

// roundPulls are the pull requests a correction round's lookup must tell
// apart: the open one of the branch, a closed one of the same branch, and
// an open one of another branch.
func roundPulls() []fakePull {
	return []fakePull{
		{number: 40, state: "closed", head: agentBranch, author: forjaLogin, base: "main", headRepo: fakeRepo},
		{number: 57, state: "open", head: agentBranch, author: forjaLogin, base: "release", headRepo: fakeRepo},
		{number: 58, state: "open", head: "forja/8", author: forjaLogin, base: "main", headRepo: fakeRepo},
	}
}

// Find asks for the open pull requests of owner:branch, which is the only
// head filter the API honours, with a token that only reads pull requests.
func TestPullRequests_find_openPullOfHeadWithAuthorBaseAndHeadRepo(t *testing.T) {
	t.Parallel()
	fake := newFakeDelivery(t, deliveryToken)
	fake.listed = roundPulls()
	prs, tokens := pullRequests(fake)

	got, err := prs.Find(context.Background(), fakeRepo, agentBranch)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	want := domain.PullRequest{Number: 57, URL: "https://github.com/" + fakeRepo + "/pull/57", Author: forjaLogin, Base: "release", HeadRepository: fakeRepo}
	if got != want {
		t.Fatalf("Find = %+v, want %+v", got, want)
	}
	queries := fake.listQueries()
	if len(queries) != 1 || queries[0].Get("state") != "open" || queries[0].Get("head") != fakeOwner+":"+agentBranch {
		t.Fatalf("queries = %v, want one for the open pull requests of %s:%s", queries, fakeOwner, agentBranch)
	}
	assertEveryMint(t, tokens, github.ReadPullRequests)
}

func TestPullRequests_find_none_errNoPullRequest(t *testing.T) {
	t.Parallel()
	fake := newFakeDelivery(t, deliveryToken)
	fake.listed = roundPulls()[:1]
	prs, _ := pullRequests(fake)

	_, err := prs.Find(context.Background(), fakeRepo, agentBranch)
	if !errors.Is(err, github.ErrNoPullRequest) {
		t.Fatalf("Find error = %v, want ErrNoPullRequest for a branch whose only pull request is closed", err)
	}
	assertNoDeliveryLeak(t, err)
}

// Two open pull requests from one head, into two bases, leave no single
// pull request to work on.
func TestPullRequests_find_moreThanOne_refused(t *testing.T) {
	t.Parallel()
	fake := newFakeDelivery(t, deliveryToken)
	fake.listed = append(roundPulls(), fakePull{number: 59, state: "open", head: agentBranch, author: forjaLogin, base: "main", headRepo: fakeRepo})
	prs, _ := pullRequests(fake)

	got, err := prs.Find(context.Background(), fakeRepo, agentBranch)
	if !errors.Is(err, github.ErrManyPullRequests) {
		t.Fatalf("Find = %+v, %v; want ErrManyPullRequests", got, err)
	}
}

// A listing that did not honour the head filter answers another branch's
// pull request, which is never taken for the branch's own.
func TestPullRequests_find_listingIgnoresHead_otherBranchRefused(t *testing.T) {
	t.Parallel()
	fake := newFakeDelivery(t, deliveryToken)
	fake.listed = roundPulls()[2:]
	fake.unfiltered = true
	prs, _ := pullRequests(fake)

	got, err := prs.Find(context.Background(), fakeRepo, agentBranch)
	if err == nil {
		t.Fatalf("Find = %+v, want an error for a listing of another branch's pull request", got)
	}
}

func TestPullRequests_findBadInput_refusedBeforeAnyMint(t *testing.T) {
	t.Parallel()
	fake := newFakeDelivery(t, deliveryToken)
	prs, tokens := pullRequests(fake)
	for name, in := range map[string][2]string{
		"malformed repo":      {"a/b/c", agentBranch},
		"no branch":           {fakeRepo, ""},
		"branch an option":    {fakeRepo, "--x"},
		"branch with a colon": {fakeRepo, "other:forja/7"},
	} {
		if _, err := prs.Find(context.Background(), in[0], in[1]); err == nil {
			t.Errorf("%s: Find = nil error", name)
		}
	}
	if n := len(tokens.mints()); n != 0 {
		t.Fatalf("bad input minted %d tokens", n)
	}
}

func TestPullRequests_comment_postsBodyMintsPullRequestsWrite(t *testing.T) {
	t.Parallel()
	fake := newFakeDelivery(t, deliveryToken)
	fake.listed = roundPulls()
	prs, tokens := pullRequests(fake)

	if err := prs.Comment(context.Background(), github.CommentRequest{Repository: fakeRepo, Number: 57, Body: evidenceBody}); err != nil {
		t.Fatalf("Comment: %v", err)
	}
	calls := fake.commentCalls()
	if len(calls) != 1 || calls[0].Number != 57 || !reflect.DeepEqual(calls[0].Body, map[string]any{"body": evidenceBody}) {
		t.Fatalf("comments = %+v, want one on #57 with the evidence as its only field", calls)
	}
	assertEveryMint(t, tokens, github.WritePullRequests)
}

// A comment the API refuses is the status and its message, never the
// evidence sent nor the token; bad input mints nothing.
func TestPullRequests_commentRefusedOrBadInput_noLeakNoMint(t *testing.T) {
	t.Parallel()
	fake := newFakeDelivery(t, deliveryToken)
	fake.listed = roundPulls()
	prs, tokens := pullRequests(fake)

	err := prs.Comment(context.Background(), github.CommentRequest{Repository: fakeRepo, Number: 99, Body: evidenceBody})
	var se *github.StatusError
	if !errors.As(err, &se) || se.Status != http.StatusNotFound {
		t.Fatalf("Comment on an unknown number = %v, want a *StatusError 404", err)
	}
	assertNoDeliveryLeak(t, err)
	minted := len(tokens.mints())
	for name, req := range map[string]github.CommentRequest{
		"malformed repo": {Repository: "a/b/c", Number: 57, Body: evidenceBody},
		"no number":      {Repository: fakeRepo, Body: evidenceBody},
		"no body":        {Repository: fakeRepo, Number: 57},
	} {
		if err := prs.Comment(context.Background(), req); err == nil {
			t.Errorf("%s: Comment = nil error", name)
		}
	}
	if n := len(tokens.mints()); n != minted {
		t.Fatalf("bad input minted %d tokens", n-minted)
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
