package workspace_test

import (
	"archive/tar"
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
	"github.com/fuseone-io/fuseone-forja/internal/workspace"
)

// agentImage ships curl and git, which the agent workspace tests need, and
// is the pilot's own image.
const agentImage = "golang:1.25"

// fakeKey stands for the operator's real key. It never has to reach the
// workspace for the fake to answer.
const fakeKey = "sk-ant-fake-real-key-workspace-test-0042"

func runModel(runID string) string { return "forja-run-" + runID + "-model" }

// fakeBuild cross-compiles testdata/fakemodel once per test binary.
var fakeBuild struct {
	once sync.Once
	path string
	err  error
}

func fakeModelBinary(t *testing.T) string {
	t.Helper()
	fakeBuild.once.Do(func() {
		v, err := dockerCLI(t).ServerVersion(context.Background(), client.ServerVersionOptions{})
		if err != nil {
			fakeBuild.err = err
			return
		}
		dir, err := os.MkdirTemp("", "forja-fakemodel-")
		if err != nil {
			fakeBuild.err = err
			return
		}
		fakeBuild.path = filepath.Join(dir, "fakemodel")
		build := exec.Command("go", "build", "-trimpath", "-o", fakeBuild.path, "./testdata/fakemodel")
		build.Env = append(os.Environ(), "GOOS=linux", "GOARCH="+v.Arch, "CGO_ENABLED=0")
		if out, err := build.CombinedOutput(); err != nil {
			fakeBuild.err = fmt.Errorf("go build fakemodel: %w\n%s", err, out)
		}
	})
	if fakeBuild.err != nil {
		t.Fatalf("fake model binary: %v", fakeBuild.err)
	}
	return fakeBuild.path
}

// fakeUpstream starts the fake Messages API on the default bridge, where
// only a container attached to it, such as a run's model proxy, can reach
// it. It returns its URL and its container id. Like every test here, it
// skips when no daemon answers.
func fakeUpstream(t *testing.T, bash string) (string, string) {
	t.Helper()
	newDocker(t)
	cli, ctx := dockerCLI(t), context.Background()
	created, err := cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config:     &container.Config{Image: testImage, Cmd: []string{"/fakemodel"}, Env: []string{"FAKE_KEY=" + fakeKey, "FAKE_BASH=" + bash}},
		HostConfig: &container.HostConfig{NetworkMode: "bridge"},
	})
	if err != nil {
		t.Fatalf("create fake upstream: %v", err)
	}
	t.Cleanup(func() {
		_, _ = cli.ContainerRemove(context.Background(), created.ID, client.ContainerRemoveOptions{Force: true})
	})
	binary, err := os.ReadFile(fakeModelBinary(t))
	if err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	tw := tar.NewWriter(&archive)
	_ = tw.WriteHeader(&tar.Header{Name: "fakemodel", Mode: 0o755, Size: int64(len(binary)), Typeflag: tar.TypeReg})
	_, _ = tw.Write(binary)
	_ = tw.Close()
	if _, err := cli.CopyToContainer(ctx, created.ID, client.CopyToContainerOptions{DestinationPath: "/", Content: &archive}); err != nil {
		t.Fatalf("copy fake upstream: %v", err)
	}
	if _, err := cli.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		t.Fatalf("start fake upstream: %v", err)
	}
	ep := inspect(t, created.ID).NetworkSettings.Networks["bridge"]
	if ep == nil || !ep.IPAddress.IsValid() {
		t.Fatal("fake upstream has no bridge address")
	}
	return "http://" + ep.IPAddress.String() + ":8000", created.ID
}

