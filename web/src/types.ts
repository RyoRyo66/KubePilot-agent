export interface Passage { documentId: string; title: string; section?: string; version: string; source?: string; text: string }
export interface Target { kind: string; namespace: string; name: string; uid: string; version: string; container?: string }
export interface Evidence { id: string; source: string; summary: string; detail: string; at: string }
export interface Plan { action: string; summary: string; before: string; after: string; baselineId: string; target: Target; checks: string[] }
export interface Incident {
  id: string; key: string; type: string; title: string; severity: string; status: string;
  target: Target; firstSeen: string; lastSeen: string; resolvedAt?: string; occurrences: number;
  evidence: Evidence[]; diagnosis?: { knowledge?: Passage[]; engine: string; conclusion: string; evidenceIds: string[]; recommendations: string[]; limitations: string; at: string; disposition?: 'consider_plan' | 'need_evidence' };
  plan?: Plan; timeline: { at: string; status: string; title: string; detail: string }[]; executionCount: number;
}
export interface Inspection { id: string; trigger: string; startedAt: string; durationMs: number; findings: number; status: string; error?: string }
export interface Policy { action: string; name: string; description: string; enabled: boolean; automatic: boolean; risk: string }
export interface Audit { id: string; at: string; actor: string; action: string; target: string; result: string; detail: string }
export interface Experiment { id: string; at: string; fault: string; incidentId?: string; status: string; mode: string; detectionMs?: number; recoveryMs?: number }
export interface Runbook { type: string; title: string; conclusion: string; checks: string[]; action: string; source: string }
export interface State {
  revision: number;
  settings: { mode: string; namespace: string; writesEnabled: boolean; intervalSeconds: number; modelConfigured: boolean; model: string; agentModel?: string; storage: string; version: string };
  snapshot?: { at: string; cluster: string; nodes: number; readyNodes: number; pods: number; readyPods: number; services: number; warnings: string[]; resources: { name: string; namespace: string; kind: string; ready: number; desired: number; status: string }[]; findings: { type: string }[] };
  incidents: Incident[]; inspections: Inspection[]; policies: Policy[]; audits: Audit[]; experiments: Experiment[];
  agentRuns?: AgentRun[];
}
export interface AgentRequest { namespace: string; workload: string; incidentId?: string; goal: string; mode: 'observe' | 'approval' | 'automatic'; maxSteps?: number }
export interface AgentStep { id: string; tool: string; status: string; startedAt: string; finishedAt?: string; durationMs: number; arguments?: unknown; output?: unknown; error?: string }
export interface AgentRun {
  id: string; request: AgentRequest; status: string; trigger: string; engine: string; promptHash: string; incidentId?: string;
  startedAt: string; finishedAt?: string; summary: string; steps: AgentStep[]; diagnosis?: Incident['diagnosis'];
  plan?: Plan; planHash?: string; approvalDue?: string; approvedAt?: string; fallback: boolean;
  modelCalls: number; inputTokens: number; outputTokens: number;
}
export type ConsoleAction = 'inspect' | 'diagnose' | 'verify' | 'inject'
export interface ConsoleRequest { action: ConsoleAction; namespace: string; workload: string; incidentId?: string; fault?: string }
export interface ConsoleResult {
  action: ConsoleAction; outcome: 'success' | 'error' | 'unconfirmed'; message: string; at: string; elapsedMs: number;
  request: ConsoleRequest; incident?: Incident; inspection?: Inspection; snapshot?: State['snapshot']; incidents?: Incident[];
}
