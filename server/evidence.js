// Emit only fixed diagnostic vocabulary and bounded numeric fields. Free-form
// Kubernetes messages are local inputs to classification, never model inputs.
const signals = [
  ['OOMKilled', /OOMKilled|out of memory/i],
  ['CrashLoopBackOff', /CrashLoopBackOff/i],
  ['ImagePullBackOff', /ErrImagePull|ImagePullBackOff|failed to pull/i],
  ['ProbeFailure', /ProbeFailure|probe failed|Unhealthy/i],
  ['ServiceSelector', /ServiceSelector|缺少就绪端点/],
  ['WorkloadUnavailable', /WorkloadUnavailable|未达到期望就绪副本数/],
  ['Pending', /Pending|Unschedulable|FailedScheduling/i],
  ['FailedCreate', /FailedCreate/i],
  ['FailedMount', /FailedMount|failed to mount/i],
  ['InsufficientCPU', /InsufficientCPU|insufficient cpu/i],
  ['InsufficientMemory', /InsufficientMemory|insufficient memory/i],
  ['Timeout', /timeout|timed out|deadline exceeded/i],
  ['ConnectionRefused', /ConnectionRefused|connection refused/i],
  ['DNSFailure', /DNSFailure|no such host|name resolution/i],
  ['PermissionDenied', /PermissionDenied|permission denied|unauthorized|forbidden/i],
  ['TLSFailure', /TLSFailure|certificate expired|certificate signed by unknown authority/i],
  ['BackOff', /\bBackOff\b/i],
  ['NotReady', /\bNotReady\b/i],
  ['ready', /\bready\b/i],
];
const sources = new Set(['Resource Status', 'Kubernetes Event', 'Container Log', 'Business Probe',
  ...['workload', 'events', 'logs', 'endpoints'].map(check => '模拟集群 / ' + check)]);
const counters = ['replicas', 'readyReplicas', 'updatedReplicas', 'availableReplicas', 'readyEndpoints'];
export function diagnosticSignals(value) {
  const text = String(value ?? '').slice(-32768);
  return signals.filter(([, rule]) => rule.test(text)).map(([name]) => name).join(', ') || '无已识别信号';
}
export function statusCounters(status) {
  return Object.fromEntries(counters.filter(k => Number.isSafeInteger(status?.[k]) && status[k] >= 0)
    .map(k => [k, status[k]]));
}
export function publicEvidence(e) {
  const source = sources.has(e.source) ? e.source : 'Diagnostic';
  let detail = diagnosticSignals(e.detail);
  if (source === 'Business Probe') detail = ['true', 'false'].includes(e.detail) ? e.detail : '未知';
  if (source === 'Resource Status') {
    try {
      const status = statusCounters(JSON.parse(e.detail));
      if (Object.keys(status).length) detail = JSON.stringify(status);
    } catch { /* Earlier Connectors supplied a text observation here. */ }
  }
  return {
    id: e.id,
    source,
    summary: source === 'Business Probe' ? '业务检查' : diagnosticSignals(e.summary),
    detail,
    at: typeof e.at === 'string' && /^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d\.\d{3}Z$/.test(e.at) ? e.at : undefined,
  };
}
