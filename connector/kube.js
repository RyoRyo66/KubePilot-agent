import { join } from 'node:path';
import { isIP } from 'node:net';
import https from 'node:https';
import { readFileSync } from 'node:fs';
import { actions, ensure, hashPlan, inScope, now, probeServiceName, Problem } from '../server/common.js';
import { request } from '../server/network.js';
const tokenDir = '/var/run/secrets/kubernetes.io/serviceaccount/';
const enc = encodeURIComponent;
export function apiClient(accountDir = tokenDir) {
  const raw = process.env.KUBERNETES_SERVICE_HOST;
  ensure(raw, '需要集群内 ServiceAccount');
  const host = raw.includes(':') ? '[' + raw + ']' : raw;
  const root = 'https://' + host + ':' + (process.env.KUBERNETES_SERVICE_PORT_HTTPS || 443);
  return (path, { patch, signal, text = false } = {}) =>
    new Promise((ok, no) => {
      const data = patch ? JSON.stringify(patch) : undefined;
      const req = https.request(
        new URL(path, root),
        {
          method: patch ? 'PATCH' : 'GET',
          ca: readFileSync(join(accountDir, 'ca.crt')),
          signal: signal
            ? AbortSignal.any([signal, AbortSignal.timeout(10000)])
            : AbortSignal.timeout(10000),
          headers: {
            Authorization: 'Bearer ' + readFileSync(join(accountDir, 'token'), 'utf8').trim(),
            ...(data ? { 'Content-Type': 'application/json-patch+json' } : {}),
          },
        },
        (res) => {
          const chunks = [];
          let n = 0;
          res.on('data', (p) => {
            n += p.length;
            if (n > 4 * 1024 * 1024) {
              if (text) {
                chunks.push(p);
                const tail = Buffer.concat(chunks).subarray(-16384);
                chunks.splice(0, chunks.length, Buffer.from(tail));
                n = tail.length;
                return;
              }
              res.destroy(new Error('响应超过上限'));
              return;
            }
            chunks.push(p);
          });
          res.on('error', no);
          res.on('end', () => {
            try {
              ensure(
                res.statusCode >= 200 && res.statusCode < 300,
                'Kubernetes API 返回 ' + res.statusCode,
              );
              const value = Buffer.concat(chunks).toString('utf8');
              ok(text ? value.slice(-16384) : JSON.parse(value));
            } catch (e) {
              no(e);
            }
          });
        },
      );
      req.on('error', no);
      req.end(data);
    });
}
const probes = ['startupProbe', 'readinessProbe', 'livenessProbe'];
const owner = (object, uid) =>
  object.metadata.ownerReferences?.some((x) => x.uid === uid && x.controller !== false);
const readyPod = (p) =>
  p.status?.conditions?.some((c) => c.type === 'Ready' && c.status === 'True') &&
  !p.metadata.deletionTimestamp;
