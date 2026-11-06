package claim

import "time"

// WithTimeouts shortens the store's budgets so a test can spend one
// without waiting for the production figure.
func WithTimeouts(s *Store, mutation, followUp time.Duration) *Store {
	s.mutationTimeout, s.followUpTimeout = mutation, followUp
	return s
}
