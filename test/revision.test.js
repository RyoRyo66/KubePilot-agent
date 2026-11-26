import test from 'node:test';
import assert from 'node:assert/strict';
import http from 'node:http';
import { mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { Accounts } from '../server/auth.js';
import { connectorImage } from '../server/common.js';
import { publicEvidence } from '../server/evidence.js';
import { Dispatch } from '../server/dispatch.js';
import { Pipeline } from '../server/pipeline.js';
import { Store } from '../server/database.js';
import { hashPlan, probeServiceName } from '../server/common.js';
import { manifest, webhook } from '../server/manifest.js';
import { monitoring } from '../ops/monitoring.js';
import { Model, modelKey } from '../server/model.js';
import { application } from '../server/main.js';
import { laboratory } from '../server/lab.js';
import { Connector, listenHealth } from '../connector/main.js';

const target = {
  kind: 'Service',
  name: 'orders',
  namespace: 'sample',
  uid: 'service-uid',
  version: '2',
};
const scope = {
  namespace: 'sample',
  automatic: true,
  actions: ['restore_selector'],
  image: 'fixture:local@sha256:' + 'a'.repeat(64),
  targets: [{ kind: 'Service', name: 'orders', probeUrl: 'http://orders.sample.svc/' }],
};
const cfg = { generation: 1, scope };
const hello = {
  protocol: 3,
  connection: { id: 'worker', generation: 1, namespace: 'sample', provider: 'demo' },
};
function tempDir() {
  return mkdtempSync(join(tmpdir(), 'kp-revision-'));
}
async function listen(server) {
  await new Promise((resolve, reject) => server.once('error', reject).listen(0, '127.0.0.1', resolve));
  return 'http://127.0.0.1:' + server.address().port;
}
test('清单和运行时只识别本命名空间业务 Service，探针端口来自同一配置', () => {
  const hosts = [
    'Orders.Sample.Svc',
    'orders.sample.svc.cluster.local.',
    'orders.other.svc',
    'docs.example.org',
    'kubepilot-maintenance.sample.svc',
    'reports.sample.svc.other.example',
  ];
  assert.deepEqual(
    hosts.map((host) => probeServiceName('http://' + host, 'sample')),
    ['orders', 'orders', null, null, null, null],
  );
  assert.equal(probeServiceName('ftp://orders.sample.svc', 'sample'), null);
  const targets = hosts.map((host, i) => ({
    kind: 'Deployment',
    name: 'app-' + i,
    probeUrl: 'http://' + host,
  }));
  const items = JSON.parse(manifest({ scope: { ...scope, targets } })).items;
  const serviceRules = items
    .find((x) => x.kind === 'Role')
    .rules.filter((r) => r.resources.includes('services'));
  assert.deepEqual(
    serviceRules.map((r) => r.resourceNames),
    [['orders']],
  );
  const container = items.find((x) => x.kind === 'Deployment').spec.template.spec.containers[0];
  assert.equal(container.env.find((v) => v.name === 'CONNECTOR_HEALTH_PORT').value, '18086');
  for (const probe of [container.readinessProbe, container.livenessProbe])
    assert.match(probe.exec.command.at(-1), /CONNECTOR_HEALTH_PORT/);
});
test('重复完成旧轮询或旧任务不能影响后继请求', async () => {
  const bus = new Dispatch();
  const first = bus.poll('s', cfg, { ...hello, wait: true });
  const oldWaiter = bus.waiters.get('s');
  oldWaiter();
  await first;
  const second = bus.poll('s', cfg, { ...hello, wait: true }),
    current = bus.waiters.get('s');
  oldWaiter(true);
  assert.equal(bus.waiters.get('s'), current);
  const read = bus.call('s', cfg, 'collect');
  const oldJob = bus.jobs.get('s');
  const { job } = await second;
  bus.result('s', cfg, { ...job, connectorId: 'worker', ok: true });
  await read;
  const next = bus.call('s', cfg, 'collect');
  const nextJob = bus.jobs.get('s');
  oldJob.finish(null, {});
  assert.equal(bus.jobs.get('s'), nextJob);
  const polled = bus.poll('s', cfg, hello);
  assert.equal(polled.job.id, nextJob.job.id);
  nextJob.finish(null, {});
  await next;
  bus.close();
});
test('暂停或到期的未领任务保留额度，已领取后中断则持久保留一次登记', async (t) => {
  const dir = tempDir(),
    db = new Store(dir);
  t.after(() => {
    db.close();
    rmSync(dir, { recursive: true, force: true });
  });
  db.db.prepare('INSERT INTO users VALUES (?,?,?)').run('user', 'user', '{}');
  const ws = db.createSpace('user', 'sample', '').id;
  db.config(ws, { ...db.config(ws), ...cfg });
  const agent = new Pipeline(db),
    bus = agent.bus;
  bus.touch(ws, cfg, hello);
  const plan = { action: 'restore_selector', target, baselineId: 'b', summary: 'baseline' },
    hash = hashPlan(plan);
  const c = {
    generation: 1,
    review: { planHash: hash, decision: 'approve' },
    reviewExpires: new Date(Date.now() + 60000).toISOString(),
    incident: {
      id: 'case',
      status: 'planned',
      target,
      type: 'ServiceSelector',
      plan,
      executionCount: 0,
      timeline: [],
    },
  };
  db.put(ws, 'case', 'case', c);
  async function queued(stop) {
    const executing = agent.execute(ws, c, hash, true, stop.signal);
    const { job } = bus.poll(ws, cfg, hello);
    assert.equal(job.action, 'plan');
    bus.result(ws, cfg, { ...job, connectorId: 'worker', ok: true, plan });
    await Promise.resolve();
    assert.equal(bus.jobs.get(ws)?.job.action, 'execute');
    return { executing };
  }
  const stop = new AbortController();
  const first = await queued(stop);
  bus.pauseAutomatic(ws);
  await assert.rejects(first.executing, /暂停/);
  assert.equal(db.get(ws, 'case', 'case').incident.executionCount, 0);
  const second = await queued(stop);
  bus.jobs.get(ws).job.expiresAt = new Date(Date.now() - 1).toISOString();
  assert.deepEqual(bus.poll(ws, cfg, hello), {});
  await assert.rejects(second.executing, /过期/);
  assert.equal(db.get(ws, 'case', 'case').incident.executionCount, 0);
  const third = await queued(stop);
  assert.equal(bus.poll(ws, cfg, hello).job.action, 'execute');
  assert.equal(db.get(ws, 'case', 'case').incident.executionCount, 1);
  stop.abort();
  await assert.rejects(third.executing, /中止/);
  assert.equal(db.get(ws, 'case', 'case').incident.executionCount, 1);
  await agent.close();
});
test('模型传输重试与参数降级各自有界，取消和凭证错误立即返回', async (t) => {
  const calls = [];
  const server = http.createServer(async (req, res) => {
    let body = '';
    for await (const chunk of req) body += chunk;
    calls.push(JSON.parse(body));
    if (calls.length === 1) return req.socket.destroy(); // Split connection during normal test fixture response.
    if (calls.length === 2) return res.writeHead(503, { 'retry-after': '0' }).end();
    if (calls.length === 3) return res.writeHead(400).end('parallel_tool_calls is unsupported');
    const response = {
      choices: [
        {
          finish_reason: 'stop',
          message: {
            tool_calls: [{ function: { name: 'connection', arguments: '{"connected":true}' } }],
          },
        },
      ],
    };
    return res.end(JSON.stringify(response));
  });
  const base = await listen(server);
  t.after(() => new Promise((ok) => server.close(ok)));
  const model = new Model({ baseUrl: base, model: 'fixture' }, base);
  assert.equal((await model.call('connection', {})).connected, true);
  assert.deepEqual(
    calls.map((v) => v.parallel_tool_calls),
    [false, false, false, undefined],
  );
  await assert.rejects(model.call('connection', {}, AbortSignal.abort()));
  assert.throws(() => modelKey('sample\nkey'), /无效字符/);
  assert.equal(calls.length, 4);
});
test('接收配置兼容 CRLF 与末尾空行并保留结构校验', () => {
  const config = webhook({ publicUrl: 'https://sample.example', webhookToken: 'fixture'.repeat(5) });
  assert.deepEqual(monitoring(config.replaceAll('\n', '\r\n') + '\r\n\r\n'), monitoring(config));
  assert.throws(() => monitoring(config + '\r\nextra'));
});
test('Lab 与平台互斥，初始化失败和正常关闭均释放目录锁', async (t) => {
  const dir = tempDir();
  t.after(() => rmSync(dir, { recursive: true, force: true }));
  assert.throws(() => laboratory({ dir, mode: 'unknown' }));
  const lab = laboratory({ dir });
  await lab.initial;
  assert.throws(() => application({ dir }), /运行中/);
  assert.throws(() => laboratory({ dir }), /运行中/);
  assert.ok(
    lab.server.requestTimeout > 0 &&
      lab.server.headersTimeout <= lab.server.requestTimeout &&
      lab.server.timeout > 0,
  );
  await Promise.all([lab.close(), lab.close()]);
  const platform = application({ dir });
  assert.throws(() => laboratory({ dir }), /运行中/);
  await platform.close();
});
test('健康端口被占用时可取消等待，正常绑定后可读取就绪状态', async (t) => {
  const occupied = http.createServer();
  await listen(occupied);
  const port = occupied.address().port,
    worker = { running: true, lastContact: Date.now() };
  const stop = new AbortController(),
    retry = listenHealth(worker, stop.signal, port);
  await new Promise((done) => setTimeout(done, 20));
  stop.abort();
  assert.equal(await retry, null);
  await new Promise((done) => occupied.close(done));
  const health = await listenHealth(worker, new AbortController().signal, port);
  t.after(() => new Promise((done) => health.close(done)));
  assert.equal((await fetch('http://127.0.0.1:' + port + '/ready')).status, 200);
});
test('Connector 在读取分块响应时执行字节上限', async (t) => {
  const server = http.createServer((req, res) => {
    req.resume();
    res.writeHead(200, { 'content-type': 'application/json' });
    res.write(' '.repeat(700000));
    res.write(' '.repeat(400000));
    res.end('{}');
  });
  const root = await listen(server);
  t.after(() => new Promise((done) => server.close(done)));
  await assert.rejects(
    Connector.prototype.post.call(
      { root, cfg: { token: 'test' } },
      'poll',
      {},
      new AbortController().signal,
    ),
    /平台响应过大/,
  );
});

test('Connector 登记与历史清单只接受不可变镜像', () => {
  const digest = 'a'.repeat(64);
  const image = 'registry.example.com:5000/team/connector:v1@sha256:' + digest;
  for (const ref of ['connector', 'connector:v1', 'connector@sha256:' + digest.toUpperCase(),
    'connector@sha256:' + digest.slice(1), image + '\n', 'other@' + image]) {
    assert.throws(() => connectorImage(ref), /固定摘要/);
    assert.throws(() => manifest({ scope: { ...scope, image: ref } }), /固定摘要/);
  }
  assert.equal(connectorImage(image), image);
  const deployment = JSON.parse(manifest({ scope: { ...scope, image } })).items.find(x => x.kind === 'Deployment');
  assert.equal(deployment.spec.template.spec.containers[0].image, image);
});

test('登录失败按账号累计，冷却跨连接和重启，成功登录清零', async t => {
  const dir = tempDir();
  let store = new Store(dir);
  t.after(() => { store.close(); rmSync(dir, { recursive: true, force: true }); });
  let accounts = new Accounts(store, false, false);
  const pw = 'example-fixture-password';
  const request = (op, name, password, peer) =>
    accounts.handle(op, { username: name, password, confirmation: password }, peer,
      { headers: {} }, { setHeader() {} });
  const { account } = await request('register', 'TestUser', pw, 'loop1');
  await request('register', 'Neighbor', pw, 'loop1');
  // Preload the first four failures without spending the test on password derivations.
  for (let i = 0; i < 4; i++) accounts.recordFailedLogin(account.id);
  await assert.rejects(request('login', 'TESTUSER', 'incorrect', 'loop2'), /账号或密码错误/);
  const state = () => store.db.prepare('SELECT * FROM login_failures WHERE user=?').get(account.id);
  const before = state();
  assert.equal(before.failures, 5);
  assert.ok(before.retry_at > before.last_failure);
  store.close();
  store = new Store(dir);
  accounts = new Accounts(store, false, false);
  // Pin the test clock inside the persisted window even on slow CI.
  t.mock.method(Date, 'now', () => before.last_failure + 1);
  await assert.rejects(request('login', 'testuser', pw, 'loop3'), /账号或密码错误/);
  assert.deepEqual(state(), before);
  assert.equal((await request('login', 'neighbor', pw, 'loop3')).authenticated, true);
  Date.now.mock.mockImplementation(() => before.retry_at + 1);
  assert.equal((await request('login', 'testuser', pw, 'loop3')).authenticated, true);
  assert.equal(state(), undefined);
});

test('入库和模型出口反复筛选仅保留诊断信号、计数和证据引用', () => {
  const marker = 'fixture-private-content';
  const examples = [
    ['Container Log', 'connection refused; ' + marker, 'ConnectionRefused'],
    ['Kubernetes Event', 'FailedScheduling ' + marker + ' insufficient cpu', 'Pending, InsufficientCPU'],
    ['Resource Status', JSON.stringify({ readyReplicas: 2, conditions: [{ message: marker }] }), '{"readyReplicas":2}'],
    ['Business Probe', 'false', 'false'],
    [marker, marker, '无已识别信号'],
  ];
  for (const [source, detail, expected] of examples) {
    const e = publicEvidence({ id: 'ev-1', source, summary: marker, detail, at: marker });
    assert.equal(e.id, 'ev-1');
    assert.equal(e.detail, expected);
    assert.ok(!JSON.stringify(e).includes(marker));
    assert.deepEqual(publicEvidence(e), e);
  }
});