const evidence = (source, summary, detail) => ({
  id: hashPlan([source, summary, String(detail)]).slice(0, 16),
  source,
  summary,
  detail: String(detail).slice(0, 5000),
  at: now(),
});
function safeLog(v) {
  return v
    .replace(/(Bearer\s+)[\w+/=._-]+/gi, '$1[已隐藏]')
    .replace(/((?:password|api[_-]?key|token|secret)\s*["']?\s*[:=]\s*["']?)[^"'\s,}]+/gi, '$1[已隐藏]')
    .replace(/-----BEGIN ([A-Z0-9 ]*PRIVATE KEY)-----[\s\S]*?(?:-----END \1-----|$)/g, '[已隐藏]');
}
const problemByAction = Object.fromEntries(Object.entries(actions).map(([a, f]) => [f, a]));
export class Kubernetes {
  constructor(cfg, api = apiClient(), probe, extraOrigins = []) {
    this.cfg = cfg;
    this.api = api;
    this.probe = probe || ((t, s) => this.business(t, s));
    this.extraOrigins = new Set(extraOrigins);
  }
  async business(t, signal) {
    const u = new URL(t.probeUrl),
      dns = u.hostname.toLowerCase(),
      ns = this.cfg.namespace;
    const svc = probeServiceName(u, ns);
    ensure(
      svc || this.extraOrigins.has(u.origin),
      '业务探测地址需要使用授权命名空间的 Service DNS 或管理员登记的地址',
    );
    let allowedIPs;
    if (svc) {
      const service = await this.api(this.path({ kind: 'Service', name: svc }), { signal });
      allowedIPs = service.spec?.clusterIPs || [service.spec?.clusterIP];
      ensure(
        service.spec?.type !== 'ExternalName' && allowedIPs.every((a) => a && a !== 'None'),
        '业务探测需要有 ClusterIP 的 Service',
      );
    }
    if (!svc && isIP(dns.replace(/^\[|\]$/g, ''))) allowedIPs = [dns.replace(/^\[|\]$/g, '')];
    const r = await request(u, {
      policy: 'cluster',
      allowedIPs,
      timeout: 4000,
      limit: 128 * 1024,
      signal,
    });
    return r.status >= 200 && r.status < 300;
  }
  path(t) {
    return (
      (t.kind === 'Deployment' ? '/apis/apps/v1' : '/api/v1') +
      '/namespaces/' +
      enc(this.cfg.namespace) +
      '/' +
      (t.kind === 'Deployment' ? 'deployments' : 'services') +
      '/' +
      enc(t.name)
    );
  }
  async list(path, signal) {
    const items = [];
    let more = '';
    do {
      const url =
        path + (path.includes('?') ? '&' : '?') + 'limit=200' + (more ? '&continue=' + enc(more) : '');
      const result = await this.api(url, { signal });
      items.push(...result.items);
      ensure(items.length <= 6000, '采集数量超过单次预算');
      more = result.metadata?.continue || '';
    } while (more);
    return items;
  }
  async read(t, signal) {
    ensure(inScope(this.cfg, { ...t, namespace: this.cfg.namespace }), '未授权目标');
    const object = await this.api(this.path(t), { signal }),
      m = object.metadata;
    ensure(
      m.uid && m.resourceVersion && m.name === t.name && m.namespace === this.cfg.namespace,
      '对象身份不符',
    );
    const target = {
      kind: t.kind,
      namespace: m.namespace,
      name: m.name,
      uid: m.uid,
      version: m.resourceVersion,
    };
    const ns = '/namespaces/' + enc(m.namespace) + '/';
    if (t.kind === 'Service') {
      const slices = await this.list(
        '/apis/discovery.k8s.io/v1' +
          ns +
          'endpointslices?labelSelector=' +
          enc('kubernetes.io/service-name=' + m.name),
        signal,
      );
      const endpoints = slices
        .filter((s) => s.metadata.ownerReferences?.some((o) => o.uid === m.uid))
        .flatMap((s) => s.endpoints || [])
        .filter(
          (e) => e.conditions?.ready !== false && !e.conditions?.terminating && e.addresses?.length,
        );
      return { object, target, pods: [], endpoints, ready: endpoints.length > 0 };
    }
    const selector = object.spec.selector;
    const parts = Object.entries(selector?.matchLabels || {}).map(([k, val]) => k + '=' + val);
    for (const { key, operator, values } of selector?.matchExpressions || []) {
      if (operator === 'Exists') parts.push(key);
      else if (operator === 'DoesNotExist') parts.push('!' + key);
      else {
        ensure(['In', 'NotIn'].includes(operator) && values?.length, '不支持的标签选择条件');
        parts.push(key + (operator === 'In' ? ' in (' : ' notin (') + values.join(',') + ')');
      }
    }
    ensure(parts.length, 'Deployment 缺少可限定的选择条件');
    const query = '?labelSelector=' + enc(parts.join(','));
    const [rs, pods] = await Promise.all([
      this.list('/apis/apps/v1' + ns + 'replicasets' + query, signal),
      this.list('/api/v1' + ns + 'pods' + query, signal),
    ]);
    const owners = new Set(rs.filter((r) => owner(r, m.uid)).map((r) => r.metadata.uid));
    const owned = pods.filter(
      (p) => p.metadata.ownerReferences?.some((o) => owners.has(o.uid)) && !p.metadata.deletionTimestamp,
    );
    const count = object.spec.replicas ?? 1,
      st = object.status || {};
    const ready =
      count > 0 &&
      st.observedGeneration === m.generation &&
      st.updatedReplicas === count &&
      st.availableReplicas === count &&
      st.readyReplicas === count &&
      owned.length === count &&
      owned.every(readyPod);
    return { object, target, pods: owned, endpoints: [], ready, rs: rs.filter((r) => owner(r, m.uid)) };
  }
  async events(v, signal) {
    const active = (v.rs || []).filter((r) => (r.spec?.replicas || r.status?.replicas || 0) > 0);
    const pods = v.pods.filter((p) => !readyPod(p));
    const refs = [
      ...pods.slice(0, 8),
      ...active.slice(0, 1),
      v.object,
      ...pods.slice(8),
      ...active.slice(1),
    ].slice(0, 10);
    const values = await Promise.all(
      refs.map((o) =>
        this.list(
          '/api/v1/namespaces/' +
            enc(v.target.namespace) +
            '/events?fieldSelector=' +
            enc('involvedObject.uid=' + o.metadata.uid),
          signal,
        ),
      ),
    );
    return values
      .flat()
      .filter((e) => e.type === 'Warning')
      .sort((a, b) => eventTime(b) - eventTime(a))
      .slice(0, 12);
  }
  async logs(v, container, signal) {
    const p = [...v.pods]
      .sort((a, b) => Number(!!readyPod(a)) - Number(!!readyPod(b)))
      .find((p) =>
        [...(p.spec.initContainers || []), ...p.spec.containers].some(
          (c) => !container || c.name === container,
        ),
      );
    if (!p) return [];
    const name = container || p.spec.containers[0].name;
    const path =
      '/api/v1/namespaces/' +
      enc(v.target.namespace) +
      '/pods/' +
      enc(p.metadata.name) +
      '/log?container=' +
      enc(name) +
      '&tailLines=40';
    for (const previous of [true, false])
      try {
        const value = await this.api(path + '&previous=' + previous, { signal, text: true });
        if (value)
          return [
            evidence('Container Log', p.metadata.name + ' / ' + name, safeLog(value).slice(-4096)),
          ];
      } catch (e) {
        signal?.throwIfAborted();
        if (!previous) throw e;
      }
    return [];
  }
  async inspect(t, check, signal) {
    const v = await this.read(t, signal);
    ensure(v.target.uid === t.uid, '对象已重建');
    if (check === 'logs') return this.logs(v, t.container, signal);
    if (check === 'events')
      return (await this.events(v, signal)).map((e) =>
        evidence('Kubernetes Event', e.reason, safeLog(e.message || '')),
      );
    if (check === 'endpoints')
      return [evidence('Business Probe', '业务检查', String(await this.probe(this.spec(t), signal)))];
    ensure(check === 'workload', '未知的只读检查');
    return [
      evidence(
        'Resource Status',
        t.name,
        JSON.stringify(v.object.status || { readyEndpoints: v.endpoints.length }),
      ),
    ];
  }
  spec(t) {
    const spec = this.cfg.targets.find((x) => x.kind === t.kind && x.name === t.name);
    ensure(spec, '目标不在本地授权范围');
    return spec;
  }
  async collect(signal) {
    const snapshot = {
      at: now(),
      cluster: 'kubernetes',
      pods: 0,
      readyPods: 0,
      services: 0,
      nodes: 0,
      readyNodes: 0,
      warnings: [],
      resources: [],
      findings: [],
    };
    for (const t of this.cfg.targets) {
      const v = await this.read(t, signal);
      snapshot.pods += v.pods.length;
      snapshot.readyPods += v.pods.filter(readyPod).length;
      if (t.kind === 'Service') snapshot.services++;
      snapshot.resources.push({
        ...v.target,
        status: v.ready ? 'Ready' : 'NotReady',
        ready: t.kind === 'Service' ? v.endpoints.length : v.pods.filter(readyPod).length,
        desired: v.object.spec.replicas ?? 1,
      });
      if (v.ready) continue;
      const warnings = await this.events(v, signal);
      const result = this.classify(v, warnings);
      const ev = [
        evidence('Resource Status', result.type, result.detail),
        ...warnings
          .slice(0, 5)
          .map((e) => evidence('Kubernetes Event', e.reason, safeLog(e.message || ''))),
      ];
      if (result.container && !['ImagePullBackOff', 'Pending'].includes(result.type))
        try {
          ev.push(...(await this.logs(v, result.container, signal)));
        } catch {
          snapshot.warnings.push(t.name + ' 日志不可用');
        }
      snapshot.findings.push({
        type: result.type,
        title: result.type + ' · ' + t.name,
        severity: 'warning',
        target: { ...v.target, ...(result.container ? { container: result.container } : {}) },
        evidence: ev,
      });
    }
    return snapshot;
  }
  classify(v, events) {
    if (v.target.kind === 'Service')
      return { type: 'ServiceSelector', detail: '授权 Service 缺少就绪端点，需核对选择器和后端 Pod。' };
    for (const p of v.pods) {
      for (const c of [
        ...(p.status?.initContainerStatuses || []),
        ...(p.status?.containerStatuses || []),
      ]) {
        if (c.ready) continue;
        const reason = c.state?.waiting?.reason,
          exit = c.lastState?.terminated?.reason || c.state?.terminated?.reason;
        const type =
          exit === 'OOMKilled'
            ? 'OOMKilled'
            : ['ErrImagePull', 'ImagePullBackOff'].includes(reason)
              ? 'ImagePullBackOff'
              : reason === 'CrashLoopBackOff'
                ? reason
                : null;
        if (type)
          return {
            type,
            container: c.name,
            detail:
              p.metadata.name +
              ' / ' +
              c.name +
              ' ' +
              (reason || exit) +
              ' ' +
              safeLog(c.state?.waiting?.message || ''),
          };
      }
      const blocked = p.status?.conditions?.find(
        (c) => c.type === 'PodScheduled' && c.status === 'False',
      );
      if (blocked) return { type: 'Pending', detail: blocked.message || blocked.reason };
    }
    const event = events.find(
      (e) =>
        e.reason === 'Unhealthy' &&
        Date.now() - eventTime(e) < 600000 &&
        v.pods.some((p) => p.metadata.uid === e.involvedObject?.uid && !readyPod(p)),
    );
    if (event)
      return {
        type: 'ProbeFailure',
        container: /spec\.(?:containers|initContainers)\{([^}]+)\}/.exec(
          event.involvedObject.fieldPath || '',
        )?.[1],
        detail: safeLog(event.message),
      };
    return {
      type: events.some((e) => e.reason === 'FailedCreate') ? 'FailedCreate' : 'WorkloadUnavailable',
      detail: 'Deployment 未达到期望就绪副本数。',
    };
  }
  async baseline(t, signal) {
    const v = await this.read(t, signal);
    if (!v.ready || !(await this.probe(t, signal))) return null;
    const data = { uid: v.target.uid, kind: t.kind };
    if (t.kind === 'Service') {
      if (!Object.keys(v.object.spec.selector || {}).length) return null;
      data.selector = v.object.spec.selector;
    } else {
      data.containers = {};
      const podSpec = v.object.spec.template.spec;
      for (const c of [...podSpec.containers, ...(podSpec.initContainers || [])]) {
        const init = (podSpec.initContainers || []).some((item) => item.name === c.name);
        const imageIDs = v.pods.map((p) =>
          (init ? p.status.initContainerStatuses : p.status.containerStatuses)
            ?.find((s) => s.name === c.name)
            ?.imageID?.replace(/^[\w-]+:\/\//, ''),
        );
        const digest = imageIDs[0];
        const match =
          typeof digest === 'string' &&
          /@sha256:[0-9a-f]{64}$/i.test(digest) &&
          imageIDs.every((x) => x === digest);
        data.containers[c.name] = {
          group: init ? 'initContainers' : 'containers',
          image: c.image,
          pinned: match ? digest : /@sha256:[0-9a-f]{64}$/i.test(c.image) ? c.image : null,
          probes: Object.fromEntries(probes.filter((p) => c[p]).map((p) => [p, c[p]])),
        };
      }
    }
    return { ...data, id: hashPlan(data) };
  }
  async plan(t, type, b, signal) {
    const action = problemByAction[type];
    if (!b || !this.cfg.actions.includes(action)) return null;
    const v = await this.read(t, signal);
    ensure(
      t.namespace === this.cfg.namespace &&
        t.uid === v.target.uid &&
        t.version === v.target.version &&
        b.uid === t.uid,
      '资源版本或身份已改变',
    );
    let before, after;
    if (action === 'restore_selector' && t.kind === 'Service') {
      before = v.object.spec.selector || {};
      after = b.selector;
    } else if (t.kind === 'Deployment') {
      const known = b.containers?.[t.container];
      const group = known?.group || 'containers';
      const container = v.object.spec.template.spec[group]?.find((c) => c.name === t.container);
      if (!container || !known) return null;
      if (action === 'restore_image') {
        if (container.image === known.image || !known.pinned) return null;
        before = container.image;
        after = known.pinned;
      } else if (action === 'restore_probe') {
        before = Object.fromEntries(probes.filter((k) => container[k]).map((k) => [k, container[k]]));
        after = known.probes;
        if (!after || typeof after !== 'object') return null;
      } else return null;
    } else return null;
    if (!after || hashPlan(before) === hashPlan(after)) return null;
    return {
      action,
      summary: '恢复已核验的健康配置',
      before: JSON.stringify(before),
      after: JSON.stringify(after),
      baselineId: b.id,
      target: t,
      checks: ['资源 UID 和版本一致', '服务端预检', '工作负载和业务检查'],
    };
  }
  async apply(plan, signal, beforeCommit = () => {}, commitSignal = signal) {
    const t = plan.target,
      obj = await this.api(this.path(t), { signal });
    ensure(obj.metadata.uid === t.uid && obj.metadata.resourceVersion === t.version, '资源已改变');
    const patch = [
      { op: 'test', path: '/metadata/uid', value: t.uid },
      { op: 'test', path: '/metadata/resourceVersion', value: t.version },
    ];
    const value = JSON.parse(plan.after);
    if (plan.action === 'restore_selector') patch.push({ op: 'add', path: '/spec/selector', value });
    else {
      const spec = obj.spec.template.spec;
      const group = spec.containers.some((c) => c.name === t.container)
        ? 'containers'
        : 'initContainers';
      const index = (spec[group] || []).findIndex((c) => c.name === t.container);
      ensure(index >= 0, '目标容器不存在');
      const path = '/spec/template/spec/' + group + '/' + index + '/';
      patch.push({ op: 'test', path: path + 'name', value: t.container });
      if (plan.action === 'restore_image') patch.push({ op: 'replace', path: path + 'image', value });
      else {
        ensure(plan.action === 'restore_probe', '未知动作');
        for (const k of probes) {
          if (value[k]) patch.push({ op: 'add', path: path + k, value: value[k] });
          else if (spec[group][index][k]) patch.push({ op: 'remove', path: path + k });
        }
      }
    }
    try {
      await this.api(this.path(t) + '?dryRun=All', { patch, signal });
    } catch {
      throw new Problem('Kubernetes 预检失败，未提交修改', 409, 'preflight_failed');
    }
    signal?.throwIfAborted();
    beforeCommit();
    await this.api(this.path(t), { patch, signal: commitSignal });
  }
  async verify(t, signal) {
    const v = await this.read(t, signal);
    return v.target.uid === t.uid && v.ready && (await this.probe(this.spec(t), signal));
  }
}
function eventTime(e) {
  return (
    Date.parse(
      e.series?.lastObservedTime || e.lastTimestamp || e.eventTime || e.metadata.creationTimestamp,
    ) || 0
  );
}
