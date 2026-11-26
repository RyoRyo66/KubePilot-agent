import { createHmac, pbkdf2, randomBytes, timingSafeEqual } from 'node:crypto';
import { promisify } from 'node:util';
import { isIP } from 'node:net';
import { id, digest, ensure, string, Problem } from './common.js';
const derive = promisify(pbkdf2),
  rounds = 600000,
  ttl = 12 * 3600_000;
export async function passwordHash(p, salt = randomBytes(24).toString('base64'), cost = rounds) {
  return {
    algorithm: 'pbkdf2-sha256',
    iterations: cost,
    salt,
    hash: (await derive(p, Buffer.from(salt, 'base64'), cost, 32, 'sha256')).toString('base64'),
  };
}
export async function matches(p, d) {
  if (
    typeof p !== 'string' ||
    Buffer.byteLength(p) > 512 ||
    d.algorithm !== 'pbkdf2-sha256' ||
    !Number.isInteger(d.iterations) ||
    d.iterations < 600000 ||
    d.iterations > 2000000
  )
    return false;
  const expected = Buffer.from(d.hash, 'base64'),
    actual = await passwordHash(p, d.salt, d.iterations);
  return expected.length === 32 && timingSafeEqual(expected, Buffer.from(actual.hash, 'base64'));
}
function password(p, confirmation) {
  ensure(
    typeof p === 'string' &&
      p.trim() &&
      [...p].length >= 12 &&
      [...p].length <= 128 &&
      Buffer.byteLength(p) <= 512,
    '密码需要 12–128 个字符',
  );
  ensure(p === confirmation, '两次密码不一致');
  return p;
}
export class Budget {
  constructor(capacity) {
    this.capacity = capacity;
    this.entries = new Map();
  }
  charge(key, limit, duration) {
    const time = Date.now();
    let slot = this.entries.get(key);
    if (!slot || slot.until <= time) {
      if (!slot && this.entries.size >= this.capacity)
        this.entries.delete(this.entries.keys().next().value);
      slot = { until: time + duration, used: 0 };
    }
    this.entries.delete(key);
    this.entries.set(key, slot);
    ensure(slot.used < limit, '尝试过于频繁，请稍后重试', 429);
    slot.used++;
  }
}
export class Accounts {
  constructor(store, closed, secure) {
    this.store = store;
    this.db = store.db;
    this.closed = closed;
    this.secure = secure;
    this.cookie = secure ? '__Host-kubepilot_session' : 'kubepilot_session';
    this.ipBudget = new Budget(32768);
    this.accountBudget = new Budget(65536);
    this.challengeKey = randomBytes(32);
    this.slots = 0;
    this.byPeer = new Map();
    this.dummy = {
      algorithm: 'pbkdf2-sha256',
      iterations: rounds,
      salt: randomBytes(24).toString('base64'),
      hash: randomBytes(32).toString('base64'),
    };
  }
  checkWork(name, password, proof) {
    const tag = prefix => createHmac('sha256', this.challengeKey)
      .update(JSON.stringify([name, password, prefix])).digest('hex');
    const [expires, nonce, mac] = String(proof?.challenge || '').slice(0, 180).split('.');
    const prefix = expires + '.' + nonce;
    if (/^[0-9]{13}$/.test(expires) && Number(expires) >= Date.now() &&
        mac && /^[a-f0-9]{64}$/.test(mac) && timingSafeEqual(Buffer.from(mac), Buffer.from(tag(prefix))) &&
        Number.isSafeInteger(proof.answer) && proof.answer >= 0 &&
        digest(proof.challenge + ':' + proof.answer).startsWith('0000')) return;
    const next = (Date.now() + 120000) + '.' + id();
    const error = new Problem('请完成登录验证', 428, 'login_work');
    error.challenge = next + '.' + tag(next);
    throw error;
  }
  recordFailedLogin(user) {
    const time = Date.now();
    this.store.tx(() => {
      const last = this.db.prepare('SELECT * FROM login_failures WHERE user=?').get(user);
      const failures = last && time - last.last_failure < 900000 ? Math.min(last.failures + 1, 11) : 1;
      const delay = failures < 5 ? 0 : Math.min(60000, 1000 * 2 ** (failures - 5));
      this.db.prepare('INSERT OR REPLACE INTO login_failures VALUES (?,?,?,?)')
        .run(user, failures, time, time + delay);
    });
  }
  token(req) {
    return (
      (req.headers.cookie || '')
        .split(/;\s*/)
        .find((v) => v.startsWith(this.cookie + '='))
        ?.slice(this.cookie.length + 1) || ''
    );
  }
  user(token) {
    return this.db
      .prepare(
        'SELECT u.id,u.name AS username FROM sessions s JOIN users u ON u.id=s.user WHERE s.hash=? AND s.expires>?',
      )
      .get(digest(token), Date.now());
  }
  session(a) {
    return a
      ? {
          authenticated: true,
          registrationOpen: !this.closed,
          account: a,
          workspace: this.store.workspace(a.id),
        }
      : { authenticated: false, registrationOpen: !this.closed };
  }
  setCookie(res, token) {
    res.setHeader(
      'Set-Cookie',
      this.cookie +
        '=' +
        token +
        '; Path=/; HttpOnly; SameSite=Strict; Max-Age=' +
        (token ? ttl / 1000 : 0) +
        (this.secure ? '; Secure' : ''),
    );
  }
  async handle(action, b, ip, req, res) {
    const token = this.token(req),
      current = this.user(token);
    if (action === 'session') return this.session(current);
    if (action === 'logout') {
      this.db.prepare('DELETE FROM sessions WHERE hash=?').run(digest(token));
      this.setCookie(res, '');
      return { ok: true };
    }
    ensure(['login', 'register', 'password'].includes(action), '接口不存在', 404);
    const occupied = this.byPeer.get(ip) || 0;
    ensure(this.slots < 4 && occupied < 2, '登录服务繁忙', 429);
    this.ipBudget.charge(ip, 100, 900000);
    const name =
      action === 'password'
        ? current?.username
        : string(b.username, 1, 32, '用户名').normalize('NFC').toLowerCase();
    ensure(name, '登录已失效', 401);
    this.accountBudget.charge(
      JSON.stringify([action, ip, action === 'register' ? '' : name]),
      action === 'register' ? 5 : 15,
      900000,
    );
    this.byPeer.set(ip, occupied + 1);
    this.slots++;
    try {
      let row = this.db.prepare('SELECT * FROM users WHERE name=?').get(name);
      if (action === 'register') {
        ensure(!this.closed, '已关闭注册', 403);
        ensure(/^[\p{L}\p{N}_-]{3,32}$/u.test(name), '用户名无效');
        const pw = await passwordHash(password(b.password, b.confirmation));
        ensure(!this.db.prepare('SELECT id FROM users WHERE name=?').get(name), '用户名已存在', 409);
        row = { id: id(), name, password: JSON.stringify(pw) };
        this.db.prepare('INSERT INTO users VALUES (?,?,?)').run(row.id, name, row.password);
      } else {
        if (action === 'login' && row) {
          const budget = this.db.prepare('SELECT retry_at FROM login_failures WHERE user=?').get(row.id);
          if (budget && budget.retry_at > Date.now()) this.checkWork(name, b.password, b.proof);
        }
        const ok = await matches(
          action === 'login' ? b.password : b.currentPassword,
          row ? JSON.parse(row.password) : this.dummy,
        );
        if (action === 'login' && row && !ok) this.recordFailedLogin(row.id);
        ensure(ok && row, '账号或密码错误', 401);
        ensure(
          this.db.prepare('SELECT password FROM users WHERE id=?').get(row.id)?.password ===
            row.password,
          '凭证已改变',
          409,
        );
        if (action === 'password') {
          const next = await passwordHash(password(b.password, b.confirmation));
          this.store.tx(() => {
            ensure(this.user(token)?.id === row.id, '请重新登录', 401);
            ensure(
              this.db
                .prepare('UPDATE users SET password=? WHERE id=? AND password=?')
                .run(JSON.stringify(next), row.id, row.password).changes,
              '凭证已改变',
              409,
            );
            this.db.prepare('DELETE FROM login_failures WHERE user=?').run(row.id);
            this.db.prepare('DELETE FROM sessions WHERE user=?').run(row.id);
          });
          this.setCookie(res, '');
          return { ok: true };
        }
      }
      const raw = id();
      this.store.tx(() => {
        this.db.prepare('DELETE FROM login_failures WHERE user=?').run(row.id);
        this.db.prepare('DELETE FROM sessions WHERE expires<=?').run(Date.now());
        this.db
          .prepare('INSERT INTO sessions VALUES (?,?,?)')
          .run(digest(raw), row.id, Date.now() + ttl);
        this.db
          .prepare(
            'DELETE FROM sessions WHERE hash IN (SELECT hash FROM sessions WHERE user=? ORDER BY rowid DESC LIMIT -1 OFFSET 5)',
          )
          .run(row.id);
      });
      this.setCookie(res, raw);
      return this.session({ id: row.id, username: row.name });
    } finally {
      this.slots--;
      const remaining = this.byPeer.get(ip) - 1;
      if (remaining) this.byPeer.set(ip, remaining);
      else this.byPeer.delete(ip);
    }
  }
}
