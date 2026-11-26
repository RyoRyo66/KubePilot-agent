import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { EventEmitter } from 'node:events';
import https from 'node:https';
import { writeFileSync } from 'node:fs';
import { Kubernetes, apiClient } from '../connector/kube.js';
import { Connector } from '../connector/main.js';
import { hashPlan } from '../server/common.js';
import { manifest } from '../server/manifest.js';
const t = {
  kind: 'Deployment',
  name: 'api',
  namespace: 'sample',
  uid: 'app1',
  version: '5',
  container: 'web',
};
const cfg = {
  namespace: 'sample',
  targets: [{ kind: 'Deployment', name: 'api', probeUrl: 'http://api.sample.svc/' }],
  image: 'test:3',
  automatic: true,
  actions: ['restore_image', 'restore_probe'],
};
const base = {
  uid: 'app1',
  id: 'baseline',
  containers: {
    web: {
      image: 'old:v1',
      pinned: 'example/test@sha256:' + 'a'.repeat(64),
      probes: { readinessProbe: { httpGet: { path: '/ready', port: 80 } } },
    },
  },
};
function fixture() {
  const obj = {
    metadata: { namespace: 'sample', name: 'api', uid: 'app1', resourceVersion: '5', generation: 1 },
    spec: {
      replicas: 1,
      selector: { matchLabels: { app: 'api' } },
      template: {
        spec: {
          containers: [
            { name: 'sidecar', image: 'keep:v1' },
            { name: 'web', image: 'bad:v2', readinessProbe: { httpGet: { path: '/bad', port: 80 } } },
          ],
        },
      },
    },
    status: {},
  };
  const writes = [];
  async function api(path, opts = {}) {
    if (opts.patch) {
      writes.push({ path, patch: structuredClone(opts.patch) });
      return obj;
    }
    if (path.includes('/replicasets') || path.includes('/pods')) return { items: [] };
    return obj;
  }
  return { k: new Kubernetes(cfg, api, async () => true), obj, writes };
}
test('Deployment 镜像与探针修复均先预检，只触碰绑定 UID、版本、容器的字段', async () => {
  for (const type of ['ImagePullBackOff', 'ProbeFailure']) {
    const { k, writes } = fixture(),
      p = await k.plan(t, type, base);
    assert.ok(p);
    await k.apply(p);
    assert.equal(writes.length, 2);
    assert.match(writes[0].path, /\?dryRun=All$/);
    assert.deepEqual(writes[0].patch, writes[1].patch);
    assert.deepEqual(
      writes[0].patch.slice(0, 3).map((x) => x.value),
      ['app1', '5', 'web'],
    );
    assert.deepEqual(
      writes[1].patch.slice(3).map((x) => x.path),
      ['/spec/template/spec/containers/1/' + (type === 'ProbeFailure' ? 'readinessProbe' : 'image')],
    );
  }
});
test('日志、基线和 UID 约束，初始容器异常优先于探针事件', async () => {
  const { k, obj, writes } = fixture();
  obj.metadata.resourceVersion = '6';
  await assert.rejects(k.plan(t, 'ImagePullBackOff', base));
  assert.equal(writes.length, 0);
  assert.equal(
    k.classify(
      {
        target: t,
        pods: [
          {
            metadata: { name: 'pod' },
            status: {
              initContainerStatuses: [
                { name: 'setup', state: { waiting: { reason: 'CrashLoopBackOff' } } },
              ],
            },
          },
        ],
      },
      [{ reason: 'Unhealthy' }],
    ).type,
    'CrashLoopBackOff',
  );
  const items = JSON.parse(manifest({ scope: cfg })).items;
  const role = items.find((r) => r.kind === 'Role');
  assert.ok(role.rules.every((r) => !r.verbs.includes('update')));
  assert.deepEqual(role.rules.find((r) => r.verbs.includes('patch')).resourceNames, ['api']);
});
test('执行端重启后仍拒绝同一事件的第二次写入', async (tctx) => {
  const dir = mkdtempSync(join(tmpdir(), 'kp-worker-'));
  tctx.after(() => rmSync(dir, { recursive: true, force: true }));
  const { k, writes } = fixture(),
    enroll = {
      scope: cfg,
      serverUrl: 'http://127.0.0.1',
      allowHttp: true,
      generation: 1,
      protocol: 3,
      token: 'A'.repeat(32),
    };
  let w = new Connector(enroll, dir, k);
  w.put('Deployment/api', base);
  const plan = await k.plan(t, 'ImagePullBackOff', base);
  const job = {
    action: 'execute',
    generation: 1,
    owner: w.id,
    lease: 'lease',
    id: 'one',
    caseId: 'case-1',
    type: 'ImagePullBackOff',
    expiresAt: new Date(Date.now() + 60000).toISOString(),
    automatic: true,
    plan,
    planHash: hashPlan(plan),
  };
  const s = new AbortController().signal;
  assert.equal((await w.handle(job, s)).ok, true);
  w.close();
  w = new Connector(enroll, dir, k);
  try {
    const second = await w.handle({ ...job, id: 'two' }, s);
    assert.equal(second.ok, false);
    assert.equal(writes.length, 2);
  } finally {
    w.close();
  }
});

