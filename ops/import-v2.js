// Offline import from a stopped v0.2 backup into a separate, new directory.
import { createDecipheriv } from 'node:crypto';
import {
  existsSync,
  constants, closeSync, fstatSync, lstatSync, openSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  renameSync,
  rmSync,
  realpathSync,
} from 'node:fs';
import { basename, dirname, isAbsolute, join, relative, resolve, sep } from 'node:path';
import { pathToFileURL } from 'node:url';
import { Store } from '../server/database.js';
import { digest, ensure, now } from '../server/common.js';
function inside(root, path) {
  const rel = relative(root, path);
  return rel !== '..' && !rel.startsWith('..' + sep) && !isAbsolute(rel);
}
function backupFile(root, dirs, file, optional) {
  function inspect() {
    ensure(lstatSync(root).isDirectory() && relative(root, realpathSync(root)) === '',
      '备份根目录在导入期间发生变化');
    let dir = root;
    for (const segment of dirs) {
      dir = join(dir, segment);
      const stat = lstatSync(dir);
      ensure(stat.isDirectory() && !stat.isSymbolicLink() && inside(root, realpathSync(dir)),
        '备份目录必须位于源目录内，不能使用链接');
    }
    const path = join(dir, file);
    const stat = lstatSync(path, { throwIfNoEntry: false, bigint: true });
    if (!stat && optional) return null;
    ensure(stat?.isFile() && stat.nlink === 1n && inside(root, realpathSync(path)),
      '备份文件必须是源目录内的独立普通文件');
    return { path, stat };
  }
  const entry = inspect();
  if (!entry) return null;
  const fd = openSync(entry.path, constants.O_RDONLY | (constants.O_NOFOLLOW ?? 0) | (constants.O_NONBLOCK ?? 0));
  try {
    const opened = fstatSync(fd, { bigint: true });
    const checked = inspect();
    ensure(opened.isFile() && opened.nlink === 1n && checked &&
      [entry.stat, checked.stat].every(s => s.dev === opened.dev && s.ino === opened.ino),
      '备份文件在导入期间发生变化');
    return readFileSync(fd);
  } finally {
    closeSync(fd);
  }
}
function decode(root, dirs, file, optional = false) {
  const bytes = backupFile(root, dirs, file, optional);
  if (!bytes) return null;
  const key = backupFile(root, dirs, 'master.key', false);
  const d = createDecipheriv('aes-256-gcm', key, bytes.subarray(0, 12));
  d.setAAD(Buffer.from('kubepilot-platform-v1'));
  d.setAuthTag(bytes.subarray(-16));
  return JSON.parse(Buffer.concat([d.update(bytes.subarray(12, -16)), d.final()]));
}
export function importV2(source, destination) {
  const src = realpathSync(source),
    dest = resolve(destination);
  let parent = dirname(dest);
  while (!existsSync(parent)) parent = dirname(parent);
  const location = resolve(realpathSync(parent), relative(parent, dest));
  ensure(
    !lstatSync(dest, { throwIfNoEntry: false }) && !inside(src, location),
    '目标必须是旧备份以外的新目录',
  );
  const identity = decode(src, ['identity'], 'platform.enc');
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
        const dirs = ws.legacy ? [] : ['workspaces', ws.id];
        const state = decode(src, dirs, 'platform.enc');
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
        for (const doc of decode(src, dirs, 'knowledge.enc', true) || []) {
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
