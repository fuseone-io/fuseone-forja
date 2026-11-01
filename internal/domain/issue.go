package domain

// Issue is the statement of a GitHub issue as the agent will read it. Title
// and Body may carry personal data: they travel in tool results, never in
// errors or logs.
type Issue struct {
	Number int
	Title  string
	Body   string
}
