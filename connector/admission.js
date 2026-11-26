import https from 'node:https';
import { readFileSync } from 'node:fs';
import { pathToFileURL } from 'node:url';
import { resolve } from 'node:path';
import { hashPlan, probeFields } from '../server/common.js';

// Reconstruct the one permitted edit from oldObject. Comparing whole objects
// also protects fields introduced by future Kubernetes versions.
export function permittedUpdate(req, policy) {
  const { oldObject: old, object: next, namespace, name, kind, resource } = req;
  if (!old || !next || !name || namespace !== policy.namespace || req.operation !== 'UPDATE' ||
      req.subResource || req.userInfo?.username !== 'system:serviceaccount:' + namespace + ':kubepilot-connector') return false;
  const target = policy.targets[kind.kind + '/' + name];
  if (!target || !['Service','Deployment'].includes(kind.kind) ||
      resource.resource !== (kind.kind === 'Service' ? 'services' : 'deployments') ||
      (kind.group || '') !== (kind.kind === 'Service' ? '' : 'apps') ||
      old.metadata.uid !== target.uid || next.metadata.uid !== target.uid ||
      next.metadata.namespace !== namespace || next.metadata.name !== name) return false;
  const normalize = obj => {
    const copy = structuredClone(obj);
    delete copy.metadata.managedFields;
    delete copy.metadata.generation;
    return copy;
  };
  const desired = normalize(next), original = normalize(old);
  const equal = candidate => hashPlan(candidate) === hashPlan(desired);
  if (kind.kind === 'Service') {
    if (!policy.actions.includes('restore_selector') || !desired.spec.selector ||
        hashPlan(desired.spec.selector) !== target.selector) return false;
    original.spec.selector = desired.spec.selector;
    return equal(original);
  }
  for (const group of ['initContainers','containers']) {
    const before = original.spec.template.spec[group] || [], after = desired.spec.template.spec[group] || [];
    if (before.length !== after.length) return false;
    for (let i = 0; i < before.length; i++) {
      const a = after[i], b = before[i], allowed = target.containers[b.name];
      if (!allowed || a.name !== b.name || group !== allowed.group) continue;
      for (const action of policy.actions) {
        const value = structuredClone(b);
        if (action === 'restore_image' && hashPlan(a.image) === allowed.image) value.image = a.image;
        else if (action === 'restore_probe') {
          const probes = Object.fromEntries(probeFields.filter(k => Object.hasOwn(a,k)).map(k => [k,a[k]]));
          if (hashPlan(probes) !== allowed.probes) continue;
          for (const k of probeFields) { delete value[k]; if (Object.hasOwn(probes,k)) value[k] = probes[k]; }
        } else continue;
        before[i] = value;
        const ok = equal(original);
        before[i] = b;
        if (ok) return true;
      }
    }
  }
  return false;
}
if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  const server = https.createServer({ cert: readFileSync('/tls/tls.crt'), key: readFileSync('/tls/tls.key'),
    requestTimeout: 10000, headersTimeout: 5000 }, async (req,res) => {
    const path = req.url.split('?', 1)[0];
    if (path === '/ready' && req.method === 'GET') { res.writeHead(200).end(); return; }
    if (path !== '/validate' || req.method !== 'POST') { res.writeHead(404).end(); return; }
    try {
      const chunks = []; let size = 0;
      for await (const part of req) {
        size += part.length;
        if (size > 6 * 1024 * 1024) { res.writeHead(413).end(); return; }
        chunks.push(part);
      }
      const { request } = JSON.parse(Buffer.concat(chunks));
      const policy = JSON.parse(readFileSync('/approvals/policy.json','utf8'));
      const allowed = permittedUpdate(request, policy);
      res.writeHead(200, {'Content-Type':'application/json'}).end(JSON.stringify({
        kind:'AdmissionReview', apiVersion:'admission.k8s.io/v1',
        response:{uid:request.uid, allowed, ...(!allowed && {status:{message:'变更不符合已批准的资源、字段或恢复值'}})},
      }));
    } catch { res.writeHead(400).end(); }
  });
  server.listen(8443);
}
