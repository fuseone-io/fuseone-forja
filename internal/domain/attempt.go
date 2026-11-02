package domain

// DefaultMaxAttempts is how many attempts a run gets when its contract
// sets no limits.max_attempts.
const DefaultMaxAttempts = 3

// Attempts is the contract's limits.max_attempts, or DefaultMaxAttempts
// when it is omitted.
func (l Limits) Attempts() int {
	if l.MaxAttempts == 0 {
		return DefaultMaxAttempts
	}
	return l.MaxAttempts
}

// Attempt is one pass of the agent followed by the gates on its head.
// Number counts from 1; Tokens is what that pass of the agent used.
type Attempt struct {
	Number  int
	HeadSHA string
	Gates   GateReport
	Tokens  TokenUsage
}
