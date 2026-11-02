package workspace_test

import (
	"archive/tar"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/client"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
	"github.com/fuseone-io/fuseone-forja/internal/gates"
	"github.com/fuseone-io/fuseone-forja/internal/proxy/registry"
	"github.com/fuseone-io/fuseone-forja/internal/workspace"
)

// testRegistry is declared but never resolves: these tests prove what
// reaches the proxy and what the workspace holds, not an upstream.
const testRegistry = "registry.forja-test.invalid/area"

func runRegistry(runID string) string { return "forja-run-" + runID + "-registry" }

// canary is a fresh credential, so a match anywhere is this run's leak.
func canary(t *testing.T) string {
	t.Helper()
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	return "forja-canary-" + hex.EncodeToString(b[:])
}

// withRegistry launches a run that declares testRegistry, with secret as
// its area's password.
func withRegistry(t *testing.T, secret string) (*workspace.Container, string) {
	t.Helper()
	d := newDockerWith(t, workspace.Options{RegistryCredentials: map[string]registry.Credential{
		testRegistry: {Username: "forja-reader", Password: secret},
	}})
	runID := newRunID(t)
	return launchSpec(t, d, workspace.LaunchSpec{RunID: runID, Image: testImage, Tree: treeWith(t), Registries: []string{testRegistry}}), runID
}

// The workspace finds the proxy by name and by the URL in its env, and
// keeps it off the gateway; the proxy runs as nobody with a way out.
func TestRegistry_declaredRegistry_workspaceReachesProxyByName(t *testing.T) {
	c, runID := withRegistry(t, canary(t))

	for _, command := range []string{resolve + "forja-registry", "nc -z -w 3 forja-registry 8080"} {
		if res := mustExec(t, c, command); res.ExitCode != 0 {
			t.Errorf("%s: exit %d, want 0\n%s%s", command, res.ExitCode, res.Stdout, res.Stderr)
		}
	}
	env := string(mustExec(t, c, "env").Stdout)
	for _, want := range []string{
		"FORJA_REGISTRY_URL=http://forja-registry:8080",
		"NO_PROXY=localhost,127.0.0.1,::1,forja-registry", "no_proxy=localhost,127.0.0.1,::1,forja-registry",
		"HTTPS_PROXY=http://forja-gateway:3128",
	} {
		if !strings.Contains(env, want+"\n") {
			t.Errorf("workspace env lacks %s:\n%s", want, env)
		}
	}
	proxy := inspect(t, runRegistry(runID))
	if proxy.Config.User != "65534" || !slices.Equal(proxy.HostConfig.CapDrop, []string{"ALL"}) || !slices.Contains(proxy.HostConfig.SecurityOpt, "no-new-privileges") {
		t.Errorf("registry user %q capdrop %v securityopt %v, want 65534, [ALL], no-new-privileges", proxy.Config.User, proxy.HostConfig.CapDrop, proxy.HostConfig.SecurityOpt)
	}
	if got := networksOf(proxy, runID); !slices.Equal(got, []string{"bridge", "run"}) {
		t.Errorf("registry networks = %v, want bridge and its run's", got)
	}
	if proxy.Config.Labels["forja.run"] != runID {
		t.Errorf("registry labels = %v, want forja.run=%s", proxy.Config.Labels, runID)
	}
	assertCredentialsFile(t, runRegistry(runID))
}

// assertCredentialsFile checks the file the proxy reads is its user's
// alone, readable and never writable.
func assertCredentialsFile(t *testing.T, id string) {
	t.Helper()
	res, err := dockerCLI(t).CopyFromContainer(context.Background(), id, client.CopyFromContainerOptions{SourcePath: "/run/forja/registries.json"})
	if err != nil {
		t.Fatalf("copy credentials file out: %v", err)
	}
	defer func() { _ = res.Content.Close() }()
	hdr, err := tar.NewReader(res.Content).Next()
	if err != nil {
		t.Fatalf("read credentials file header: %v", err)
	}
	if hdr.Mode&0o777 != 0o400 || hdr.Uid != 65534 || hdr.Gid != 65534 {
		t.Errorf("credentials file mode %o owner %d:%d, want 0400 65534:65534", hdr.Mode&0o777, hdr.Uid, hdr.Gid)
	}
}

// A run that declares no registry gets no proxy and no URL for one.
func TestRegistry_noRegistry_noProxyContainerNoURL(t *testing.T) {
	d := newDocker(t)
	runID := newRunID(t)
	c := launch(t, d, runID, treeWith(t))

	if env := string(mustExec(t, c, "env").Stdout); strings.Contains(env, "FORJA_REGISTRY_URL") || strings.Contains(env, "forja-registry") {
		t.Errorf("workspace env names a registry proxy for a run with none:\n%s", env)
	}
	assertGone(t, runRegistry(runID))
	report, err := c.Registry(context.Background())
	if err != nil || report.Count != 0 || len(report.Accesses) != 0 {
		t.Fatalf("Registry of a run with none = %+v, %v; want an empty report", report, err)
	}
}

// A request for a registry the contract did not declare fails in the
// workspace by its own exit code, and the control plane reads one denial
// with no query from the proxy's log.
func TestRegistry_undeclaredRegistry_deniedInReport(t *testing.T) {
	c, _ := withRegistry(t, canary(t))

	res := mustExec(t, c, `wget -Y off -T 5 -q -O- "$FORJA_REGISTRY_URL/example.com/x?token=query-secret"`)
	if res.ExitCode == 0 {
		t.Fatalf("wget of an undeclared registry exit 0, want a failure:\n%s", res.Stdout)
	}
	report, err := c.Registry(context.Background())
	if err != nil {
		t.Fatalf("Registry: %v", err)
	}
	if report.Count != 1 || len(report.Accesses) != 1 {
		t.Fatalf("report = %+v, want exactly one access", report)
	}
	got := report.Accesses[0]
	if got.Registry != "example.com" || got.Path != "/x" || got.Outcome != domain.RegistryDenied || got.Status != 403 || got.At.IsZero() {
		t.Fatalf("access = %+v, want example.com /x denied 403 with a time", got)
	}
}

