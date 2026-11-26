import { actions, digest, ensure, hashPlan, id, inScope, now, Problem, stable } from './common.js';
import { publicEvidence } from './evidence.js';
import { Model } from './model.js';
import { search } from './knowledge.js';
import { Dispatch } from './dispatch.js';

export class Pipeline {
  constructor(db, { trusted = '', modelFactory = (cfg) => new Model(cfg, trusted) } = {}) {
    this.db = db;
    this.modelFactory = modelFactory;
    this.bus = new Dispatch();
    this.work = new Map();
    this.cursor = new Map();
    this.pending = new Set();
    for (const { id: ws } of db.db.prepare('SELECT id FROM spaces').all())
      for (const c of db.list(ws, 'case')) {
        if (['executing', 'verifying', 'reviewing', 'diagnosing'].includes(c.incident.status))
          this.step(ws, c, 'manual', '上次进程中断', '请核查现场，未重新派发写入');
      }
  }
  current(ws) {
    return this.db.config(ws);
  }
  step(ws, c, status, title, detail = '') {
    const i = c.incident;
    i.status = status;
    i.timeline.push({ at: now(), status, title, detail });
    i.timeline = i.timeline.slice(-100);
    this.db.tx(() => {
      this.db.put(ws, 'case', i.id, c);
      this.db.event(ws, status, title + ' · ' + i.target.name);
    });
  }
  launch(ws, fn) {
    ensure(!this.bus.closed && !this.work.has(ws), '已有任务运行中或平台正在停止', 409);
    const ctrl = new AbortController();
    const signal = AbortSignal.any([ctrl.signal, AbortSignal.timeout(8 * 60000)]);
    const task = Promise.resolve()
      .then(() => fn(signal))
      .catch((e) => {
        this.db.put(ws, 'system', 'lastError', String(e.message).slice(0, 600));
        this.db.event(ws, 'stopped', String(e.message).slice(0, 600));
      })
      .finally(() => {
        this.work.delete(ws);
      });
    this.work.set(ws, { ctrl, task });
    return { accepted: true };
  }
  async close() {
    this.bus.close();
    for (const { ctrl } of this.work.values()) ctrl.abort();
    await Promise.all([...this.work.values()].map((x) => x.task));
  }
  activeCases(ws) {
    return this.db.list(ws, 'case');
  }
  view(ws) {
    const c = this.current(ws),
      { apiKey, webhookToken, connectorToken, ...config } = c;
    const connection = this.bus.connections.get(ws) || {
      id: '',
      lastSeen: '',
      namespace: '',
      provider: '',
      generation: 0,
      baselines: {},
    };
    const online = this.bus.online(ws, c),
      baselineReady = c.scope.targets.filter(
        (t) => connection.baselines[t.kind + '/' + t.name] === 'ready',
      ).length;
    return {
      version: '0.3.1',
      config: { ...config, apiKeySet: !!apiKey },
      connection,
      online,
      baselineReady,
      ready: !!(c.publicUrl && online && c.modelTested && baselineReady === c.scope.targets.length),
      busy: this.work.has(ws),
      alerts: this.db.list(ws, 'alert'),
      cases: this.activeCases(ws),
      events: this.db.list(ws, 'event'),
      snapshot: this.db.get(ws, 'system', 'snapshot'),
      lastScan: this.db.get(ws, 'system', 'lastScan') || '',
      lastError: this.db.get(ws, 'system', 'lastError') || '',
    };
  }
  ingest(ws, b) {
    ensure(
      b.version === '4' && Array.isArray(b.alerts) && b.alerts.length >= 1 && b.alerts.length <= 100,
      '需要 Alertmanager v4 通知',
    );
    const cfg = this.current(ws);
    ensure(cfg.publicUrl, '请先配置告警接入');
    this.db.tx(() => {
      for (const a of b.alerts) {
        const name = a.labels?.alertname;
        ensure(
          typeof name === 'string' &&
            name.length > 0 &&
            name.length <= 256 &&
            ['firing', 'resolved'].includes(a.status),
          '告警名称或状态无效',
        );
        const key = digest(JSON.stringify([a.fingerprint || stable(a.labels), a.startsAt || '']));
        const old = this.db.get(ws, 'alert', key);
        this.db.put(ws, 'alert', key, {
          id: key,
          name,
          namespace: String(a.labels.namespace || '').slice(0, 253),
          summary: [...String(a.annotations?.summary || '')].slice(0, 1500).join(''),
          status: a.status,
          receivedAt: now(),
          count: (old?.count || 0) + 1,
        });
      }
      this.db.trim(ws, 'alert', 200);
      this.db.event(ws, 'alert.received', '已收到 ' + b.alerts.length + ' 条告警通知');
    });
    this.pending.add(ws);
    return { ok: true };
  }
  observe(ws, c, f) {
    ensure(
      inScope(c.scope, f.target) &&
        f.target.uid &&
        f.target.version &&
        typeof f.type === 'string' &&
        Array.isArray(f.evidence) &&
        f.evidence.length,
      '执行端证据无效',
    );
    const key = JSON.stringify([f.target.uid, f.target.container || '', f.type]);
    let cases = this.activeCases(ws);
    let entry = cases.find(
      (x) => x.generation === c.generation && x.incident.key === key && x.incident.status !== 'resolved',
    );
    if (!entry) {
      if (cases.length >= 150) {
        const done =
          cases.find((x) => x.generation !== c.generation) ||
          cases.find((x) => x.incident.status === 'resolved');
        if (!done) return null;
        this.db.del(ws, 'case', done.incident.id);
      }
      entry = {
        generation: c.generation,
        incident: {
          id: Date.now().toString(36) + '.' + id(),
          key,
          type: f.type,
          title: f.title || f.type,
          severity: f.severity || 'warning',
          status: 'new',
          firstSeen: now(),
          lastSeen: now(),
          occurrences: 0,
          executionCount: 0,
          timeline: [],
        },
      };
    }
    const i = entry.incident;
    i.lastSeen = now();
    i.occurrences++;
    if (i.status === 'new') {
      i.target = f.target;
      i.evidence = f.evidence;
    }
    this.db.put(ws, 'case', i.id, entry);
    return entry;
  }
  async scan(ws, signal) {
    const cfg = this.current(ws),
      ask = (action, args = {}) => this.bus.call(ws, cfg, action, args, signal);
    const { snapshot } = await ask('collect');
    ensure(snapshot && Array.isArray(snapshot.findings), '缺少实时快照');
    for (const f of snapshot.findings) f.evidence = f.evidence.map(publicEvidence);
    const seen = new Set();
    this.db.tx(() => {
      this.db.put(ws, 'system', 'snapshot', snapshot);
      this.db.put(ws, 'system', 'lastScan', now());
      this.db.put(ws, 'system', 'lastError', '');
      for (const f of snapshot.findings) {
        if (inScope(cfg.scope, f.target)) {
          const entry = this.observe(ws, cfg, f);
          if (entry) seen.add(entry.incident.id);
          else this.db.put(ws, 'system', 'lastError', '活动事件已达 150 条，新增发现保留在本次快照');
        }
      }
    });
    const eligible = this.activeCases(ws).filter(
      (c) =>
        c.generation === cfg.generation &&
        ((c.incident.status === 'new' && seen.has(c.incident.id) && cfg.modelTested) ||
          (['manual', 'planned', 'new', 'rejected'].includes(c.incident.status) &&
            !seen.has(c.incident.id))),
    );
    if (!eligible.length) return;
    const offset = (this.cursor.get(ws) || 0) % eligible.length;
    for (let j = 0; j < eligible.length; j++) {
      signal.throwIfAborted();
      const index = (offset + j) % eligible.length;
      this.cursor.set(ws, index + 1);
      const c = eligible[index];
      try {
        if (seen.has(c.incident.id)) await this.diagnose(ws, c, signal);
        else await this.verify(ws, c, signal, false);
      } catch (e) {
        this.step(
          ws,
          c,
          c.incident.status === 'rejected' ? 'rejected' : 'manual',
          '处理已停止',
          String(e.message),
        );
      }
    }
  }
  async diagnose(ws, c, signal) {
    const cfg = this.current(ws),
      i = c.incident;
    ensure(
      inScope(cfg.scope, i.target) && !i.executionCount && c.generation === cfg.generation,
      '事件超出授权范围',
    );
    this.step(ws, c, 'diagnosing', '结合现场证据与知识库诊断');
    const model = this.modelFactory(cfg),
      knowledge = search(this.db, ws, i.type + ' ' + i.title),
      initial = [...i.evidence];
    const checked = new Set(),
      rounds = [];
    let assessment;
    for (let n = 0; n < 3; n++) {
      signal.throwIfAborted();
      assessment = await model.call(
        'assess',
        {
          type: i.type,
          target: i.target,
          evidence: i.evidence,
          knowledge,
          completedChecks: [...checked],
        },
        signal,
      );
      rounds.push(assessment);
      if (assessment.disposition !== 'inspect' || n === 2) break;
      const next = assessment.checks.filter((check) => !checked.has(check)).slice(0, 3);
      if (!next.length) break;
      for (const check of next) {
        const r = await this.bus.call(ws, cfg, 'inspect', { target: i.target, check }, signal);
        ensure(Array.isArray(r.evidence), '补充采集无效');
        for (const e of r.evidence.map(publicEvidence))
          if (!i.evidence.some((v) => v.id === e.id)) i.evidence.push(e);
        checked.add(check);
      }
    }
    c.investigation = {
      engine: model.name,
      knowledge,
      initialEvidence: initial,
      hypotheses: [],
      assessment: rounds[0],
      checks: [...checked],
      followUps: rounds.slice(1),
    };
    i.diagnosis = { engine: model.name, knowledge, ...assessment };
    if (assessment.disposition !== 'consider_plan') {
      this.step(ws, c, 'manual', '证据不足，转人工核查', assessment.conclusion);
      return;
    }
    const { plan } = await this.bus.call(ws, cfg, 'plan', { target: i.target, type: i.type }, signal);
    if (!plan) {
      this.step(ws, c, 'manual', '没有适用的健康基线恢复动作');
      return;
    }
    ensure(this.allowed(cfg, i, plan), '执行端计划超出授权');
    i.plan = plan;
    this.step(ws, c, 'reviewing', '独立模型复核', plan.summary);
    const binding = hashPlan(plan);
    c.review = await model.call(
      'review',
      { type: i.type, target: i.target, evidence: i.evidence, plan, planHash: binding, knowledge },
      signal,
    );
    ensure(c.review.planHash === binding, '复核摘要不匹配');
    c.reviewExpires = new Date(Date.now() + 15 * 60000).toISOString();
    if (c.review.decision === 'reject') {
      this.step(ws, c, 'rejected', '独立复核拒绝执行', c.review.reason);
      return;
    }
    this.step(ws, c, 'planned', '等待执行授权', c.review.reason);
    if (c.review.decision === 'approve' && this.current(ws).scope.automatic)
      await this.execute(ws, c, binding, true, signal);
  }
  allowed(cfg, i, p) {
    return (
      p &&
      cfg.scope.actions.includes(p.action) &&
      actions[p.action] === i.type &&
      p.baselineId &&
      hashPlan(p.target) === hashPlan(i.target) &&
      inScope(cfg.scope, p.target)
    );
  }
  gate(ws, c, hash, automatic) {
    const cfg = this.current(ws),
      i = c.incident;
    ensure(
      c.generation === cfg.generation &&
        !i.executionCount &&
        i.status === 'planned' &&
        c.review &&
        ['approve', 'manual'].includes(c.review.decision) &&
        Date.parse(c.reviewExpires) > Date.now() &&
        this.allowed(cfg, i, i.plan) &&
        hashPlan(i.plan) === hash &&
        c.review.planHash === hash,
      '当前授权、计划或复核已失效',
      409,
    );
    ensure(
      !automatic || (cfg.scope.automatic && c.review.decision === 'approve'),
      '自动修复未获授权',
      409,
    );
    return cfg;
  }
  async execute(ws, c, hash, automatic, signal) {
    const cfg = this.gate(ws, c, hash, automatic),
      i = c.incident;
    const { plan } = await this.bus.call(ws, cfg, 'plan', { target: i.target, type: i.type }, signal);
    ensure(plan && hashPlan(plan) === hash, '现场已改变，原计划失效', 409);
    this.gate(ws, c, hash, automatic);
    const r = await this.bus.call(
      ws,
      cfg,
      'execute',
      { plan, planHash: hash, type: i.type, automatic, caseId: i.id, reviewExpires: c.reviewExpires },
      signal,
      () => {
        this.gate(ws, c, hash, automatic);
        // Commit the reservation before returning the job to the polling Connector.
        const claimed = structuredClone(c);
        claimed.incident.executionCount = 1;
        this.step(ws, claimed, 'executing', '执行端领取任务，已登记一次写入尝试', plan.summary);
        Object.assign(i, claimed.incident);
      },
    );
    if (r.preflightFailed) {
      i.executionCount = 0;
      this.step(ws, c, 'manual', '服务端预检未通过，未提交修改', r.error);
      return;
    }
    ensure(r.ok, '执行端未确认提交成功');
    this.step(ws, c, 'verifying', '独立检查业务恢复');
    await this.verify(ws, c, signal, true);
  }
  async verify(ws, c, signal, afterWrite) {
    const r = await this.bus.call(ws, this.current(ws), 'verify', { target: c.incident.target }, signal);
    if (r.ok) {
      c.incident.resolvedAt = now();
      this.step(ws, c, 'resolved', '业务恢复验证通过', r.detail);
    } else if (afterWrite) this.step(ws, c, 'manual', '恢复验证未通过', r.detail);
    else if (c.incident.timeline.at(-1)?.title !== '外部恢复尚未通过验证')
      this.step(ws, c, c.incident.status, '外部恢复尚未通过验证', r.detail);
  }
  command(ws, key, action, b) {
    const c = this.db.get(ws, 'case', key);
    ensure(c, '事件不存在', 404);
    if (action === 'approve') this.gate(ws, c, b.planHash, false);
    else
      ensure(
        ['manual', 'planned', 'rejected', 'new'].includes(c.incident.status) &&
          !c.incident.executionCount &&
          c.generation === this.current(ws).generation,
        '事件不能重新诊断',
        409,
      );
    return this.launch(ws, async (signal) => {
      try {
        if (action === 'approve') return await this.execute(ws, c, b.planHash, false, signal);
        const cfg = this.current(ws);
        ensure(inScope(cfg.scope, c.incident.target), '目标未获授权', 409);
        const { snapshot } = await this.bus.call(ws, cfg, 'collect', {}, signal);
        ensure(
          Array.isArray(snapshot?.findings) &&
            snapshot.findings.every(
              (f) => f?.target?.uid && f.target.version && Array.isArray(f.evidence),
            ),
          '现场快照不完整',
        );
        const f = snapshot.findings.find(
          (f) =>
            f.type === c.incident.type &&
            f.target.uid === c.incident.target.uid &&
            (f.target.container || '') === (c.incident.target.container || '') &&
            inScope(cfg.scope, f.target),
        );
        if (!f) {
          await this.verify(ws, c, signal, false);
          return;
        }
        c.incident.target = f.target;
        c.incident.evidence = f.evidence.map(publicEvidence);
        delete c.incident.plan;
        delete c.review;
        delete c.reviewExpires;
        await this.diagnose(ws, c, signal);
      } catch (e) {
        this.step(
          ws,
          c,
          c.incident.status === 'rejected' ? 'rejected' : 'manual',
          '处置停止',
          e.message,
        );
      }
    });
  }
}
