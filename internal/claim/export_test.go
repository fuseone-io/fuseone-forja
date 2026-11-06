package claim

import "time"

// Budgets is the production budget of each call in a mutation's path:
// the mutation's, then a follow-up's.
var Budgets = map[string]time.Duration{"mutation": mutationTimeout, "follow-up": followUpTimeout}

// WithTimeouts shortens the store's budgets so a test can spend one
// without waiting for the production figure.
func WithTimeouts(s *Store, mutation, followUp time.Duration) *Store {
	s.mutationTimeout, s.followUpTimeout = mutation, followUp
	return s
}