// Through the real runner, the way forja.doctor runs it: the contract's
// registries get the run its proxy, and the report carries the access.
func TestRegistry_gateThroughRunner_accessInGateReport(t *testing.T) {
	d := newDockerWith(t, workspace.Options{RegistryCredentials: map[string]registry.Credential{
		testRegistry: {Username: "forja-reader", Password: canary(t)},
	}})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	runner := &gates.Runner{Launcher: pilotLauncher{d}, Now: time.Now}
	c := domain.Contract{Image: testImage, Registries: []string{testRegistry},
		Gates: []domain.Gate{{Name: "fetch", Run: `wget -Y off -T 5 -q -O- "$FORJA_REGISTRY_URL/example.com/x"`}}}

	report, err := runner.Run(ctx, newRunID(t), c, treeWith(t))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if fetch := report.Gates[0]; fetch.Status != domain.StepFailed || fetch.ExitCode == 0 {
		t.Fatalf("gate = {%s exit %d}, want failed by its exit code", fetch.Status, fetch.ExitCode)
	}
	if report.Registry.Count != 1 || report.Registry.Accesses[0].Registry != "example.com" || report.Registry.Accesses[0].Outcome != domain.RegistryDenied {
		t.Fatalf("report.Registry = %+v, want the one denied access of example.com", report.Registry)
	}
}

// The area's credential is nowhere the workspace can look, nor anywhere
// docker inspect or a log shows.
func TestRegistry_credential_absentFromWorkspace(t *testing.T) {
	secret := canary(t)
	c, runID := withRegistry(t, secret)

	streams := []workspace.ExecResult{
		mustExec(t, c, `wget -Y off -T 5 -q -O- "$FORJA_REGISTRY_URL/`+testRegistry+`/pkg"; true`),
		mustExec(t, c, "env"),
		// Every directory but the kernel's: the command line of this very
		// grep, which holds the canary, lives under /proc.
		mustExec(t, c, `for d in /*; do case "$d" in /proc|/sys|/dev) ;; *) grep -r -s -F -l "`+secret+`" "$d" ;; esac; done; true`),
	}
	for i, res := range streams {
		if strings.Contains(string(res.Stdout)+string(res.Stderr), secret) {
			t.Errorf("command %d in the workspace saw the credential:\n%s%s", i, res.Stdout, res.Stderr)
		}
	}
	proxy := inspect(t, runRegistry(runID))
	shown := strings.Join(append(append(append([]string{}, proxy.Config.Env...), proxy.Config.Cmd...), proxy.Args...), "\n")
	if strings.Contains(shown, secret) {
		t.Errorf("docker inspect of the registry shows the credential:\n%s", shown)
	}
	if log := rawLog(t, runRegistry(runID)); strings.Contains(log, secret) || !strings.Contains(log, `"outcome":"allowed"`) {
		t.Errorf("registry log carries the credential or lacks the allowed access:\n%s", log)
	}
}

// rawLog is a container's stdout and stderr as the daemon keeps them.
func rawLog(t *testing.T, id string) string {
	t.Helper()
	logs, err := dockerCLI(t).ContainerLogs(context.Background(), id, client.ContainerLogsOptions{ShowStdout: true, ShowStderr: true})
	if err != nil {
		t.Fatalf("logs of %s: %v", id, err)
	}
	defer func() { _ = logs.Close() }()
	b, err := io.ReadAll(logs)
	if err != nil {
		t.Fatalf("read logs of %s: %v", id, err)
	}
	return string(b)
}

// A declared registry the operator has no credential for refuses the
// launch before anything of the run exists.
func TestLaunch_declaredRegistryWithoutCredential_refusedBeforeCreate(t *testing.T) {
	d := newDocker(t)
	runID := newRunID(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	_, err := d.Launch(ctx, workspace.LaunchSpec{RunID: runID, Image: testImage, Tree: treeWith(t), Registries: []string{"ghcr.io/my-org"}})
	if !errors.Is(err, workspace.ErrRegistryCredentialMissing) || !strings.Contains(err.Error(), "ghcr.io/my-org") {
		t.Fatalf("Launch = %v, want ErrRegistryCredentialMissing naming ghcr.io/my-org", err)
	}
	if n := leftovers(t, runID); n != 0 {
		t.Fatalf("%d containers or networks created for a refused launch", n)
	}
}

// A proxy that stopped may have served requests it never logged: its log
// is refused, and Close still removes it with the rest.
func TestRegistryLog_proxyNotRunning_refused(t *testing.T) {
	c, runID := withRegistry(t, canary(t))
	ctx := context.Background()
	if _, err := dockerCLI(t).ContainerKill(ctx, runRegistry(runID), client.ContainerKillOptions{}); err != nil {
		t.Fatalf("kill registry: %v", err)
	}

	if report, err := c.Registry(ctx); err == nil {
		t.Fatalf("Registry after the proxy stopped = %+v, nil error; want a failure", report)
	}
	if err := c.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}
	assertGone(t, runRegistry(runID))
	if err := c.Close(ctx); err != nil {
		t.Fatalf("second Close: %v, want nil: Close is idempotent", err)
	}
}
