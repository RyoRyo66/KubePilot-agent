package core

import "time"

type Evidence struct {
	ID      string    `json:"id"`
	Source  string    `json:"source"`
	Summary string    `json:"summary"`
	Detail  string    `json:"detail"`
	At      time.Time `json:"at"`
}
type Target struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	UID       string `json:"uid"`
	Version   string `json:"version"`
	Container string `json:"container,omitempty"`
}
type Finding struct {
	Key      string     `json:"key"`
	Type     string     `json:"type"`
	Title    string     `json:"title"`
	Severity string     `json:"severity"`
	Target   Target     `json:"target"`
	Evidence []Evidence `json:"evidence"`
}
type Resource struct {
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Ready     int32  `json:"ready"`
	Desired   int32  `json:"desired"`
	Status    string `json:"status"`
}
type Snapshot struct {
	At         time.Time  `json:"at"`
	Cluster    string     `json:"cluster"`
	Nodes      int        `json:"nodes"`
	ReadyNodes int        `json:"readyNodes"`
	Pods       int        `json:"pods"`
	ReadyPods  int        `json:"readyPods"`
	Services   int        `json:"services"`
	Resources  []Resource `json:"resources"`
	Findings   []Finding  `json:"findings"`
	Warnings   []string   `json:"warnings"`
}
type Plan struct {
	Action     string   `json:"action"`
	Summary    string   `json:"summary"`
	Before     string   `json:"before"`
	After      string   `json:"after"`
	BaselineID string   `json:"baselineId"`
	Target     Target   `json:"target"`
	Checks     []string `json:"checks"`
}
type Diagnosis struct {
	Knowledge       []Passage `json:"knowledge,omitempty"`
	Engine          string    `json:"engine"`
	Conclusion      string    `json:"conclusion"`
	EvidenceIDs     []string  `json:"evidenceIds"`
	Recommendations []string  `json:"recommendations"`
	Limitations     string    `json:"limitations"`
	At              time.Time `json:"at"`
	Disposition     string    `json:"disposition,omitempty"`
}

// An investigation records verifiable hypotheses, separate from the final diagnosis.
type Hypothesis struct {
	Cause       string   `json:"cause"`
	EvidenceIDs []string `json:"evidenceIds"`
	Check       string   `json:"check"`
}
type Investigation struct {
	Knowledge       []Passage       `json:"knowledge,omitempty"`
	Assessment      *Diagnosis      `json:"assessment,omitempty"`
	Checks          []string        `json:"checks,omitempty"`
	Hypotheses      []Hypothesis    `json:"hypotheses"`
	InitialEvidence []Evidence      `json:"initialEvidence"`
	Engine          string          `json:"engine"`
	FollowUps       []CheckDecision `json:"followUps,omitempty"`
}

// CheckDecision records what the model learned and which checks it requests next.
type CheckDecision struct {
	Diagnosis
	Checks []string `json:"checks,omitempty"`
}
type Step struct {
	At     time.Time `json:"at"`
	Status string    `json:"status"`
	Title  string    `json:"title"`
	Detail string    `json:"detail"`
}
type Incident struct {
	ID             string     `json:"id"`
	Key            string     `json:"key"`
	Type           string     `json:"type"`
	Title          string     `json:"title"`
	Severity       string     `json:"severity"`
	Status         string     `json:"status"`
	Target         Target     `json:"target"`
	FirstSeen      time.Time  `json:"firstSeen"`
	LastSeen       time.Time  `json:"lastSeen"`
	ResolvedAt     *time.Time `json:"resolvedAt,omitempty"`
	Occurrences    int        `json:"occurrences"`
	Evidence       []Evidence `json:"evidence"`
	Diagnosis      *Diagnosis `json:"diagnosis,omitempty"`
	Plan           *Plan      `json:"plan,omitempty"`
	Timeline       []Step     `json:"timeline"`
	ExecutionCount int        `json:"executionCount"`
}
type Inspection struct {
	ID         string    `json:"id"`
	Trigger    string    `json:"trigger"`
	StartedAt  time.Time `json:"startedAt"`
	DurationMS int64     `json:"durationMs"`
	Findings   int       `json:"findings"`
	Status     string    `json:"status"`
	Error      string    `json:"error,omitempty"`
}
type Policy struct {
	Action      string `json:"action"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Enabled     bool   `json:"enabled"`
	Automatic   bool   `json:"automatic"`
	Risk        string `json:"risk"`
}
type Audit struct {
	ID     string    `json:"id"`
	At     time.Time `json:"at"`
	Actor  string    `json:"actor"`
	Action string    `json:"action"`
	Target string    `json:"target"`
	Result string    `json:"result"`
	Detail string    `json:"detail"`
}
type Experiment struct {
	ID          string    `json:"id"`
	At          time.Time `json:"at"`
	Fault       string    `json:"fault"`
	IncidentID  string    `json:"incidentId,omitempty"`
	Status      string    `json:"status"`
	Mode        string    `json:"mode"`
	DetectionMS *int64    `json:"detectionMs,omitempty"`
	RecoveryMS  *int64    `json:"recoveryMs,omitempty"`
}
type Settings struct {
	Mode            string `json:"mode"`
	Namespace       string `json:"namespace"`
	WritesEnabled   bool   `json:"writesEnabled"`
	IntervalSeconds int    `json:"intervalSeconds"`
	ModelConfigured bool   `json:"modelConfigured"`
	Model           string `json:"model"`
	AgentModel      string `json:"agentModel"`
	Storage         string `json:"storage"`
	Version         string `json:"version"`
}
type State struct {
	Schema      int          `json:"schema"`
	Revision    int64        `json:"revision"`
	Settings    Settings     `json:"settings"`
	Snapshot    *Snapshot    `json:"snapshot,omitempty"`
	Incidents   []Incident   `json:"incidents"`
	Inspections []Inspection `json:"inspections"`
	Policies    []Policy     `json:"policies"`
	Audits      []Audit      `json:"audits"`
	Experiments []Experiment `json:"experiments"`
	AgentRuns   []AgentRun   `json:"agentRuns"`
}

func InitialState(settings Settings) State {
	return State{Schema: 1, Settings: settings, Incidents: []Incident{}, Inspections: []Inspection{}, Audits: []Audit{}, Experiments: []Experiment{}, Policies: []Policy{
		{Action: "restore_image", Name: "恢复镜像版本", Description: "仅恢复已记录的健康镜像；校验对象身份与版本。", Enabled: true, Risk: "中"},
		{Action: "restore_selector", Name: "恢复服务选择器", Description: "恢复可信基线中的 Service 选择器，并验证服务端点。", Enabled: true, Risk: "中"},
		{Action: "restore_probe", Name: "恢复健康探针", Description: "恢复基线探针配置，不关闭探针或放宽阈值。", Enabled: true, Risk: "中"},
	}}
}
