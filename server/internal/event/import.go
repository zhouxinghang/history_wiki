package event

import "time"

const (
	MaxImportRecords = 10_000
	MaxImportBytes   = 20 << 20
)

// ImportRecord is one new unpublished historical event and its active draft.
// IDs are supplied by the import document so preflight and committed writes
// evaluate exactly the same identities.
type ImportRecord struct {
	Index int
	Event Event
}

type ImportIssue struct {
	Index  int    `json:"index"`
	Field  string `json:"field"`
	Code   string `json:"code"`
	Detail string `json:"detail"`
}

type ImportBatch struct {
	ID        string
	EventIDs  []string
	CreatedAt time.Time
}

type ImportValidationError struct {
	Issues []ImportIssue
}

func (err *ImportValidationError) Error() string {
	return "event import validation failed"
}
