import { readFileSync } from 'node:fs';
import { ensure, hashPlan } from '../server/common.js';

export function loadApprovals(file, namespace, generation) {
  if (!file) return {};
  try {
    const p = JSON.parse(readFileSync(file, 'utf8'));
    ensure(p.namespace === namespace && p.generation === generation && p.targets, '基线审批范围不符');
    return p.targets;
  } catch (error) {
    if (error.code === 'ENOENT') return {};
    throw error;
  }
}
export function approved(record, baseline) {
  if (!baseline || !record) return false;
  const { id, ...fields } = baseline;
  return record.uid === baseline.uid && id === record.baselineId && hashPlan(fields) === id;
}
export function approvalFor(b, reads) {
  const containers = {};
  for (const [name, c] of Object.entries(b.containers || {}))
    containers[name] = { group: c.group, image: c.pinned ? hashPlan(c.pinned) : null, probes: hashPlan(c.probes) };
  return { uid: b.uid, baselineId: b.id, selector: b.selector ? hashPlan(b.selector) : null, containers, reads };
}