test('事件采集优先故障 Pod 和活跃控制器', async () => {
  const { k } = fixture();
  const uids = [];
  k.list = async (path) => {
    uids.push(new URL(path, 'http://cluster').searchParams.get('fieldSelector'));
    return [];
  };
  const item = (uid) => ({ metadata: { uid } });
  await k.events({
    object: item('deployment'),
    target: t,
    pods: [item('unready')],
    rs: [
      ...Array.from({ length: 12 }, (_, i) => item('old' + i)),
      { ...item('active'), spec: { replicas: 1 } },
    ],
  });
  assert.deepEqual(
    uids,
    ['unready', 'active', 'deployment'].map((uid) => 'involvedObject.uid=' + uid),
  );
});
test('初始容器只恢复该容器的基线探针，可移除新加的异常探针', async () => {
  const { k, obj, writes } = fixture();
  obj.spec.template.spec.initContainers = [
    { name: 'setup', image: 'job', livenessProbe: { tcpSocket: { port: 80 } } },
  ];
  const known = {
    uid: t.uid,
    id: 'healthy',
    containers: { setup: { group: 'initContainers', probes: {} } },
  };
  const plan = await k.plan({ ...t, container: 'setup' }, 'ProbeFailure', known);
  await k.apply(plan);
  assert.deepEqual(writes[1].patch.at(-1), {
    op: 'remove',
    path: '/spec/template/spec/initContainers/0/livenessProbe',
  });
});
test('预检失败保留写入机会，基线只在连续两次健康采样后轮换', async (ctx) => {
  const dir = mkdtempSync(join(tmpdir(), 'kp-preflight-'));
  const { k } = fixture();
  const worker = new Connector(
    {
      scope: cfg,
      serverUrl: 'http://localhost',
      allowHttp: true,
      generation: 1,
      protocol: 3,
      token: 'B'.repeat(32),
    },
    dir,
    k,
  );
  ctx.after(() => {
    worker.close();
    rmSync(dir, { recursive: true, force: true });
  });
  let sample = base;
  k.baseline = async () => sample;
  const signal = new AbortController().signal;
  for (let i = 0; i < 2; i++) await worker.sample(t, signal);
  sample = { ...base, id: 'healthy-next' };
  await worker.sample(t, signal);
  assert.equal(worker.get('Deployment/api').id, base.id);
  await worker.sample(t, signal);
  assert.equal(worker.get('Deployment/api').id, sample.id);
  const plan = await k.plan(t, 'ImagePullBackOff', sample);
  const job = {
    action: 'execute',
    generation: 1,
    owner: worker.id,
    lease: 'lease',
    id: 'job',
    caseId: 'legacy-id',
    type: 'ImagePullBackOff',
    expiresAt: new Date(Date.now() + 60000).toISOString(),
    automatic: true,
    plan,
    planHash: hashPlan(plan),
  };
  const original = k.api;
  k.api = async (path, opts) => {
    if (path.endsWith('dryRun=All')) throw Error('preflight');
    return original(path, opts);
  };
  assert.equal((await worker.handle(job, signal)).preflightFailed, true);
  assert.equal(worker.db.prepare('SELECT count(*) n FROM attempts').get().n, 0);
  k.api = original;
  assert.equal((await worker.handle(job, signal)).ok, true);
  assert.equal(worker.db.prepare('SELECT count(*) n FROM attempts').get().n, 1);
});

