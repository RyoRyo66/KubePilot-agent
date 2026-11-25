import type { Evidence, Incident, Plan, Passage } from './types'
export interface TargetSpec { kind: 'Deployment' | 'Service'; name: string; probeUrl: string }
export interface Scope { namespace: string; targets: TargetSpec[]; actions: string[]; automatic: boolean; image: string }
export interface Review { knowledge?: Passage[]; decision: 'approve' | 'manual' | 'reject'; reason: string; evidenceIds: string[]; planHash: string; at: string }
interface CheckDecision { conclusion:string; evidenceIds:string[]; checks?:string[]; disposition:'inspect'|'need_evidence'|'consider_plan'; knowledge?:Passage[] }
export interface PlatformCase {
 incident: Incident; review?: Review; generation: number; reviewExpires?: string;
 investigation?: {knowledge?: Passage[]; engine: string; assessment?: {conclusion: string; disposition: 'inspect'|'need_evidence'|'consider_plan'}; checks?: string[]; hypotheses: {cause: string; evidenceIds: string[]; check: string}[]; initialEvidence: Evidence[]; followUps?: CheckDecision[]};
}
export interface PlatformState {
 version: string; config: { publicUrl: string; allowHttp: boolean; baseUrl: string; model: string; apiKeySet: boolean; modelTested: boolean; scope: Scope; generation: number };
 connection: { id: string; lastSeen: string; namespace: string; provider: string; generation: number; baselines: Record<string,string> };
 online: boolean; baselineReady: number; ready: boolean; busy: boolean; lastScan: string; lastError: string;
 alerts: { id: string; name: string; namespace: string; summary: string; status: string; receivedAt: string; count: number }[];
 cases: PlatformCase[]; events: { id: string; at: string; action: string; detail: string }[];
 snapshot?: { at: string; pods: number; readyPods: number; services: number; warnings: string[]; resources: { name: string; kind: string; status: string }[] };
}
export type ReviewPlan = Plan
