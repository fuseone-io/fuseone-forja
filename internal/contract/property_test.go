package contract_test

import (
	"math/rand"
	"reflect"
	"sort"
	"testing"
	"testing/quick"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/fuseone-io/fuseone-forja/internal/contract"
	"github.com/fuseone-io/fuseone-forja/internal/domain"
)

// The YAML writer is test-local: the package under test never renders a
// contract, so the tagged mirror lives here.
type yamlContract struct {
	Version         int                    `yaml:"version"`
	Image           string                 `yaml:"image"`
	Setup           []string               `yaml:"setup"`
	Services        map[string]yamlService `yaml:"services"`
	Registries      []string               `yaml:"registries"`
	Gates           []yamlGate             `yaml:"gates"`
	Verify          yamlVerify             `yaml:"verify"`
	ProofPaths      []string               `yaml:"proof_paths"`
	HumanMergePaths []string               `yaml:"human_merge_paths"`
	Limits          yamlLimits             `yaml:"limits"`
	AgentContext    []string               `yaml:"agent_context"`
}

type yamlService struct {
	Image string            `yaml:"image"`
	Env   map[string]string `yaml:"env"`
}

type yamlGate struct {
	Name  string   `yaml:"name"`
	Run   string   `yaml:"run"`
	Needs []string `yaml:"needs"`
}

type yamlVerify struct {
	Exclude []string `yaml:"exclude"`
}

type yamlLimits struct {
	WallClock    string `yaml:"wall_clock"`
	MaxDiffLines int    `yaml:"max_diff_lines"`
	MaxAttempts  int    `yaml:"max_attempts"`
}

func render(t *testing.T, c domain.Contract) []byte {
	t.Helper()
	y := yamlContract{
		Version:         c.Version,
		Image:           c.Image,
		Setup:           c.Setup,
		Services:        map[string]yamlService{},
		Registries:      c.Registries,
		Gates:           []yamlGate{},
		Verify:          yamlVerify{Exclude: c.Verify.Exclude},
		ProofPaths:      c.ProofPaths,
		HumanMergePaths: c.HumanMergePaths,
		Limits:          yamlLimits{WallClock: c.Limits.WallClock.String(), MaxDiffLines: c.Limits.MaxDiffLines, MaxAttempts: c.Limits.MaxAttempts},
		AgentContext:    c.AgentContext,
	}
	for name, s := range c.Services {
		y.Services[name] = yamlService{Image: s.Image, Env: s.Env}
	}
	for _, g := range c.Gates {
		y.Gates = append(y.Gates, yamlGate{Name: g.Name, Run: g.Run, Needs: g.Needs})
	}
	out, err := yaml.Marshal(y)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return out
}

// validContract generates contracts the parser must accept unchanged.
type validContract domain.Contract

var (
	words = []string{"build", "lint", "test", "race", "vet", "integration", "postgres", "redis", "api", "web"}
	globs = []string{"**/*.md", "docs/**", "scripts/**", "**/testdata/**", "Makefile", ".forja.yaml", "**/*_test.go", "[abc]*.go", "a?c"}
)

func pick(r *rand.Rand, from []string) string {
	return from[r.Intn(len(from))]
}

func pickList(r *rand.Rand, from []string) []string {
	n := r.Intn(4)
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, pick(r, from))
	}
	return out
}

// uniqueNames draws between 1 and 4 distinct names.
func uniqueNames(r *rand.Rand) []string {
	set := map[string]bool{}
	for i := 0; i < 1+r.Intn(4); i++ {
		set[pick(r, words)] = true
	}
	names := make([]string, 0, len(set))
	for n := range set {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func genServices(r *rand.Rand) map[string]domain.Service {
	services := map[string]domain.Service{}
	for _, name := range pickList(r, words) {
		env := map[string]string{}
		for _, k := range pickList(r, []string{"POSTGRES_PASSWORD", "PORT", "MODE"}) {
			env[k] = pick(r, []string{"test", "5432", "dev"})
		}
		services[name] = domain.Service{Image: pick(r, words) + ":17", Env: env}
	}
	return services
}

func genGates(r *rand.Rand, services map[string]domain.Service) []domain.Gate {
	serviceNames := make([]string, 0, len(services))
	for n := range services {
		serviceNames = append(serviceNames, n)
	}
	sort.Strings(serviceNames)
	gates := []domain.Gate{}
	for _, name := range uniqueNames(r) {
		needs := []string{}
		if len(serviceNames) > 0 {
			needs = pickList(r, serviceNames)
		}
		gates = append(gates, domain.Gate{Name: name, Run: "make " + name, Needs: needs})
	}
	return gates
}

// Generate implements quick.Generator.
func (validContract) Generate(r *rand.Rand, _ int) reflect.Value {
	services := genServices(r)
	c := validContract{
		Version:         1,
		Image:           pick(r, words) + ":1.25",
		Setup:           pickList(r, []string{"go mod download", "make deps"}),
		Services:        services,
		Registries:      pickList(r, []string{"ghcr.io/my-org", "registry.example.com"}),
		Gates:           genGates(r, services),
		Verify:          domain.Verify{Exclude: pickList(r, globs)},
		ProofPaths:      pickList(r, globs),
		HumanMergePaths: pickList(r, globs),
		Limits:          domain.Limits{WallClock: time.Duration(1+r.Intn(120)) * time.Minute, MaxDiffLines: 1 + r.Intn(2000), MaxAttempts: 1 + r.Intn(10)},
		AgentContext:    pickList(r, []string{"CLAUDE.md", "docs/", "CONTRIBUTING.md"}),
	}
	return reflect.ValueOf(c)
}

func TestParse_roundTrip_normalisedContractIsStable(t *testing.T) {
	t.Parallel()
	property := func(v validContract) bool {
		want := domain.Contract(v)
		got, err := contract.Parse(render(t, want))
		if err != nil {
			t.Logf("Parse rejected a valid contract: %v\n%s", err, render(t, want))
			return false
		}
		if !reflect.DeepEqual(got, want) {
			t.Logf("round trip changed the contract:\n got %#v\nwant %#v", got, want)
			return false
		}
		return true
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 300}); err != nil {
		t.Fatal(err)
	}
}
