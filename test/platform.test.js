import test from 'node:test';
import assert from 'node:assert/strict';
import http from 'node:http';
import { mkdtempSync, rmSync, readFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { setTimeout as sleep } from 'node:timers/promises';
import { application } from '../server/main.js';
import { Model } from '../server/model.js';
import { Connector } from '../connector/main.js';
import { Demo } from '../server/demo.js';
import { permitted } from '../server/network.js';
import { hashPlan } from '../server/common.js';

const listen = (s) =>
  new Promise((ok) => s.listen(0, '127.0.0.1', () => ok('http://127.0.0.1:' + s.address().port)));
async function until(f) {
  for (let n = 0; n < 300; n++) {
    const result = await f();
    if (result) return result;
    await sleep(15);
  }
  assert.fail('状态未在测试期限内到达');
}
function modelDouble() {
  const calls = [];
  const opts = { decision: 'approve', badReference: false };
  const server = http.createServer(async (req, res) => {
    let raw = '';
    for await (const p of req) raw += p;
    const b = JSON.parse(raw);
    const name = b.tools[0].function.name;
    const input = JSON.parse(b.messages.at(-1).content);
    calls.push({ name, input });
    const refs = opts.badReference ? ['missing'] : input.evidence?.slice(0, 1).map((e) => e.id);
    const value =
      name === 'connection'
        ? { connected: true }
        : name === 'review'
          ? {
              reason: '现场证据与基线一致',
              decision: opts.decision,
              evidenceIds: refs,
              planHash: input.planHash,
            }
          : {
              conclusion: '资源配置偏离基线',
              evidenceIds: refs,
              recommendations: ['验证恢复'],
              limitations: '仅限本次采集',
              disposition: input.completedChecks?.length ? 'consider_plan' : 'inspect',
              checks: ['workload'],
            };
    res.setHeader('Content-Type', 'application/json');
    res.end(
      JSON.stringify({
        choices: [
          {
            finish_reason: 'tool_calls',
            message: { tool_calls: [{ function: { name, arguments: JSON.stringify(value) } }] },
          },
        ],
      }),
    );
  });
  return { server, calls, opts };
}
async function fixture(t) {
  const dir = mkdtempSync(join(tmpdir(), 'kp-platform-')),
    model = modelDouble(),
    base = await listen(model.server);
  const app = application({ dir: join(dir, 'platform'), trustedModel: base }),
    root = await listen(app.server);
  let cookie = '',
    worker,
    stop,
    loop,
    world;
  t.after(async () => {
    stop?.abort();
    await loop;
    worker?.close();
    await app.close();
    await new Promise((ok) => model.server.close(ok));
    rmSync(dir, { recursive: true, force: true });
  });
  async function call(path, data, opts = {}) {
    const res = await fetch(root + path, {
      method: data === undefined ? 'GET' : 'POST',
      headers: { cookie, 'Content-Type': 'application/json', 'X-Requested-With': 'KubePilot', ...opts },
      body: data === undefined ? undefined : JSON.stringify(data),
    });
    const out = await res.json();
    return { status: res.status, out, headers: res.headers };
  }
  async function good(path, data) {
    const r = await call(path, data);
    assert.equal(r.status, 200, JSON.stringify(r.out));
    return r.out;
  }
  const registered = await call('/api/auth/register', {
    username: 'tester',
    password: 'fixture-Password-17',
    confirmation: 'fixture-Password-17',
  });
  assert.equal(registered.status, 200);
  cookie = registered.headers.get('set-cookie').split(';')[0];
  const ws = (await good('/api/workspace/create', { name: '测试空间', description: '临时数据' }))
    .workspace.id;
  const { config } = await good('/api/platform/setup/source', { publicUrl: root, allowHttp: true });
  const token = JSON.parse(config.split('credentials: ')[1]);
  await good('/api/platform/setup/model', {
    baseUrl: base,
    model: 'fixture',
    apiKey: 'test-only-secret',
  });
  const scope = {
    namespace: 'kube-agent-lab',
    targets: [
      {
        kind: 'Service',
        name: 'payment-service',
        probeUrl: 'http://payment-service.kube-agent-lab.svc/',
      },
    ],
    image: 'kubepilot-connector:0.3.0@sha256:' + 'a'.repeat(64),
    automatic: true,
    actions: ['restore_selector'],
  };
  const manifest = JSON.parse((await good('/api/platform/setup/connector', scope)).manifest);
  const cfg = JSON.parse(manifest.items.find((x) => x.kind === 'Secret').stringData['config.json']);
  assert.equal((await call('/api/platform/setup/connector', { ...scope, image: 'demo:latest' })).status, 400);
  assert.deepEqual(JSON.parse((await good('/api/platform/setup/manifest', {})).manifest), manifest);
  const demo = new Demo(
    scope.namespace,
    () => structuredClone(world),
    (x) => {
      world = structuredClone(x);
    },
  );
  // The fixture exposes just the enrolled Service, as the real Connector does.
  const collect = demo.collect.bind(demo);
  demo.collect = async () => {
    const snap = await collect();
    snap.findings = snap.findings.filter((f) => f.target.kind === 'Service');
    return snap;
  };
  worker = new Connector(cfg, join(dir, 'connector'), demo, { kind: 'demo', verifyDelay: 1 });
  for (let i = 0; i < 2; i++) await worker.sample(scope.targets[0], new AbortController().signal);
  stop = new AbortController();
  loop = worker.run(stop.signal, 5);
  await until(async () => (await good('/api/platform/state')).ready);
  const alert = async (status = 'firing') => {
    const r = await call(
      '/ingest/alertmanager',
      {
        version: '4',
        alerts: [
          {
            status,
            labels: { alertname: 'ServiceDown', namespace: scope.namespace },
            annotations: { summary: '服务检查异常' },
            fingerprint: 'abc',
            startsAt: '2026-11-26T00:00:00Z',
          },
        ],
      },
      { Authorization: 'Bearer ' + token },
    );
    assert.equal(r.status, 200);
  };
  return { app, ws, call, good, demo, model, dir, alert, base, scope, worker };
}
test('接入、告警去重、补采、RAG、独立复核、一次受控修复、业务验证', async (t) => {
  const f = await fixture(t);
  const inspect = f.demo.inspect.bind(f.demo);
  const privateText = 'example-local-log-marker';
  f.demo.inspect = async (target, check) => [
    ...await inspect(target, check),
    { id: 'ev-log-' + check, source: 'Container Log', summary: 'local', detail: privateText + ' connection refused' },
  ];
  await f.good('/api/platform/knowledge', {
    title: 'ServiceSelector 实验',
    content: 'ServiceSelector 发生后核对选择器和就绪端点',
  });
  assert.ok(
    (await f.good('/api/platform/knowledge/search?q=ServiceSelector')).some((d) =>
      d.title.includes('实验'),
    ),
  );
  f.demo.inject('ServiceSelector');
  await f.alert();
  await f.alert();
  await f.good('/api/platform/scan', {});
  const state = await until(async () => {
    const s = await f.good('/api/platform/state');
    return s.cases[0]?.incident.status === 'resolved' && !s.busy && s;
  });
  assert.equal(state.alerts[0].count, 2);
  assert.equal(state.cases[0].incident.executionCount, 1);
  assert.ok(state.cases[0].investigation.followUps.length);
  assert.equal(state.cases[0].review.planHash, hashPlan(state.cases[0].incident.plan));
  assert.ok(
    f.model.calls.find((c) => c.name === 'review').input.knowledge.some((d) => d.title.includes('实验')),
  );
  assert.equal(
    (await f.call('/api/platform/cases/' + state.cases[0].incident.id + '/retry', {})).status,
    409,
  );
  assert.ok(!JSON.stringify(state).includes('test-only-secret'));
  assert.ok(!JSON.stringify(state).includes(privateText));
  for (const { input } of f.model.calls.filter(c => c.name === 'assess' || c.name === 'review')) {
    assert.ok(input.evidence.some(e => e.detail === 'ConnectionRefused'));
    assert.ok(!JSON.stringify(input).includes(privateText));
  }
});
test('模型切换端点不得复用旧凭据，跨空间写入和错误证据受阻', async (t) => {
  const f = await fixture(t);
  const n = f.model.calls.length;
  assert.equal(
    (
      await f.call('/api/platform/setup/model', {
        baseUrl: 'https://new-model.example/v1',
        model: 'fixture',
        apiKey: '',
      })
    ).status,
    400,
  );
  assert.equal(f.model.calls.length, n);
  assert.equal(
    (await f.call('/api/platform/pause', {}, { 'X-KubePilot-Workspace': 'other' })).status,
    409,
  );
  assert.equal(
    (await f.call('/api/platform/pause', {}, { Origin: 'https://other.example' })).status,
    403,
  );
  f.model.opts.badReference = true;
  f.demo.inject('ServiceSelector');
  await f.alert();
  await f.good('/api/platform/scan', {});
  const c = await until(async () => {
    const s = await f.good('/api/platform/state');
    return s.cases[0]?.incident.status === 'manual' && s.cases[0];
  });
  assert.equal(c.incident.executionCount, 0);
  assert.ok(!c.incident.plan);
  await f.alert('resolved');
  assert.equal((await f.good('/api/platform/state')).cases[0].incident.status, 'manual');
});
test('人工批准仅适用于已复核且未变化的版本', async (t) => {
  const f = await fixture(t);
  f.model.opts.decision = 'manual';
  f.demo.inject('ServiceSelector');
  await f.good('/api/platform/scan', {});
  const c = await until(async () => {
    const s = await f.good('/api/platform/state');
    return !s.busy && s.cases[0]?.incident.status === 'planned' && s.cases[0];
  });
  assert.equal(
    (await f.call('/api/platform/cases/' + c.incident.id + '/approve', { planHash: 'old' })).status,
    409,
  );
  f.demo.reset();
  f.demo.inject('ServiceSelector');
  await f.good('/api/platform/cases/' + c.incident.id + '/approve', { planHash: c.review.planHash });
  const stopped = await until(async () => {
    const s = await f.good('/api/platform/state');
    return s.cases[0]?.incident.status === 'manual' && s.cases[0];
  });
  assert.equal(stopped.incident.executionCount, 0);
});
test('网络策略区分公网模型、管理员回环模型和集群探测', () => {
  assert.equal(permitted('8.8.8.8', 'public'), true);
  assert.equal(permitted('10.43.0.2', 'public'), false);
  assert.equal(permitted('10.43.0.2', 'cluster'), false);
  assert.equal(permitted('10.43.0.2', 'cluster', ['10.43.0.2']), true);
  for (const ip of ['169.254.169.254', '100.100.100.200', '::ffff:127.0.0.1'])
    assert.equal(permitted(ip, 'cluster'), false);
  assert.equal(permitted('127.0.0.1', 'admin-local'), true);
  assert.equal(permitted('127.0.0.1', 'public'), false);
});

test('独立复核拒绝后允许验证外部恢复，同一故障复发创建新事件', async (t) => {
  const f = await fixture(t);
  f.model.opts.decision = 'reject';
  const scan = async (status) => {
    await f.good('/api/platform/scan', {});
    return until(async () => {
      const s = await f.good('/api/platform/state');
      return !s.busy && s.cases.at(-1)?.incident.status === status && s.cases;
    });
  };
  f.demo.inject('ServiceSelector');
  const [first] = await scan('rejected');
  assert.equal(first.incident.executionCount, 0);
  f.demo.reset();
  const [resolved] = await scan('resolved');
  assert.equal(resolved.incident.id, first.incident.id);
  f.demo.inject('ServiceSelector');
  const cases = await scan('rejected');
  assert.equal(cases.length, 2);
  assert.notEqual(cases[1].incident.id, first.incident.id);
});

test('模型限次退避后接受含单个工具的 stop，校验证据仅限实际发送的编号', async (ctx) => {
  const replies = [],
    requests = [];
  const server = http.createServer(async (req, res) => {
    let raw = '';
    for await (const part of req) raw += part;
    requests.push(JSON.parse(raw));
    const next = replies.shift();
    res
      .writeHead(next.status, { 'Content-Type': 'application/json', 'Retry-After': '0' })
      .end(JSON.stringify(next.body));
  });
  const base = await listen(server);
  ctx.after(() => new Promise((resolve) => server.close(resolve)));
  const model = new Model({ model: 'fixture', baseUrl: base }, base);
  const called = (name, value) => ({
    status: 200,
    body: {
      choices: [
        {
          finish_reason: 'stop',
          message: { tool_calls: [{ function: { name, arguments: JSON.stringify(value) } }] },
        },
      ],
    },
  });
  replies.push({ status: 429, body: {} }, called('connection', { connected: true }));
  assert.equal((await model.call('connection', {})).connected, true);
  assert.equal(requests.length, 2);
  assert.equal(requests[0].parallel_tool_calls, false);
  replies.push(
    called('assess', {
      conclusion: 'inspect',
      evidenceIds: ['ev-32'],
      recommendations: [],
      limitations: '',
      disposition: 'need_evidence',
      checks: [],
    }),
  );
  const evidence = Array.from({ length: 33 }, (_, i) => ({
    id: 'ev-' + i,
    source: 'Container Log',
    summary: 'legacy-user-text',
    detail: 'x'.repeat(3000) + ' connection refused',
  }));
  await assert.rejects(model.call('assess', { evidence }), /证据/);
  const sent = JSON.parse(requests.at(-1).messages[1].content).evidence;
  assert.equal(sent.length, 32);
  assert.equal(sent[0].detail, 'ConnectionRefused');
  assert.ok(!JSON.stringify(sent).includes('legacy-user-text'));
});
