import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtempSync, rmSync } from 'node:fs';
import { join } from 'node:path';
import { tmpdir } from 'node:os';
import { Budget } from '../server/auth.js';
import { Dispatch } from '../server/dispatch.js';
import { Store } from '../server/database.js';
import { monitoring } from '../ops/monitoring.js';
import { webhook } from '../server/manifest.js';
import { Pipeline } from '../server/pipeline.js';

test('限流按完整键独立计数，容量有界', () => {
  const budget = new Budget(3);
  for (const k of ['peer1/user', 'peer2/user', 'peer1/other']) budget.charge(k, 1, 1000);
  assert.throws(() => budget.charge('peer1/user', 1, 1000));
  budget.charge('peer3/user', 1, 1000);
  assert.equal(budget.entries.size, 3);
});

test('长轮询立即派工，结果验证后续期，独立心跳不领取任务', async () => {
  const bus = new Dispatch(),
    cfg = { generation: 1, scope: { namespace: 'sample', targets: [], automatic: true } };
  const hello = {
    protocol: 4,
    heartbeat: true,
    wait: true,
    connection: { generation: 1, namespace: 'sample', id: 'worker', provider: 'demo' },
  };
  const pending = bus.poll('space', cfg, hello);
  const done = bus.call('space', cfg, 'collect');
  bus.touch('space', cfg, hello);
  const { job } = await pending;
  const connection = bus.connections.get('space');
  connection.lastSeen = new Date(Date.now() - 30000).toISOString();
  assert.equal(bus.online('space', cfg), false);
  assert.throws(() =>
    bus.touch('space', cfg, { ...hello, connection: { ...hello.connection, id: 'new-worker' } }),
  );
  assert.throws(() => bus.result('space', cfg, { ...job, lease: 'stale', connectorId: 'worker' }));
  assert.equal(bus.online('space', cfg), false);
  bus.result('space', cfg, { ...job, connectorId: 'worker', ok: true });
  assert.ok((await done).ok && bus.online('space', cfg));
  connection.lastSeen = new Date(Date.now() - 17000).toISOString();
  bus.touch('space', cfg, { ...hello, connection: { ...hello.connection, id: 'new-worker' } });
  assert.equal(bus.connections.get('space').id, 'new-worker');
  const expired = new Date(Date.now() - 1000).toISOString();
  assert.throws(() => bus.call('space', cfg, 'execute', { reviewExpires: expired }));
  const approvalDue = new Date(Date.now() + 3000).toISOString();
  const write = bus.call('space', cfg, 'execute', { automatic: true, reviewExpires: approvalDue });
  const incoming = { ...hello, wait: false, connection: { ...hello.connection, id: 'new-worker' } };
  const next = bus.poll('space', cfg, incoming);
  assert.ok(Date.parse(next.job.expiresAt) <= Date.parse(approvalDue));
  bus.result('space', cfg, { ...next.job, ok: true, connectorId: 'new-worker' });
  await write;
  bus.close();
});
function space(t) {
  const dir = mkdtempSync(join(tmpdir(), 'kp-storage-')),
    db = new Store(dir);
  t.after(() => {
    db.close();
    rmSync(dir, { recursive: true, force: true });
  });
  db.db.prepare('INSERT INTO users VALUES (?,?,?)').run('user', 'user', '{}');
  return { db, ws: db.createSpace('user', 'sample', '').id };
}
test('嵌套事务独立回滚，容量清理保留最近更新的记录', (t) => {
  const { db, ws } = space(t);
  db.tx(() => {
    db.put(ws, 'alert', 'a', 'first');
    assert.throws(
      () =>
        db.tx(() => {
          db.put(ws, 'alert', 'bad', 'bad');
          throw Error('failed');
        }),
      /failed/,
    );
    db.put(ws, 'alert', 'b', 'second');
    db.put(ws, 'alert', 'a', 'refreshed');
  });
  db.trim(ws, 'alert', 1);
  assert.deepEqual(db.list(ws, 'alert'), ['refreshed']);
});
test('活动事件已满仍提交巡检快照，重新登记后可建立新事件', async (t) => {
  const { db, ws } = space(t),
    agent = new Pipeline(db),
    cfg = db.config(ws);
  cfg.generation = 1;
  cfg.scope = { namespace: 'sample', targets: [{ kind: 'Service', name: 'api' }] };
  db.config(ws, cfg);
  const target = { ...cfg.scope.targets[0], namespace: 'sample', version: '2', uid: 'current' };
  const finding = { type: 'ServiceSelector', target, evidence: [{ id: 'ev' }] };
  for (let i = 0; i < 150; i++)
    db.put(ws, 'case', String(i), {
      generation: 1,
      incident: { id: String(i), key: String(i), target, status: 'rejected', timeline: [] },
    });
  agent.bus.call = async (ws, c, action) =>
    action === 'collect' ? { snapshot: { findings: [finding] } } : { ok: false };
  const signal = new AbortController().signal;
  await agent.scan(ws, signal);
  assert.ok(db.get(ws, 'system', 'lastScan'));
  assert.match(db.get(ws, 'system', 'lastError'), /150/);
  cfg.generation++;
  db.config(ws, cfg);
  await agent.scan(ws, signal);
  assert.equal(agent.activeCases(ws).length, 150);
  assert.ok(agent.activeCases(ws).some((c) => c.generation === cfg.generation));
  await agent.close();
});
test('监控配置按单个 receiver 序列化，明确拒绝其他内容', () => {
  const config = webhook({ publicUrl: 'https://example.org', webhookToken: 'X'.repeat(32) });
  const secret = monitoring(config).items.find((x) => x.kind === 'Secret');
  const v = JSON.parse(secret.stringData['alertmanager.yml']);
  assert.equal(v.receivers.length, 1);
  assert.equal(v.receivers[0].webhook_configs[0].url, 'https://example.org/ingest/alertmanager');
  assert.throws(() => monitoring(config + '\n# extra'));
});
