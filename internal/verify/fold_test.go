package verify_test

import (
	"math/rand"
	"reflect"
	"strconv"
	"testing"
	"testing/quick"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
	"github.com/fuseone-io/fuseone-forja/internal/verify"
)

func step(name string, status domain.StepStatus, exit int) domain.GateResult {
	return domain.GateResult{Name: name, Status: status, ExitCode: exit}
}

var passedSetup = step("setup[0]", domain.StepPassed, 0)

// The reports one inversion's gates can leave, keyed by what they show.
var reports = map[string]domain.GateReport{
	"red at test": {
		Setup:      []domain.GateResult{passedSetup},
		Gates:      []domain.GateResult{step("build", domain.StepPassed, 0), step("test", domain.StepFailed, 2), step("race", domain.StepSkipped, domain.NoExitCode)},
		FailedStep: "test",
	},
	"red at build": {
		Setup:      []domain.GateResult{passedSetup},
		Gates:      []domain.GateResult{step("build", domain.StepFailed, 1), step("test", domain.StepSkipped, domain.NoExitCode)},
		FailedStep: "build",
	},
	"green": {
		Setup: []domain.GateResult{passedSetup},
		Gates: []domain.GateResult{step("build", domain.StepPassed, 0), step("test", domain.StepPassed, 0)},
	},
	"setup red": {
		Setup:      []domain.GateResult{step("setup[0]", domain.StepFailed, 1)},
		Gates:      []domain.GateResult{step("build", domain.StepSkipped, domain.NoExitCode)},
		FailedStep: "setup[0]",
	},
	"service not ready": {
		Setup:      []domain.GateResult{passedSetup},
		Gates:      []domain.GateResult{step("build", domain.StepPassed, 0), step("integration", domain.StepFailed, domain.NoExitCode)},
		FailedStep: "integration",
	},
	"gate timed out": {
		Setup:      []domain.GateResult{passedSetup},
		Gates:      []domain.GateResult{step("build", domain.StepTimedOut, domain.NoExitCode)},
		FailedStep: "build",
	},
	"gate named like a setup step": {
		Setup:      []domain.GateResult{step("setup[0]", domain.StepFailed, 1)},
		Gates:      []domain.GateResult{step("setup[0]", domain.StepSkipped, domain.NoExitCode)},
		FailedStep: "setup[0]",
	},
}

func TestJudge_eachReport_outcome(t *testing.T) {
	t.Parallel()
	want := map[string]domain.InversionOutcome{
		"red at test": domain.InversionBit, "red at build": domain.InversionBit, "green": domain.InversionEscaped,
		"setup red": "", "service not ready": "", "gate timed out": "", "gate named like a setup step": "",
	}
	for name, report := range reports {
		got, conclusive := verify.Judge(report)
		if got != want[name] || conclusive != (want[name] != "") {
			t.Errorf("%s: Judge = %q, %v; want %q", name, got, conclusive, want[name])
		}
	}
}

// ran is a list of inversions drawn from the reports, each judged as the
// control plane judges it.
type ran []domain.Inversion

var reportNames = []string{"red at test", "red at build", "green", "setup red", "service not ready", "gate timed out"}

func (ran) Generate(r *rand.Rand, size int) reflect.Value {
	out := make(ran, r.Intn(min(size, 5)+1))
	for i := range out {
		report := reports[reportNames[r.Intn(len(reportNames))]]
		outcome, _ := verify.Judge(report)
		out[i] = domain.Inversion{Branch: "forja-qa/" + strconv.Itoa(i+1), Admitted: true, Gates: report, Outcome: outcome}
	}
	return reflect.ValueOf(out)
}

// Bites if and only if at least one inversion ran and every one bit; does
// not bite, naming the first that escaped, when every one is conclusive
// and one escaped; no verdict at all as soon as one is inconclusive.
func TestFold_property_bitesIffEveryInversionBit(t *testing.T) {
	t.Parallel()
	property := func(invs ran) bool {
		verdict, escaped, ok := verify.Fold(invs)
		every, conclusive, firstEscaped := summary(invs)
		switch {
		case !conclusive:
			return !ok && verdict == "" && escaped == ""
		case every:
			return ok && verdict == domain.VerdictBites && escaped == ""
		default:
			return ok && verdict == domain.VerdictDoesNotBite && escaped == firstEscaped
		}
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 2000, Rand: rand.New(rand.NewSource(22))}); err != nil {
		t.Fatal(err)
	}
}

// summary says, of a non-empty list, whether every inversion bit and
// whether every one is conclusive, and names the first that escaped.
func summary(invs ran) (every, conclusive bool, firstEscaped string) {
	every, conclusive = len(invs) > 0, len(invs) > 0
	for _, inv := range invs {
		every = every && inv.Outcome == domain.InversionBit
		conclusive = conclusive && inv.Outcome != ""
		if inv.Outcome == domain.InversionEscaped && firstEscaped == "" {
			firstEscaped = inv.Branch
		}
	}
	return every, conclusive, firstEscaped
}

// A red setup or a service that never became ready never bites, whatever
// the other inversions did.
func TestFold_setupRedOrServiceNotReady_neverBites(t *testing.T) {
	t.Parallel()
	bit := domain.Inversion{Branch: "forja-qa/1", Outcome: domain.InversionBit, Gates: reports["red at test"]}
	for _, name := range []string{"setup red", "service not ready"} {
		outcome, _ := verify.Judge(reports[name])
		other := domain.Inversion{Branch: "forja-qa/2", Gates: reports[name], Outcome: outcome}
		if verdict, _, ok := verify.Fold([]domain.Inversion{bit, other}); ok || verdict == domain.VerdictBites {
			t.Errorf("%s: Fold = %q, %v; want inconclusive", name, verdict, ok)
		}
	}
}
