// Offline import from a stopped v0.2 backup into a separate, new directory.
import { createDecipheriv } from 'node:crypto';
import {
  existsSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  renameSync,
  rmSync,
  realpathSync,
} from 'node:fs';
import { basename, dirname, join, resolve, sep } from 'node:path';
import { pathToFileURL } from 'node:url';
import { Store } from '../server/database.js';
import { digest, ensure, now } from '../server/common.js';
function decode(dir, file) {
  const bytes = readFileSync(join(dir, file)),
    d = createDecipheriv('aes-256-gcm', readFileSync(join(dir, 'master.key')), bytes.subarray(0, 12));
  d.setAAD(Buffer.from('kubepilot-platform-v1'));
  d.setAuthTag(bytes.subarray(-16));
  return JSON.parse(Buffer.concat([d.update(bytes.subarray(12, -16)), d.final()]));
}
export function importV2(source, destination) {
  const src = realpathSync(source),
    dest = resolve(destination);
  ensure(
    !existsSync(dest) && dest !== src && !dest.startsWith(src + sep),
    '目标必须是旧备份以外的新目录',
  );
  const identity = decode(join(src, 'identity'), 'platform.enc');
  ensure(identity.schema === 1 && identity.accounts && identity.workspaces, '账号库格式不受支持');
  mkdirSync(dirname(dest), { recursive: true });
  const staging = mkdtempSync(join(dirname(dest), '.' + basename(dest) + '-'));
  let db;
  const counts = { accounts: 0, workspaces: 0, cases: 0, documents: 0 };
  try {
    db = new Store(staging);
    db.tx(() => {
      for (const a of Object.values(identity.accounts)) {
        ensure(
          a.password.algorithm === 'pbkdf2-sha256' && a.password.iterations === 600000,
          '旧账号摘要格式不受支持',
        );
        db.db
          .prepare('INSERT INTO users VALUES (?,?,?)')
          .run(a.id, a.username, JSON.stringify(a.password));
        counts.accounts++;
      }
      for (const ws of Object.values(identity.workspaces)) {
        ensure(/^[A-Za-z0-9_-]{24}$/.test(ws.id), '工作空间标识无效');
        const dir = ws.legacy ? src : join(src, 'workspaces', ws.id);
        const state = decode(dir, 'platform.enc');
        ensure(state.schema === 1, '工作空间格式不受支持');
        const cfg = {
          ...state.config,
          scope: {
            ...state.config.scope,
            automatic: false,
            actions: state.config.scope?.actions || [],
            targets: state.config.scope?.targets || [],
          },
        };
        cfg.modelTested = false;
        cfg.connectorToken = '';
        cfg.generation = (cfg.generation || 0) + 1;
        ws.onboardingComplete = false;
        db.db
          .prepare('INSERT INTO spaces VALUES (?,?,?,?)')
          .run(ws.id, ws.ownerId, JSON.stringify(ws), db.seal(cfg, ws.id));
        counts.workspaces++;
        if (cfg.webhookToken)
          db.db
            .prepare('INSERT INTO credentials VALUES (?,?,?)')
            .run(digest(cfg.webhookToken), ws.id, 'webhook');
        for (const c of state.cases || []) {
          if (c.incident.status !== 'resolved') {
            c.incident.status = 'manual';
            c.incident.timeline.push({
              at: now(),
              status: 'manual',
              title: '历史事件已导入',
              detail: '旧授权已过期，请以新版巡检结果核查。',
            });
          }
          delete c.reviewExpires;
          db.put(ws.id, 'case', c.incident.id, c);
          counts.cases++;
        }
        for (const a of state.alerts || [])
          db.put(ws.id, 'alert', digest(JSON.stringify([a.fingerprint, a.startsAt])), a);
        for (const e of state.events || []) db.put(ws.id, 'event', e.id, e);
        if (state.snapshot) db.put(ws.id, 'system', 'snapshot', state.snapshot);
        if (state.lastScan) db.put(ws.id, 'system', 'lastScan', state.lastScan);
        if (existsSync(join(dir, 'knowledge.enc')))
          for (const doc of decode(dir, 'knowledge.enc') || []) {
            db.put(ws.id, 'doc', doc.id, doc);
            counts.documents++;
          }
        db.event(ws.id, 'migration', '已导入历史数据，请重新测试模型并登记新版执行端');
      }
    });
    db.close();
    db = null;
    renameSync(staging, dest);
  } catch (error) {
    try {
      db?.close();
    } finally {
      if (
        dirname(resolve(staging)) === dirname(dest) &&
        basename(staging).startsWith('.' + basename(dest) + '-')
      )
        rmSync(staging, { recursive: true, force: true });
    }
    throw error;
  }
  return counts;
}
if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  const [src, dst] = process.argv.slice(2);
  ensure(src && dst, '用法：node ops/import-v2.js 旧版备份目录 新目录');
  console.log(importV2(src, dst));
}
