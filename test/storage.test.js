import test from 'node:test';
import assert from 'node:assert/strict';
import { createCipheriv, randomBytes } from 'node:crypto';
import { join } from 'node:path';
import { tmpdir } from 'node:os';
import { mkdtempSync, mkdirSync, writeFileSync, rmSync, readFileSync, renameSync, symlinkSync, linkSync, unlinkSync, existsSync } from 'node:fs';
import { importV2 } from '../ops/import-v2.js';
import { Store } from '../server/database.js';
import { passwordHash, matches } from '../server/auth.js';
import { laboratory } from '../server/lab.js';
import { setTimeout as sleep } from 'node:timers/promises';
test('旧版 AES-GCM 备份可保留账号、工作空间、加密配置与自建资料，并撤销旧授权', async (t) => {
  const dir = mkdtempSync(join(tmpdir(), 'kp-import-'));
  t.after(() => rmSync(dir, { recursive: true, force: true }));
  const source = join(dir, 'old'),
    dest = join(dir, 'new'),
    account = 'a'.repeat(24),
    ws = 'b'.repeat(24),
    pw = await passwordHash('fixture-Password-17');
  function seal(folder, file, v) {
    mkdirSync(folder, { recursive: true });
    let key;
    try {
      key = readFileSync(join(folder, 'master.key'));
    } catch {
      key = randomBytes(32);
      writeFileSync(join(folder, 'master.key'), key);
    }
    const iv = randomBytes(12),
      c = createCipheriv('aes-256-gcm', key, iv);
    c.setAAD(Buffer.from('kubepilot-platform-v1'));
    const cipher = Buffer.concat([c.update(JSON.stringify(v)), c.final()]);
    writeFileSync(join(folder, file), Buffer.concat([iv, cipher, c.getAuthTag()]));
  }
  seal(join(source, 'identity'), 'platform.enc', {
    schema: 1,
    accounts: { [account]: { id: account, username: 'legacy', password: pw } },
    workspaces: { [ws]: { id: ws, name: '旧空间', ownerId: account } },
  });
  const folder = join(source, 'workspaces', ws);
  seal(folder, 'platform.enc', {
    schema: 1,
    config: { apiKey: 'test-only-migrated', scope: { automatic: true }, generation: 9 },
    cases: [],
  });
  seal(folder, 'knowledge.enc', [{ id: 'doc-one', title: '旧资料', content: '文档' }]);
  assert.equal(importV2(source, dest).workspaces, 1);
  // A valid backup with a linked directory or shared file must leave no partial import.
  const rejected = join(dir, 'refused');
  for (const path of [join(source, 'identity'), join(source, 'workspaces'), folder]) {
    const moved = join(dir, 'detached');
    renameSync(path, moved);
    symlinkSync(moved, path, 'junction');
    assert.throws(() => importV2(source, rejected), /备份目录/);
    assert.equal(existsSync(rejected), false);
    unlinkSync(path);
    renameSync(moved, path);
  }
  for (const file of ['platform.enc', 'knowledge.enc', 'master.key']) {
    const alias = join(dir, 'duplicate-file');
    linkSync(join(folder, file), alias);
    assert.throws(() => importV2(source, rejected), /普通文件/);
    assert.equal(existsSync(rejected), false);
    unlinkSync(alias);
  }
  const db = new Store(dest);
  try {
    assert.equal(db.workspace(account).name, '旧空间');
    assert.equal(db.config(ws).scope.automatic, false);
    assert.equal(db.config(ws).apiKey, 'test-only-migrated');
    assert.equal(db.list(ws, 'doc').length, 1);
    assert.equal(
      await matches(
        'fixture-Password-17',
        JSON.parse(db.db.prepare('SELECT password FROM users').get().password),
      ),
      true,
    );
    assert.throws(() => importV2(source, dest));
  } finally {
    db.close();
  }
});
test('独立实验台：注入、只读诊断、人工确认、业务恢复；审批摘要绑定', async (t) => {
  const dir = mkdtempSync(join(tmpdir(), 'kp-lab-'));
  const lab = laboratory({ dir });
  await lab.initial;
  await new Promise((ok) => lab.server.listen(0, '127.0.0.1', ok));
  const root = 'http://127.0.0.1:' + lab.server.address().port + '/api';
  t.after(async () => {
    await lab.close();
    rmSync(dir, { recursive: true, force: true });
  });
  async function call(path, b) {
    const res = await fetch(root + path, {
      method: b ? 'POST' : 'GET',
      headers: { 'Content-Type': 'application/json', 'X-Requested-With': 'KubePilot' },
      body: b ? JSON.stringify(b) : undefined,
    });
    return { status: res.status, v: await res.json() };
  }
  await call('/demo/inject', { fault: 'ServiceSelector' });
  const req = { namespace: 'kube-agent-lab', workload: 'payment-service', mode: 'approval' };
  const r = (await call('/agent/runs', req)).v;
  let run;
  for (let k = 0; k < 100; k++) {
    run = (await call('/state')).v.agentRuns.find((x) => x.id === r.id);
    if (run?.status === 'awaiting_approval') break;
    await sleep(10);
  }
  assert.equal(run.status, 'awaiting_approval');
  const incident='/incidents/'+run.incidentId+'/execute';
  assert.equal((await call(incident,{})).status,409);
  assert.equal((await call(incident,{planHash:'stale'})).status,409);
  assert.equal((await call('/agent/runs/' + r.id + '/approve', { planHash: 'stale' })).status, 409);
  assert.equal((await call('/agent/runs/' + r.id + '/approve', { planHash: run.planHash })).status, 200);
  for (let k = 0; k < 100; k++) {
    run = (await call('/state')).v.agentRuns.find((x) => x.id === r.id);
    if (run.status === 'recovered') break;
    await sleep(10);
  }
  assert.equal(run.status, 'recovered');
});