test('终止先阻止预检后的新写入，已登记的 PATCH 使用独立的租约期限', async (ctx) => {
  const dir = mkdtempSync(join(tmpdir(), 'kp-drain-')),
    { k } = fixture();
  const worker = new Connector(
    {
      scope: cfg,
      serverUrl: 'http://localhost',
      allowHttp: true,
      generation: 1,
      protocol: 3,
      token: 'C'.repeat(32),
    },
    dir,
    k,
  );
  ctx.after(() => {
    worker.close();
    rmSync(dir, { recursive: true, force: true });
  });
  worker.put('Deployment/api', base);
  const plan = await k.plan(t, 'ImagePullBackOff', base);
  const job = {
    action: 'execute',
    generation: 1,
    owner: worker.id,
    lease: 'lease',
    id: 'job',
    caseId: 'case-a',
    type: 'ImagePullBackOff',
    expiresAt: new Date(Date.now() + 60000).toISOString(),
    automatic: true,
    plan,
    planHash: hashPlan(plan),
  };
  const api = k.api;
  let commits = 0,
    stop = new AbortController(),
    duringPreflight = true;
  k.api = async (path, opts) => {
    if (opts?.patch) {
      if (path.endsWith('dryRun=All')) {
        if (duringPreflight) stop.abort();
      } else {
        commits++;
        stop.abort();
        await new Promise((done) => setImmediate(done));
        opts.signal.throwIfAborted();
      }
    }
    return api(path, opts);
  };
  assert.equal((await worker.handle(job, stop.signal)).ok, false);
  assert.equal(commits, 0);
  assert.equal(worker.db.prepare('SELECT COUNT(*) n FROM attempts').get().n, 0);
  stop = new AbortController();
  duringPreflight = false;
  assert.equal((await worker.handle(job, stop.signal)).ok, true);
  assert.equal(commits, 1);
  assert.equal(worker.db.prepare('SELECT COUNT(*) n FROM attempts').get().n, 1);
});

test('服务账号 CA 和 token 轮换后下一次采集使用更新的凭据', async (ctx) => {
  const dir = mkdtempSync(join(tmpdir(), 'kp-ca-')),
    oldHost = process.env.KUBERNETES_SERVICE_HOST;
  process.env.KUBERNETES_SERVICE_HOST = '127.0.0.1';
  ctx.after(() => {
    if (oldHost === undefined) delete process.env.KUBERNETES_SERVICE_HOST;
    else process.env.KUBERNETES_SERVICE_HOST = oldHost;
    rmSync(dir, { recursive: true, force: true });
  });
  const sent = [];
  ctx.mock.method(https, 'request', (url, opts, accept) => {
    sent.push([String(opts.ca), opts.headers.Authorization]);
    const req = new EventEmitter();
    req.end = () => {
      const res = new EventEmitter();
      res.statusCode = 200;
      accept(res);
      res.emit('data', Buffer.from('{}'));
      res.emit('end');
    };
    return req;
  });
  const api = apiClient(dir);
  for (const rev of ['one', 'two']) {
    writeFileSync(join(dir, 'ca.crt'), 'ca-' + rev);
    writeFileSync(join(dir, 'token'), 'fixture-' + rev);
    await api('/api/v1');
  }
  assert.deepEqual(sent, [
    ['ca-one', 'Bearer fixture-one'],
    ['ca-two', 'Bearer fixture-two'],
  ]);
});
test('日志脱敏保留完整密钥块之后的诊断文本，未闭合块继续遮蔽', async () => {
  const { k } = fixture();
  const sample = '-----BEGIN PRIVATE KEY-----\nfixture-data\n-----END PRIVATE KEY-----\nready';
  k.api = async () => sample;
  const v = {
    target: t,
    pods: [{ metadata: { name: 'example' }, spec: { containers: [{ name: 'web' }] } }],
  };
  const [{ detail }] = await k.logs(v, 'web');
  assert.match(detail, /ready/);
  assert.ok(!detail.includes('fixture-data'));
  k.api = async () => sample.split('-----END')[0];
  assert.ok(!(await k.logs(v, 'web'))[0].detail.includes('fixture-data'));
});
