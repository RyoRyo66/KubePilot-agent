import http from 'node:http';
import { readFileSync, mkdirSync } from 'node:fs';
import { resolve } from 'node:path';
import { pathToFileURL } from 'node:url';
import { DatabaseSync } from 'node:sqlite';
import { setTimeout as sleep } from 'node:timers/promises';
import { ensure, hashPlan, id, inScope, origin, scopeOf, Problem } from '../server/common.js';
import { Kubernetes } from './kube.js';
export class Connector {
  constructor(enroll, dir, provider, options = {}) {
    ensure(
      enroll.protocol === 3 &&
        typeof enroll.token === 'string' &&
        enroll.token.length >= 25 &&
        Number.isSafeInteger(enroll.generation) &&
        enroll.generation > 0,
      '登记信息无效',
    );
    this.scope = scopeOf(enroll.scope);
    this.cfg = enroll;
    this.root = origin(enroll.serverUrl, enroll.allowHttp === true);
    this.provider =
      provider ||
      new Kubernetes(
        this.scope,
        undefined,
        undefined,
        (process.env.CONNECTOR_PROBE_ORIGINS || '').split(',').filter(Boolean),
      );
    this.kind = options.kind || 'kubernetes';
    this.delay = options.verifyDelay ?? 1500;
    mkdirSync(dir, { recursive: true, mode: 0o700 });
    this.db = new DatabaseSync(resolve(dir, 'connector.db'));
    this.db.exec(
      'PRAGMA journal_mode=WAL; PRAGMA synchronous=FULL; CREATE TABLE IF NOT EXISTS kv(k TEXT PRIMARY KEY,v TEXT NOT NULL); CREATE TABLE IF NOT EXISTS attempts(binding TEXT, incident TEXT, PRIMARY KEY(binding,incident));',
    );
    this.binding = hashPlan([this.root, enroll.token, enroll.generation, this.scope.namespace]);
    if (
      !this.db
        .prepare('PRAGMA table_info(attempts)')
        .all()
        .some((c) => c.name === 'expires')
    ) {
      this.db.exec('ALTER TABLE attempts ADD COLUMN expires INTEGER');
    }
    this.db.exec('CREATE INDEX IF NOT EXISTS attempts_expiry ON attempts(expires)');
    this.id = this.get('id') || id();
    this.put('id', this.id);
    this.health = {};
    this.samples = new Map();
    this.lastContact = 0;
    this.running = false;
    this.baselineCursor = 0;
  }
  close() {
    this.db.close();
  }
  get(k) {
    const row = this.db.prepare('SELECT v FROM kv WHERE k=?').get(this.binding + '/' + k);
    return row && JSON.parse(row.v);
  }
  put(k, v) {
    this.db
      .prepare('INSERT INTO kv VALUES (?,?) ON CONFLICT(k) DO UPDATE SET v=excluded.v')
      .run(this.binding + '/' + k, JSON.stringify(v));
  }
  async sample(t, signal) {
    const key = t.kind + '/' + t.name;
    const saved = this.get(key);
    try {
      const b = await this.provider.baseline(t, signal);
      if (b && b.id === this.samples.get(key) && saved?.id !== b.id) this.put(key, b);
      this.samples.set(key, b?.id);
      this.health[key] = saved || (b && this.get(key)) ? 'ready' : 'pending';
    } catch {
      this.samples.delete(key);
      this.health[key] = saved ? 'ready' : 'unavailable';
    }
  }
  attemptExpiry(caseId) {
    if (!caseId.includes('.')) return null; // Legacy IDs have no verifiable creation date.
    const match = /^([0-9a-z]{8,10})\.[A-Za-z0-9_-]{32}$/.exec(caseId);
    const created = match && parseInt(match[1], 36);
    const expires = created + 90 * 86400000;
    ensure(created && created <= Date.now() + 60000 && expires > Date.now(), '事件写入窗口已过期');
    return expires;
  }
  async handle(job, stop) {
    const response = { id: job.id, lease: job.lease, connectorId: this.id };
    try {
      const remaining = Date.parse(job.expiresAt) - Date.now();
      ensure(
        remaining > 0 &&
          remaining <= 90000 &&
          job.generation === this.cfg.generation &&
          job.owner === this.id &&
          job.lease &&
          job.id,
        '任务租约无效',
      );
      const leaseSignal = AbortSignal.timeout(Math.ceil(remaining));
      const signal = AbortSignal.any([stop, leaseSignal]);
      signal.throwIfAborted();
      if (job.action === 'collect')
        return { ...response, ok: true, snapshot: await this.provider.collect(signal) };
      const target = job.action === 'execute' ? job.plan?.target : job.target;
      ensure(inScope(this.scope, target) && target.uid, '目标超出本地授权');
      if (job.action === 'inspect') {
        ensure(['workload', 'events', 'logs', 'endpoints'].includes(job.check), '未知只读工具');
        return {
          ...response,
          ok: true,
          evidence: await this.provider.inspect(target, job.check, signal),
        };
      }
      if (job.action === 'verify') {
        for (let i = 0; i < 12; i++) {
          if (await this.provider.verify(target, signal)) {
            await sleep(this.delay, undefined, { signal });
            if (await this.provider.verify(target, signal))
              return { ...response, ok: true, detail: '连续两次核验对象身份、就绪状态和业务访问成功' };
          }
          await sleep(this.delay, undefined, { signal });
        }
        return { ...response, ok: false, detail: '恢复验证未通过' };
      }
      ensure(['plan', 'execute'].includes(job.action), '未知任务');
      const baseline = this.get(target.kind + '/' + target.name);
      const fresh = await this.provider.plan(target, job.type, baseline, signal);
      if (job.action === 'plan') return { ...response, ok: true, plan: fresh };
      ensure(
        typeof job.caseId === 'string' &&
          job.caseId.length > 0 &&
          job.caseId.length <= 128 &&
          (!job.automatic || this.scope.automatic) &&
          fresh &&
          hashPlan(fresh) === job.planHash &&
          hashPlan(job.plan) === job.planHash,
        '写入计划已改变或超出本地授权',
      );
      const expires = this.attemptExpiry(job.caseId);
      this.db.prepare('DELETE FROM attempts WHERE expires <= ?').run(Date.now());
      const exists = this.db.prepare('SELECT 1 FROM attempts WHERE binding=? AND incident=?');
      ensure(!exists.get(this.binding, job.caseId), '该事件已有写入记录');
      const record = () => {
        signal.throwIfAborted();
        ensure(Date.now() < Date.parse(job.expiresAt), '任务租约已过期');
        if (expires) ensure(Date.now() < expires, '事件写入窗口已过期');
        ensure(
          this.db.prepare('SELECT count(*) n FROM attempts').get().n < 40000,
          '写入日志已达容量上限，请核查',
        );
        ensure(
          this.db
            .prepare('INSERT OR IGNORE INTO attempts(binding,incident,expires) VALUES (?,?,?)')
            .run(this.binding, job.caseId, expires).changes === 1,
          '该事件已有写入记录',
        );
      };
      await this.provider.apply(fresh, signal, record, leaseSignal);
      return { ...response, ok: true, detail: '已提交受限变更' };
    } catch (e) {
      return {
        ...response,
        ok: false,
        ...(e.code === 'preflight_failed' ? { preflightFailed: true } : {}),
        error: String(e.message).slice(0, 500),
      };
    }
  }
  async post(path, data, signal) {
    const res = await fetch(this.root + '/connector/' + path, {
      method: 'POST',
      redirect: 'error',
      headers: { 'Content-Type': 'application/json', Authorization: 'Bearer ' + this.cfg.token },
      body: JSON.stringify(data),
      signal: AbortSignal.any([signal, AbortSignal.timeout(8000)]),
    });
    if (!res.ok) {
      await res.body?.cancel();
      throw new Problem('平台返回 HTTP ' + res.status, res.status);
    }
    const limit = 1024 * 1024;
    if (Number(res.headers.get('content-length')) > limit) {
      await res.body?.cancel();
      throw new Problem('平台响应过大');
    }
    ensure(res.body, '平台响应为空');
    const chunks = [];
    let bytes = 0;
    for await (const part of res.body) {
      bytes += part.byteLength;
      ensure(bytes <= limit, '平台响应过大');
      chunks.push(part);
    }
    const value = JSON.parse(Buffer.concat(chunks, bytes).toString('utf8'));
    this.lastContact = Date.now();
    return value;
  }
  async deliver(signal) {
    const out = this.get('outbox');
    if (!out) return;
    if (Date.parse(out.expiresAt) > Date.now()) {
      try {
        await this.post('result', out.result, signal);
      } catch (e) {
        if (e.status !== 409) throw e;
      }
    }
    this.put('outbox', null);
  }
  registration() {
    return {
      id: this.id,
      provider: this.kind,
      generation: this.cfg.generation,
      namespace: this.scope.namespace,
      baselines: this.health,
    };
  }
  async cycle(stop) {
    await this.deliver(stop);
    const { job } = await this.post(
      'poll',
      { protocol: 3, heartbeat: true, wait: true, connection: this.registration() },
      stop,
    );
    if (job) {
      const result = await this.handle(job, stop);
      this.put('outbox', { result, expiresAt: job.expiresAt });
      // SIGTERM stops new jobs, but an already-submitted result gets its own deadline.
      await this.deliver(AbortSignal.timeout(6500));
      return true;
    }
  }
  async run(stop, interval = 1500) {
    ensure(!this.running, '执行端已在运行');
    this.running = true;
    const sampleLoop = async () => {
      while (!stop.aborted) {
        const t = this.scope.targets[this.baselineCursor++ % this.scope.targets.length];
        await this.sample(t, stop);
        await sleep(2000, undefined, { signal: stop }).catch(() => {});
      }
    };
    const heartbeatLoop = async () => {
      while (!stop.aborted) {
        try {
          await this.post(
            'heartbeat',
            { protocol: 3, heartbeat: true, connection: this.registration() },
            stop,
          );
        } catch {
          /* Poll reports connectivity errors; the heartbeat never leases a job. */
        }
        await sleep(4000, undefined, { signal: stop }).catch(() => {});
      }
    };
    const sampling = sampleLoop(),
      heartbeats = heartbeatLoop();
    let wait = interval;
    try {
      while (!stop.aborted) {
        try {
          const started = Date.now();
          const worked = await this.cycle(stop);
          wait = worked ? 0 : Math.max(0, interval - (Date.now() - started));
        } catch (e) {
          if (!stop.aborted) console.warn('Connector:', e.message);
          wait = Math.min(5000, Math.max(interval, wait * 2));
        }
        await sleep(wait, undefined, { signal: stop }).catch(() => {});
      }
    } finally {
      await Promise.all([sampling, heartbeats]);
      this.running = false;
    }
  }
}
export async function listenHealth(worker, signal, rawPort = '18086') {
  const port = Number(rawPort);
  ensure(Number.isInteger(port) && port >= 1 && port <= 65535, 'CONNECTOR_HEALTH_PORT 需要为 1–65535');
  const health = http.createServer((req, res) => {
    const live = worker.running && !signal.aborted;
    const ready = live && Date.now() - worker.lastContact < 16000;
    res.writeHead((req.url === '/live' ? live : req.url === '/ready' && ready) ? 200 : 503).end();
  });
  let warned = false;
  while (!signal.aborted) {
    try {
      await new Promise((resolve, reject) => {
        const failed = (error) => {
          health.off('listening', bound);
          reject(error);
        };
        const bound = () => {
          health.off('error', failed);
          resolve();
        };
        health.once('error', failed);
        health.once('listening', bound);
        health.listen(port, '127.0.0.1');
      });
      health.on('error', (error) => console.error('健康检查监听异常：', error.message));
      return health;
    } catch (error) {
      if (error.code !== 'EADDRINUSE') throw error;
      if (!warned)
        console.warn('健康检查端口 ' + port + ' 已被占用，等待重试。可设置 CONNECTOR_HEALTH_PORT。');
      warned = true;
      await sleep(5000, undefined, { signal }).catch(() => {});
    }
  }
  return null;
}
if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  const cfg = JSON.parse(readFileSync(process.env.CONNECTOR_CONFIG || 'connector.json', 'utf8'));
  const worker = new Connector(cfg, process.env.CONNECTOR_DATA || 'data/connector');
  const stop = new AbortController();
  for (const sig of ['SIGINT', 'SIGTERM']) process.once(sig, () => stop.abort());
  let health;
  try {
    health = await listenHealth(worker, stop.signal, process.env.CONNECTOR_HEALTH_PORT);
    if (health && !stop.signal.aborted) await worker.run(stop.signal);
  } catch (error) {
    console.error('Connector 启动失败：', error.message);
    process.exitCode = 1;
  } finally {
    health?.close();
    worker.close();
  }
}
