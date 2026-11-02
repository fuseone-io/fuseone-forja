package verify

import "github.com/fuseone-io/fuseone-forja/internal/domain"

// Judge is what one inversion's gate report shows, from the steps' status
// and exit codes alone. The inversion bit when the first step that did not
// pass is a gate that ran and exited non-zero, and escaped when every step
// passed. A red setup step, after which no gate runs, a gate whose service
// never became ready and a gate the deadline stopped show neither:
// conclusive is then false.
func Judge(r domain.GateReport) (outcome domain.InversionOutcome, conclusive bool) {
	if r.OK() {
		return domain.InversionEscaped, true
	}
	for _, g := range r.Gates {
		if g.Name == r.FailedStep && g.Status != domain.StepPassed {
			return gateOutcome(g)
		}
	}
	return "", false
}

func gateOutcome(g domain.GateResult) (domain.InversionOutcome, bool) {
	if g.Status == domain.StepFailed && g.ExitCode != domain.NoExitCode {
		return domain.InversionBit, true
	}
	return "", false
}

// Fold is the verdict over the inversions that ran, in order, each by its
// Outcome: bites when every one bit, does not bite naming the branch of
// the first that escaped. ok is false, with no verdict, when none ran or
// any one is neither.
func Fold(ran []domain.Inversion) (verdict domain.Verdict, escaped string, ok bool) {
	if len(ran) == 0 {
		return "", "", false
	}
	for _, inv := range ran {
		switch inv.Outcome {
		case domain.InversionBit:
		case domain.InversionEscaped:
			if escaped == "" {
				escaped = inv.Branch
			}
		default:
			return "", "", false
		}
	}
	if escaped != "" {
		return domain.VerdictDoesNotBite, escaped, true
	}
	return domain.VerdictBites, "", true
}
