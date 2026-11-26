import { ensure, id, now, same, Problem } from './common.js';
// Leases are process-local. A restarted platform never reissues a pending write.
export class Dispatch {
  constructor() {
    this.connections = new Map();
    this.jobs = new Map();
    this.waiters = new Map();
    this.closed = false;
  }
  online(ws, c) {
    const v = this.connections.get(ws);
    return (
      !!v &&
      v.generation === c.generation &&
      Date.now() - Date.parse(v.lastSeen) < (v.heartbeat ? 16000 : 90000)
    );
  }
  touch(ws, c, b) {
    const v = b.connection;
    ensure(
      b.protocol === 3 &&
        v?.generation === c.generation &&
        c.generation > 0 &&
        v.namespace === c.scope.namespace &&
        typeof v.id === 'string' &&
        v.id.length >= 1 &&
        v.id.length <= 128 &&
        ['kubernetes', 'demo'].includes(v.provider),
      'Connector 版本或范围不匹配',
      409,
    );
    const old = this.connections.get(ws);
    const active = this.jobs.get(ws);
    ensure(!active || active.job.owner === v.id, '已有任务绑定原执行端', 409);
    ensure(!this.online(ws, c) || old.id === v.id, '已有在线执行端', 409);
    const baselines = Object.fromEntries(
      c.scope.targets.map((t) => {
        const k = t.kind + '/' + t.name;
        return [k, String(v.baselines?.[k] || 'pending').slice(0, 200)];
      }),
    );
    this.connections.set(ws, {
      id: v.id,
      provider: v.provider,
      namespace: v.namespace,
      generation: v.generation,
      lastSeen: now(),
      heartbeat: b.heartbeat === true,
      baselines,
    });
  }
  poll(ws, c, b, fresh = () => c, signal) {
    this.touch(ws, c, b);
    const lease = () => this.lease(ws, fresh(), b.connection);
    if (signal?.aborted || this.closed) return {};
    const first = lease();
    if (!b.wait || first.job || this.closed || signal?.aborted) return first;
    ensure(!this.waiters.has(ws), '已有挂起的轮询', 409);
    return new Promise((resolve, reject) => {
      let done = false;
      const finish = (deliver = false) => {
        if (done) return;
        done = true;
        clearTimeout(timer);
        signal?.removeEventListener('abort', cancel);
        const ownsWaiter = this.waiters.get(ws) === finish;
        if (ownsWaiter) this.waiters.delete(ws);
        try {
          resolve(deliver && ownsWaiter ? lease() : {});
        } catch (error) {
          reject(error);
        }
      };
      const cancel = () => finish();
      const timer = setTimeout(cancel, 5000);
      this.waiters.set(ws, finish);
      signal?.addEventListener('abort', cancel, { once: true });
    });
  }
  lease(ws, c, v) {
    const entry = this.jobs.get(ws);
    if (this.closed || this.connections.get(ws)?.id !== v.id || c.generation !== v.generation) return {};
    if (!entry || entry.leased || entry.job.generation !== v.generation || entry.job.owner !== v.id)
      return {};
    if (entry.job.automatic && !c.scope.automatic) {
      entry.finish(new Problem('自动修复已暂停'));
      return {};
    }
    try {
      ensure(Date.now() < Date.parse(entry.job.expiresAt), '租约已过期', 409);
      entry.beforeLease?.();
    } catch (error) {
      entry.finish(error);
      return {};
    }
    entry.leased = true;
    return { job: entry.job };
  }
  pauseAutomatic(ws) {
    const entry = this.jobs.get(ws);
    if (entry?.job.automatic && !entry.leased) entry.finish(new Problem('自动修复已暂停', 409));
  }
  call(ws, c, action, args = {}, signal, beforeLease) {
    ensure(!this.closed && this.online(ws, c), 'Connector 离线，等待下一次巡检', 409);
    ensure(!this.jobs.has(ws), '执行端已有任务', 409);
    signal?.throwIfAborted();
    const start = Date.now();
    const ttl = action === 'execute' ? Math.min(65000, Date.parse(args.reviewExpires) - start) : 65000;
    ensure(ttl > 0, '复核已过期，请重新诊断', 409);
    const job = {
      ...args,
      id: id(),
      lease: id(),
      action,
      owner: this.connections.get(ws).id,
      generation: c.generation,
      expiresAt: new Date(start + ttl).toISOString(),
    };
    return new Promise((resolve, reject) => {
      const abort = () => finish(new Problem('执行已中止，请核对集群状态', 409));
      const timer = setTimeout(() => finish(new Problem('租约超时，请核对实际状态', 504)), ttl);
      let done = false;
      const finish = (err, value) => {
        if (done) return;
        done = true;
        clearTimeout(timer);
        signal?.removeEventListener('abort', abort);
        if (this.jobs.get(ws) === entry) this.jobs.delete(ws);
        err ? reject(err) : resolve(value);
      };
      const entry = { job, leased: false, finish, beforeLease };
      this.jobs.set(ws, entry);
      signal?.addEventListener('abort', abort, { once: true });
      this.waiters.get(ws)?.(true);
    });
  }
  result(ws, c, v) {
    const e = this.jobs.get(ws);
    ensure(
      e?.leased &&
        same(v.id, e.job.id) &&
        same(v.lease, e.job.lease) &&
        v.connectorId === e.job.owner &&
        e.job.generation === c.generation &&
        Date.now() < Date.parse(e.job.expiresAt),
      '租约无效或已结束',
      409,
    );
    const connection = this.connections.get(ws);
    if (connection?.id === e.job.owner && connection.generation === c.generation)
      connection.lastSeen = now();
    if (e.job.action === 'execute' && v.preflightFailed === true && v.error) {
      e.finish(null, { ok: false, preflightFailed: true, error: String(v.error).slice(0, 500) });
      return;
    }
    e.finish(v.error ? new Problem('执行端返回：' + String(v.error).slice(0, 800), 409) : null, v);
  }
  close() {
    this.closed = true;
    for (const cancel of this.waiters.values()) cancel();
    for (const e of this.jobs.values()) e.finish(new Problem('平台正在停止', 503));
  }
}
