package core

import "context"

type Provider interface {
	Collect(context.Context) (Snapshot, error)
	Plan(context.Context, Incident) (*Plan, error)
	Execute(context.Context, Plan) (string, error)
	Verify(context.Context, Incident) (bool, string, error)
}

type DemoControl interface {
	Inject(string) error
	Reset() error
}

type Diagnoser interface {
	Diagnose(context.Context, Incident) (Diagnosis, error)
}
