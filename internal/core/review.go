package core

import "time"

// Review is an advisory result bound to one immutable repair plan.
// It cannot grant permissions or replace executor-side validation.
type Review struct {
	Knowledge   []Passage `json:"knowledge,omitempty"`
	Decision    string    `json:"decision"`
	Reason      string    `json:"reason"`
	EvidenceIDs []string  `json:"evidenceIds"`
	PlanHash    string    `json:"planHash"`
	At          time.Time `json:"at"`
}
