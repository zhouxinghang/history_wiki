package audit

import (
	"context"
	"encoding/json"
)

type Outcome string

const (
	OutcomeSuccess Outcome = "success"
	OutcomeFailure Outcome = "failure"
)

type Entry struct {
	RequestID   string
	ActorUserID *string
	Action      string
	TargetType  string
	TargetID    *string
	SourceIP    string
	UserAgent   string
	Outcome     Outcome
	Details     map[string]any
}

func (entry Entry) DetailsJSON() ([]byte, error) {
	if entry.Details == nil {
		return []byte("{}"), nil
	}
	return json.Marshal(entry.Details)
}

type Recorder interface {
	AppendAudit(context.Context, Entry) error
}