// fakeServed sums the usage the fake upstream says it sent.
func fakeServed(t *testing.T, id string) domain.TokenUsage {
	t.Helper()
	logs, err := dockerCLI(t).ContainerLogs(context.Background(), id, client.ContainerLogsOptions{ShowStdout: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = logs.Close() }()
	var out bytes.Buffer
	if _, err := stdcopy.StdCopy(&out, &bytes.Buffer{}, logs); err != nil {
		t.Fatal(err)
	}
	var total domain.TokenUsage
	sc := bufio.NewScanner(&out)
	for sc.Scan() {
		if !strings.Contains(sc.Text(), `"input"`) {
			continue
		}
		var raw map[string]int64
		if err := json.Unmarshal(sc.Bytes(), &raw); err != nil {
			t.Fatalf("fake upstream line %q: %v", sc.Text(), err)
		}
		total = total.Add(domain.TokenUsage{Input: raw["input"], CacheCreation: raw["cache_creation"], CacheRead: raw["cache_read"], Output: raw["output"]})
	}
	return total
}

// agentBinaryFile is a stand-in for the linux claude, enough to prove the
// driver installed it.
func agentBinaryFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "claude")
	if err := os.WriteFile(path, []byte("#!/bin/sh\necho fake-claude\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// agentDocker is newDocker with the model proxy configured to reach
// upstream with fakeKey under tokenCap, and agent installed as the agent.
func agentDocker(t *testing.T, upstream string, tokenCap int64, agent string) *workspace.Docker {
	t.Helper()
	newDocker(t)
	d, err := workspace.NewDocker(workspace.Options{
		GatewayBinary: gatewayBinary(t),
		AgentBinary:   agent,
		Model:         workspace.ModelOptions{Key: fakeKey, Upstream: upstream, Cap: tokenCap},
	})
	if err != nil {
		t.Fatalf("NewDocker: %v", err)
	}
	return d
}

func launchAgent(t *testing.T, d *workspace.Docker, runID string) *workspace.Container {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	c, err := d.LaunchAgent(ctx, runID, agentImage, treeWith(t))
	if err != nil {
		t.Fatalf("LaunchAgent: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = c.Close(ctx)
	})
	return c
}

// postMessages is the agent's call, made with what the workspace was given.
const postMessages = `curl -sS -o /tmp/answer -w '%{http_code}' -X POST -H "x-api-key: $ANTHROPIC_API_KEY" ` +
	`-H 'content-type: application/json' -d '{"stream":true}' "$ANTHROPIC_BASE_URL/v1/messages?beta=true"; cat /tmp/answer`

// From the workspace, the model is the proxy on the run network: the fake
// upstream answers, which it does only for the real key. Anything else
// still goes to the gateway and is refused.
func TestDocker_launchAgent_modelReachedThroughProxyOtherEgressDenied(t *testing.T) {
	upstream, fakeID := fakeUpstream(t, "")
	c := launchAgent(t, agentDocker(t, upstream, 1_000_000, agentBinaryFile(t)), newRunID(t))

	res := mustExec(t, c, postMessages)
	if res.ExitCode != 0 || !strings.HasPrefix(string(res.Stdout), "200") || !strings.Contains(string(res.Stdout), "event: message_start") {
		t.Fatalf("POST to the model from the workspace = exit %d\n%s%s\nwant 200 and the fake's stream", res.ExitCode, res.Stdout, res.Stderr)
	}
	assertExampleDenied(t, c)
	usage, err := c.Model(context.Background())
	if err != nil || usage.Capped || usage.Tokens != fakeServed(t, fakeID) || usage.Tokens.Total() == 0 {
		t.Fatalf("Model = %+v, %v; want the fake's usage %+v uncapped", usage, err, fakeServed(t, fakeID))
	}
}

// assertExampleDenied checks a tunnel to example.com from the workspace
// fails and is the one denial the gateway recorded.
func assertExampleDenied(t *testing.T, c *workspace.Container) {
	t.Helper()
	if res := mustExec(t, c, "curl -sS -m 10 https://example.com/"); res.ExitCode == 0 {
		t.Fatal("curl https://example.com from the agent workspace exit 0, want the gateway's refusal")
	}
	report, err := c.Egress(context.Background())
	if err != nil || report.Count != 1 || report.Denied[0].Destination != "example.com:443" || report.Denied[0].Kind != domain.EgressConnect {
		t.Fatalf("Egress = %+v, %v; want one connect example.com:443", report, err)
	}
}

// envOf is the workspace's environment as a map.
func envOf(t *testing.T, c *workspace.Container) map[string]string {
	t.Helper()
	res := mustExec(t, c, "env")
	env := map[string]string{}
	for _, line := range strings.Split(string(res.Stdout), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			env[k] = v
		}
	}
	return env
}

// Each agent launch gets its own opaque token; the real key is in neither
// workspace, nor in the model container's configuration, and its file sits
// in the model container only, readable by the proxy's user alone.
func TestDocker_launchAgent_ownTokenNoKeyInWorkspaceOrConfig(t *testing.T) {
	t.Setenv("FORJA_GITHUB_TOKEN", "secret-github-token-0123")
	upstream, _ := fakeUpstream(t, "")
	d := agentDocker(t, upstream, 1_000_000, agentBinaryFile(t))
	runA, runB := newRunID(t), newRunID(t)
	a, b := launchAgent(t, d, runA), launchAgent(t, d, runB)

	envA, envB := envOf(t, a), envOf(t, b)
	tokA, tokB := envA["ANTHROPIC_API_KEY"], envB["ANTHROPIC_API_KEY"]
	for _, tok := range []string{tokA, tokB} {
		if len(tok) < 64 || strings.Trim(tok, "0123456789abcdef") != "" || tok == fakeKey {
			t.Errorf("run token %q, want at least 64 hex characters that are not the key", tok)
		}
	}
	if tokA == tokB {
		t.Fatalf("two launches share the token %q", tokA)
	}
	assertAgentEnv(t, envA)
	if res := mustExec(t, a, "test ! -e /forja/key && test ! -e /forja/token && ! grep -rqs '"+fakeKey+"' /workspace /forja /etc /root"); res.ExitCode != 0 {
		t.Fatalf("the key or its file is visible in the workspace: exit %d %s", res.ExitCode, res.Stderr)
	}
	m := inspect(t, runModel(runA))
	if config := strings.Join(append(m.Config.Env, m.Config.Cmd...), "\n"); strings.Contains(config, fakeKey) || strings.Contains(config, tokA) {
		t.Fatalf("model container config carries a secret:\n%s", config)
	}
	assertNoSecrets(t, "model", strings.Join(m.Config.Env, "\n"))
	assertModelHardened(t, m, runA)
}

func assertAgentEnv(t *testing.T, env map[string]string) {
	t.Helper()
	want := map[string]string{
		"ANTHROPIC_BASE_URL": "http://forja-model:8080",
		"NO_PROXY":           "localhost,127.0.0.1,::1,forja-model",
		"no_proxy":           "localhost,127.0.0.1,::1,forja-model",
		"HTTPS_PROXY":        "http://forja-gateway:3128",
	}
	for k, v := range want {
		if env[k] != v {
			t.Errorf("workspace %s = %q, want %q", k, env[k], v)
		}
	}
	for k, v := range env {
		if strings.Contains(v, fakeKey) || strings.Contains(k, "FORJA_") {
			t.Errorf("workspace env %s=%s carries a control-plane secret", k, v)
		}
	}
}

// assertModelHardened checks the model container copies the gateway's
// posture, is the only one on the bridge besides the gateway's own
// choice, and that the secrets are its user's alone.
func assertModelHardened(t *testing.T, m container.InspectResponse, runID string) {
	t.Helper()
	if m.Config.User != "65534" || !slices.Equal(m.HostConfig.CapDrop, []string{"ALL"}) || !slices.Contains(m.HostConfig.SecurityOpt, "no-new-privileges") {
		t.Errorf("model user %q capdrop %v securityopt %v, want 65534, [ALL], no-new-privileges", m.Config.User, m.HostConfig.CapDrop, m.HostConfig.SecurityOpt)
	}
	if got := networksOf(m, runID); !slices.Equal(got, []string{"bridge", "run"}) {
		t.Errorf("model networks = %v, want bridge and run", got)
	}
	res := execIn(t, runModel(runID), "stat", "-c", "%u %a", "/forja/key", "/forja/token")
	if res != "65534 400\n65534 400\n" {
		t.Errorf("secret files in the model container = %q, want owner 65534 mode 400", res)
	}
}

// execIn runs argv in a container the driver does not hand out, as the
// container's own user, and returns its stdout.
func execIn(t *testing.T, id string, argv ...string) string {
	t.Helper()
	out, err := exec.Command("docker", append([]string{"exec", id}, argv...)...).Output()
	if err != nil {
		t.Fatalf("docker exec %s %v: %v", id, argv, err)
	}
	return string(out)
}

// Close takes the model proxy down with the rest, so the run token dies
// with the run.
func TestDocker_launchAgent_closeRemovesModelWithTheRest(t *testing.T) {
	upstream, _ := fakeUpstream(t, "")
	runID := newRunID(t)
	c := launchAgent(t, agentDocker(t, upstream, 1_000_000, agentBinaryFile(t)), runID)

	if err := c.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	for _, id := range []string{c.ID(), runModel(runID), runGateway(runID)} {
		assertGone(t, id)
	}
	assertNetworkGone(t, c.NetworkID())
	if _, err := c.Model(context.Background()); err == nil {
		t.Fatal("Model after Close = nil error, want a failure")
	}
}

// A launch that cannot finish leaves nothing of its run behind, and one
// without an agent binary or a key is refused before anything exists.
func TestDocker_launchAgentFails_nothingLeft(t *testing.T) {
	upstream, _ := fakeUpstream(t, "")
	tests := []struct {
		name, image string
		opts        func(*workspace.Options)
	}{
		{name: "image unknown", image: "forja-no-such-image-0000:none", opts: func(*workspace.Options) {}},
		{name: "agent binary missing", image: agentImage, opts: func(o *workspace.Options) { o.AgentBinary = filepath.Join(t.TempDir(), "no-claude") }},
		{name: "agent binary unset", image: agentImage, opts: func(o *workspace.Options) { o.AgentBinary = "" }},
		{name: "no key", image: agentImage, opts: func(o *workspace.Options) { o.Model.Key = "" }},
		{name: "proxy cannot start", image: agentImage, opts: func(o *workspace.Options) { o.Model.Upstream = "not a url" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			newDocker(t)
			opts := workspace.Options{GatewayBinary: gatewayBinary(t), AgentBinary: agentBinaryFile(t), Model: workspace.ModelOptions{Key: fakeKey, Upstream: upstream, Cap: 10}}
			tt.opts(&opts)
			d, err := workspace.NewDocker(opts)
			if err != nil {
				t.Fatal(err)
			}
			runID := newRunID(t)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()

			if c, err := d.LaunchAgent(ctx, runID, tt.image, treeWith(t)); err == nil {
				_ = c.Close(context.Background())
				t.Fatal("LaunchAgent = nil error, want a failure")
			}
			if n := leftovers(t, runID); n != 0 {
				t.Fatalf("%d containers or networks of the failed launch left behind", n)
			}
		})
	}
}

// The doctor's and the gates' launch never gets a model proxy: a gate
// command must not be able to spend the operator's key.
func TestDocker_plainLaunch_noModelProxy(t *testing.T) {
	upstream, _ := fakeUpstream(t, "")
	d := agentDocker(t, upstream, 1_000_000, agentBinaryFile(t))
	runID := newRunID(t)
	c := launch(t, d, runID, treeWith(t))

	if _, err := dockerCLI(t).ContainerInspect(context.Background(), runModel(runID), client.ContainerInspectOptions{}); !cerrdefs.IsNotFound(err) {
		t.Fatalf("model container of a plain launch: %v, want not found", err)
	}
	if env := envOf(t, c); env["ANTHROPIC_API_KEY"] != "" || env["ANTHROPIC_BASE_URL"] != "" {
		t.Fatalf("plain launch env has model variables: %v", env)
	}
	if _, err := c.Model(context.Background()); err == nil {
		t.Fatal("Model of a plain launch = nil error, want a failure")
	}
}

// A model proxy that stopped may have forwarded what it never logged: its
// usage is refused, not handed over short.
func TestDocker_modelStopped_usageRefused(t *testing.T) {
	upstream, _ := fakeUpstream(t, "")
	runID := newRunID(t)
	c := launchAgent(t, agentDocker(t, upstream, 1_000_000, agentBinaryFile(t)), runID)
	if _, err := dockerCLI(t).ContainerKill(context.Background(), runModel(runID), client.ContainerKillOptions{}); err != nil {
		t.Fatalf("kill model: %v", err)
	}

	if report, err := c.Model(context.Background()); err == nil {
		t.Fatalf("Model after the proxy stopped = %+v, nil error; want a failure", report)
	}
}

// With a cap one response reaches, the next request is refused and the
// control plane hears about it.
func TestDocker_smallCap_nextRequest429AndAwaitCapReturns(t *testing.T) {
	upstream, _ := fakeUpstream(t, "")
	c := launchAgent(t, agentDocker(t, upstream, 1, agentBinaryFile(t)), newRunID(t))
	capped := make(chan error, 1)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	go func() { capped <- c.AwaitCap(ctx) }()

	if res := mustExec(t, c, postMessages); !strings.HasPrefix(string(res.Stdout), "200") {
		t.Fatalf("first POST = %s, want 200", res.Stdout)
	}
	select {
	case err := <-capped:
		if err != nil {
			t.Fatalf("AwaitCap = %v, want nil once the cap was reached", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("AwaitCap did not return after the cap was reached")
	}
	res := mustExec(t, c, postMessages)
	if !strings.HasPrefix(string(res.Stdout), "429") || !strings.Contains(string(res.Stdout), "forja: run token cap reached") {
		t.Fatalf("POST after the cap = %s, want the 429 refusal", res.Stdout)
	}
	if report, err := c.Model(context.Background()); err != nil || !report.Capped {
		t.Fatalf("Model = %+v, %v; want capped", report, err)
	}
}

// The agent is installed root-owned and executable; the prompt is written
// outside the tree; Halt stops the workspace and leaves the proxies'
// logs readable.
func TestDocker_launchAgent_agentInstalledPromptWrittenHaltKeepsLogs(t *testing.T) {
	upstream, _ := fakeUpstream(t, "")
	c := launchAgent(t, agentDocker(t, upstream, 1_000_000, agentBinaryFile(t)), newRunID(t))
	ctx := context.Background()
	prompt := []byte("# Issue\n\nline with 'quotes' and $VARS\n")

	if err := c.WriteFile(ctx, "/forja/prompt.md", prompt); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	res := mustExec(t, c, "stat -c '%u %a' /usr/local/bin/claude && claude && cat /forja/prompt.md")
	if want := "0 755\nfake-claude\n" + string(prompt); string(res.Stdout) != want {
		t.Fatalf("agent and prompt = %q, want %q", res.Stdout, want)
	}
	if err := c.Halt(ctx); err != nil {
		t.Fatalf("Halt: %v", err)
	}
	assertGone(t, c.ID())
	if _, err := c.Model(ctx); err != nil {
		t.Fatalf("Model after Halt: %v, want the log still readable", err)
	}
	if _, err := c.Egress(ctx); err != nil {
		t.Fatalf("Egress after Halt: %v, want the log still readable", err)
	}
}
