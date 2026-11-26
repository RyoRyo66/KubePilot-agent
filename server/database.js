import { privateRoot, sqlite, sqliteFiles, storageKey } from './storage.js';
import { createCipheriv, createDecipheriv, randomBytes } from 'node:crypto';
import { id, now, ensure, digest } from './common.js';

// The lock lives in a separate SQLite file so ordinary data transactions remain short.
export function withRuntimeStore(dir, initialize) {
  privateRoot(dir, [...sqliteFiles('process-lock.db'), ...sqliteFiles('kubepilot.db'), 'storage.key']);
  const lock = sqlite(dir, 'process-lock.db');
  try {
    lock.exec('BEGIN EXCLUSIVE');
  } catch (error) {
    lock.close();
    if (error.errcode === 5 || error.errcode === 6)
      throw new Error('数据目录已有运行中的 KubePilot 进程');
    throw error;
  }
  let store;
  function release() {
    try {
      store?.close();
    } finally {
      lock.close();
    }
  }
  try {
    store = new Store(dir);
    const runtime = initialize(store);
    let closing;
    return {
      ...runtime,
      close() {
        closing ??= Promise.resolve()
          .then(() => runtime.close())
          .finally(release);
        return closing;
      },
    };
  } catch (error) {
    release();
    throw error;
  }
}
export class Store {
  constructor(dir) {
    privateRoot(dir, [...sqliteFiles('kubepilot.db'), 'storage.key']);
    this.key = storageKey(dir);
    this.db = sqlite(dir, 'kubepilot.db');
    this.db.exec(`
      PRAGMA journal_mode=WAL; PRAGMA synchronous=FULL; PRAGMA foreign_keys=ON; PRAGMA busy_timeout=4000;
      CREATE TABLE IF NOT EXISTS users(id TEXT PRIMARY KEY, name TEXT UNIQUE NOT NULL, password TEXT NOT NULL);
      CREATE TABLE IF NOT EXISTS login_failures(user TEXT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE, failures INTEGER NOT NULL, last_failure INTEGER NOT NULL, retry_at INTEGER NOT NULL);
      CREATE TABLE IF NOT EXISTS sessions(hash TEXT PRIMARY KEY, user TEXT REFERENCES users(id), expires INTEGER);
      CREATE TABLE IF NOT EXISTS spaces(id TEXT PRIMARY KEY, owner TEXT UNIQUE REFERENCES users(id), info TEXT NOT NULL, config BLOB NOT NULL);
      CREATE TABLE IF NOT EXISTS credentials(hash TEXT PRIMARY KEY, space TEXT REFERENCES spaces(id), kind TEXT, UNIQUE(space,kind));
      CREATE TABLE IF NOT EXISTS entries(space TEXT REFERENCES spaces(id), kind TEXT, key TEXT, value BLOB NOT NULL, PRIMARY KEY(space,kind,key));

      CREATE INDEX IF NOT EXISTS sessions_user ON sessions(user);
      CREATE INDEX IF NOT EXISTS sessions_expires ON sessions(expires);
    `);
    this.depth = 0;
    this.tx(() => {
      if (
        !this.db
          .prepare('PRAGMA table_info(entries)')
          .all()
          .some((c) => c.name === 'updated_at')
      ) {
        this.db.exec(
          'ALTER TABLE entries ADD COLUMN updated_at INTEGER NOT NULL DEFAULT 0; UPDATE entries SET updated_at=rowid',
        );
      }
      this.db.exec('CREATE INDEX IF NOT EXISTS entries_recent ON entries(space,kind,updated_at DESC)');
    });
    this.clock = this.db
      .prepare('SELECT COALESCE(MAX(updated_at),0) AS latest FROM entries')
      .get().latest;
  }
  close() {
    this.db.close();
  }
  tx(f) {
    const nested = this.db.isTransaction;
    const savepoint = 'nested_' + ++this.depth;
    this.db.exec(nested ? 'SAVEPOINT ' + savepoint : 'BEGIN IMMEDIATE');
    try {
      const value = f();
      this.db.exec(nested ? 'RELEASE ' + savepoint : 'COMMIT');
      return value;
    } catch (error) {
      try {
        if (this.db.isTransaction)
          this.db.exec(nested ? 'ROLLBACK TO ' + savepoint + '; RELEASE ' + savepoint : 'ROLLBACK');
      } catch (rollbackError) {
        console.error('SQLite rollback failed:', rollbackError.code);
      }
      throw error;
    } finally {
      this.depth--;
    }
  }
  count(ws, kind) {
    return this.db.prepare('SELECT COUNT(*) n FROM entries WHERE space=? AND kind=?').get(ws, kind).n;
  }
  seal(value, context) {
    const iv = randomBytes(12),
      c = createCipheriv('aes-256-gcm', this.key, iv);
    c.setAAD(Buffer.from(context));
    const data = Buffer.concat([c.update(JSON.stringify(value), 'utf8'), c.final()]);
    return Buffer.concat([iv, c.getAuthTag(), data]);
  }
  open(bytes, context) {
    const b = Buffer.from(bytes),
      d = createDecipheriv('aes-256-gcm', this.key, b.subarray(0, 12));
    d.setAAD(Buffer.from(context));
    d.setAuthTag(b.subarray(12, 28));
    return JSON.parse(Buffer.concat([d.update(b.subarray(28)), d.final()]));
  }
  createSpace(user, name, description) {
    const ws = {
      id: id(),
      ownerId: user,
      name,
      description,
      createdAt: now(),
      onboardingComplete: false,
    };
    const cfg = {
      publicUrl: '',
      allowHttp: false,
      model: '',
      baseUrl: '',
      apiKey: '',
      modelTested: false,
      generation: 0,
      scope: { namespace: '', image: '', automatic: false, targets: [], actions: [] },
    };
    this.tx(() => {
      this.db
        .prepare('INSERT INTO spaces VALUES (?,?,?,?)')
        .run(ws.id, user, JSON.stringify(ws), this.seal(cfg, ws.id));
      this.rotate(ws.id, cfg, 'webhook');
      this.config(ws.id, cfg);
    });
    return ws;
  }
  workspace(user) {
    const r = this.db.prepare('SELECT info FROM spaces WHERE owner=?').get(user);
    return r ? JSON.parse(r.info) : null;
  }
  update(ws) {
    this.db.prepare('UPDATE spaces SET info=? WHERE id=?').run(JSON.stringify(ws), ws.id);
  }
  config(ws, next) {
    if (next) {
      this.db.prepare('UPDATE spaces SET config=? WHERE id=?').run(this.seal(next, ws), ws);
      return;
    }
    return this.open(this.db.prepare('SELECT config FROM spaces WHERE id=?').get(ws).config, ws);
  }
  rotate(ws, cfg, kind) {
    const token = id();
    cfg[kind + 'Token'] = token;
    this.db
      .prepare(
        'INSERT INTO credentials VALUES (?,?,?) ON CONFLICT(space,kind) DO UPDATE SET hash=excluded.hash',
      )
      .run(digest(token), ws, kind);
  }
  machine(token, kind) {
    return this.db
      .prepare('SELECT space FROM credentials WHERE hash=? AND kind=?')
      .get(digest(token), kind)?.space;
  }
  put(ws, kind, key, value) {
    this.db
      .prepare(
        'INSERT INTO entries(space,kind,key,value,updated_at) VALUES (?,?,?,?,?) ON CONFLICT(space,kind,key) DO UPDATE SET value=excluded.value,updated_at=excluded.updated_at',
      )
      .run(
        ws,
        kind,
        key,
        this.seal(value, [ws, kind, key].join('/')),
        (this.clock = Math.max(Date.now(), this.clock + 1)),
      );
  }
  get(ws, kind, key) {
    const r = this.db
      .prepare('SELECT value FROM entries WHERE space=? AND kind=? AND key=?')
      .get(ws, kind, key);
    return r && this.open(r.value, [ws, kind, key].join('/'));
  }
  list(ws, kind) {
    return this.db
      .prepare('SELECT key,value FROM entries WHERE space=? AND kind=? ORDER BY rowid')
      .all(ws, kind)
      .map((r) => this.open(r.value, [ws, kind, r.key].join('/')));
  }
  del(ws, kind, key) {
    return this.db.prepare('DELETE FROM entries WHERE space=? AND kind=? AND key=?').run(ws, kind, key);
  }
  trim(ws, kind, max) {
    this.db
      .prepare(
        'DELETE FROM entries WHERE rowid IN (SELECT rowid FROM entries WHERE space=? AND kind=? ORDER BY updated_at DESC LIMIT -1 OFFSET ?)',
      )
      .run(ws, kind, max);
  }
  event(ws, action, detail) {
    const key = id();
    this.put(ws, 'event', key, { id: key, at: now(), action, detail });
    this.trim(ws, 'event', 300);
  }
}
