package platform

import (
	"context"
	"kubeagent/internal/core"
	"time"
)

const Version = "0.2.0"

type TargetSpec struct {
	Kind     string `json:"kind"`
	Name     string `json:"name"`
	ProbeURL string `json:"probeUrl"`
}
type Scope struct {
	Namespace string       `json:"namespace"`
	Targets   []TargetSpec `json:"targets"`
	Actions   []string     `json:"actions"`
	Automatic bool         `json:"automatic"`
	Image     string       `json:"image"`
}
type Config struct {
	PublicURL      string `json:"publicUrl"`
	AllowHTTP      bool   `json:"allowHttp"`
	WebhookToken   string `json:"webhookToken"`
	ConnectorToken string `json:"connectorToken"`
	BaseURL        string `json:"baseUrl"`
	Model          string `json:"model"`
	APIKey         string `json:"apiKey"`
	ModelTested    bool   `json:"modelTested"`
	Scope          Scope  `json:"scope"`
	Generation     int    `json:"generation"`
}
type Connection struct {
	ID         string            `json:"id"`
	LastSeen   time.Time         `json:"lastSeen"`
	Namespace  string            `json:"namespace"`
	Provider   string            `json:"provider"`
	Generation int               `json:"generation"`
	Baselines  map[string]string `json:"baselines"`
	Error      string            `json:"error,omitempty"`
}
type Alert struct {
	ID          string    `json:"id"`
	Fingerprint string    `json:"fingerprint"`
	Name        string    `json:"name"`
	Namespace   string    `json:"namespace"`
	Summary     string    `json:"summary"`
	Status      string    `json:"status"`
	StartsAt    time.Time `json:"startsAt"`
	ReceivedAt  time.Time `json:"receivedAt"`
	Count       int       `json:"count"`
}
type Event struct {
	ID     string    `json:"id"`
	At     time.Time `json:"at"`
	Action string    `json:"action"`
	Detail string    `json:"detail"`
}
type Case struct {
	Incident      core.Incident       `json:"incident"`
	Investigation *core.Investigation `json:"investigation,omitempty"`
	Review        *core.Review        `json:"review,omitempty"`
	Generation    int                 `json:"generation"`
	ReviewExpires time.Time           `json:"reviewExpires,omitempty"`
}
type Job struct {
	Automatic  bool           `json:"automatic"`
	ID         string         `json:"id"`
	Action     string         `json:"action"`
	Status     string         `json:"status"`
	CreatedAt  time.Time      `json:"createdAt"`
	ExpiresAt  time.Time      `json:"expiresAt"`
	Generation int            `json:"generation"`
	Incident   *core.Incident `json:"incident,omitempty"`
	Plan       *core.Plan     `json:"plan,omitempty"`
	PlanHash   string         `json:"planHash,omitempty"`
	Lease      string         `json:"lease,omitempty"`
	Owner      string         `json:"owner,omitempty"`
	Result     *JobResult     `json:"result,omitempty"`
}
type JobResult struct {
	Check       *core.CheckResult `json:"check,omitempty"`
	ID          string            `json:"id"`
	Lease       string            `json:"lease"`
	ConnectorID string            `json:"connectorId"`
	Snapshot    *core.Snapshot    `json:"snapshot,omitempty"`
	Plan        *core.Plan        `json:"plan,omitempty"`
	OK          bool              `json:"ok"`
	Detail      string            `json:"detail"`
	Error       string            `json:"error,omitempty"`
}
type Poll struct {
	Connection Connection `json:"connection"`
}
type PollReply struct {
	Job *Job `json:"job,omitempty"`
}
type DiskState struct {
	Schema     int            `json:"schema"`
	Config     Config         `json:"config"`
	Connection Connection     `json:"connection"`
	Alerts     []Alert        `json:"alerts"`
	Cases      []Case         `json:"cases"`
	Jobs       []Job          `json:"jobs"`
	Events     []Event        `json:"events"`
	Snapshot   *core.Snapshot `json:"snapshot,omitempty"`
	LastScan   time.Time      `json:"lastScan"`
	LastError  string         `json:"lastError"`
}
type ModelClient interface {
	Probe(context.Context) error
	Investigate(context.Context, core.Incident) (core.Investigation, error)
	AssessChecks(context.Context, core.Incident, core.Investigation, []string) (core.CheckDecision, error)
	Review(context.Context, core.Incident, core.Plan) (core.Review, error)
}
type ModelFactory func(Config) (ModelClient, error)
type ConnectorConfig struct {
	ServerURL  string `json:"serverUrl"`
	Token      string `json:"token"`
	AllowHTTP  bool   `json:"allowHttp"`
	Generation int    `json:"generation"`
	Scope      Scope  `json:"scope"`
}
