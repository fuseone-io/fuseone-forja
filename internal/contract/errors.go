package contract

import (
	"fmt"
	"strings"
)

// FieldError is one defect of a contract: the field's path in the document
// (for example gates[0].needs[1]), the line it sits on and what is wrong.
// Path is empty for a defect of the document as a whole.
type FieldError struct {
	Path    string
	Line    int
	Message string
}

func (e FieldError) Error() string {
	at := e.Path
	if at == "" {
		at = "document"
	}
	if e.Line == 0 {
		return at + ": " + e.Message
	}
	return fmt.Sprintf("%s (line %d): %s", at, e.Line, e.Message)
}

// Errors is every defect Parse found, in document order. It is the error
// Parse returns; callers reach it with errors.As.
type Errors []FieldError

func (es Errors) Error() string {
	parts := make([]string, 0, len(es))
	for _, e := range es {
		parts = append(parts, e.Error())
	}
	return strings.Join(parts, "; ")
}
