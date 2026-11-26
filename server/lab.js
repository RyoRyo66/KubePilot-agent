import http from 'node:http';
import { resolve } from 'node:path';
import { pathToFileURL } from 'node:url';
import { readFileSync } from 'node:fs';
import { withRuntimeStore } from './database.js';
import { Demo, runbooks } from './demo.js';
import { Kubernetes } from '../connector/kube.js';
import { kubeconfigClient } from '../connector/kubeconfig.js';
import { Model } from './model.js';
import {
  actions,
  body,
  checkWeb,
  ensure,
  hashPlan,
  headers,
  id,
  inScope,
  json,
  now,
  Problem,
  same,
  scopeOf,
  serve,
} from './common.js';

export function laboratory(options = {}) {
  return withRuntimeStore(options.dir ?? 'data/lab-v3', (db) => createLab(db, options));
}
function createLab(
  db,
  {
    web = 'web/dist',
    ns = 'kube-agent-lab',
    token = '',
    interval = 60,
    mode = 'demo',
    scope,
    provider,
    model = null,
    writes = mode === 'demo',
    publicOrigin = '',
  } = {},
) {
  const owner = 'local-lab';
  db.db.prepare('INSERT OR IGNORE INTO users VALUES (?,?,?)').run(owner, owner, '{}');
  const ws = (db.workspace(owner) || db.createSpace(owner, '本地实验', '')).id;
  const get = (kind, key) => db.get(ws, kind, key),
    put = (kind, key, value) => db.put(ws, kind, key, value),
    list = (kind) => db.list(ws, kind);
  let targets = scope?.targets;
  if (mode === 'demo') {
    provider = new Demo(
      ns,
      () => get('world', 'demo'),
      (v) => put('world', 'demo', v),
    );
    targets = runbooks.map((book) => provider.spec(book.type));
  } else {
    ensure(mode === 'kubernetes' && scope, '真实模式需要授权配置');
    ns = scope.namespace;
    provider ||= new Kubernetes(scope);
  }
  const policies = () =>
    Object.entries(actions).map(
      ([action, type]) =>
        get('policy', action) || {
          action,
          name: type,
          description: '恢复已验证的目标基线',
          risk: '受控',
          enabled: true,
          automatic: false,
        },
    );
  const policy = (a) => policies().find((p) => p.action === a);
  const step = (i, status, title, detail = '') => {
    i.status = status;
    i.timeline.push({ at: now(), status, title, detail });
    i.timeline = i.timeline.slice(-100);
    put('incident', i.id, i);
  };
  const audit = (action, target, detail) => {
    const key = id();
    put('audit', key, {
      id: key,
      at: now(),
      actor: 'operator',
      action,
      target,
      detail,
      result: 'completed',
    });
    db.trim(ws, 'audit', 1000);
  };
  const checks = new Map();
  let occupied = false,
    stopped = false,
    task;
  const pending = new Map();
  const stop = new AbortController();
  function trimTo(kind, max, removable) {
    const rows = list(kind);
    const excess = rows.length - max;
    if (excess <= 0) return;
    const victims = rows.filter(removable).slice(0, excess);
    ensure(victims.length === excess, '活动记录达到上限', 409);
    db.tx(() => {
      for (const row of victims) db.del(ws, kind, row.id);
    });
  }
  const finishedRun = (r) => !['running', 'cancelling', 'awaiting_approval'].includes(r.status);
  const stale = () => {
    for (const r of list('run'))
      if (r.status === 'awaiting_approval' && Date.parse(r.approvalDue) <= Date.now()) {
        r.status = 'needs_attention';
        r.summary = '批准期限已过';
        r.finishedAt = now();
        put('run', r.id, r);
      }
  };
  for (const r of list('run'))
    if (['running', 'cancelling'].includes(r.status)) {
      r.status = 'needs_attention';
      r.summary = '任务因上次进程退出而中断';
      r.finishedAt = now();
      put('run', r.id, r);
    }
  stale();
  const activeRuns = list('run').filter((r) => !finishedRun(r)).length;
  trimTo('run', Math.max(100, activeRuns), finishedRun);
  for (const incident of list('incident')) {
    if (incident.timeline?.length > 100) {
      incident.timeline = incident.timeline.slice(-100);
      put('incident', incident.id, incident);
    }
  }
  async function inspect(trigger, sig) {
    const start = Date.now(),
      snap = await provider.collect(sig);
    for (const t of targets) {
      const key = t.kind + '/' + t.name;
      try {
        const b = await provider.baseline(t, sig);
        if (b && (mode === 'demo' || checks.get(key) === b.id) && get('baseline', key)?.id !== b.id)
          put('baseline', key, b);
        checks.set(key, b?.id);
      } catch (error) {
        checks.delete(key);
        throw error;
      }
    }
    db.tx(() => {
      put('system', 'snapshot', snap);
      for (const f of snap.findings) {
        const key = hashPlan([f.type, f.target.uid, f.target.container || '']);
        let i = list('incident').find((i) => i.key === key && i.status !== 'resolved');
        if (!i) {
          trimTo('incident', 299, (x) => x.status === 'resolved');
          i = {
            id: id(),
            key,
            firstSeen: now(),
            occurrences: 0,
            executionCount: 0,
            timeline: [],
            status: 'new',
          };
        }
        if (i.status === 'new') Object.assign(i, f);
        i.occurrences++;
        i.lastSeen = now();
        put('incident', i.id, i);
      }
      const key = id();
      put('inspection', key, {
        id: key,
        trigger,
        startedAt: new Date(start).toISOString(),
        durationMs: Date.now() - start,
        findings: snap.findings.length,
        status: 'completed',
      });
      db.trim(ws, 'inspection', 300);
    });
  }
  async function diagnose(i, sig, run) {
    const completedChecks = [];
    async function ask(tool, input) {
      if (run) run.modelCalls++;
      const answer = await model.call(tool, { ...input, goal: run?.request.goal, completedChecks }, sig);
      if (run) {
        run.inputTokens += answer.usage?.input || 0;
        run.outputTokens += answer.usage?.output || 0;
      }
      return answer;
    }
    const live = (await provider.collect(sig)).findings.find(
      (f) =>
        f.type === i.type && f.target.uid === i.target.uid && f.target.container === i.target.container,
    );
    ensure(live, '最新采集未复现该事件');
    i.target = live.target;
    i.evidence = live.evidence;
    let d = {
      engine: 'rules',
      conclusion: i.type + ' 的现场状态已采集',
      evidenceIds: i.evidence.map((e) => e.id),
      recommendations: ['核对实时状态和基线'],
      limitations: '规则分析需要人工确认',
      disposition: 'consider_plan',
    };
    if (model) {
      let a = await ask('assess', { target: i.target, type: i.type, evidence: i.evidence });
      const budget = Math.max(0, (run?.request.maxSteps ?? 8) - 3);
      for (let round = 1; a.disposition === 'inspect' && round < 3; round++) {
        const next = [...new Set(a.checks)]
          .filter((k) => !completedChecks.includes(k))
          .slice(0, budget - completedChecks.length);
        const previous = i.evidence.length;
        for (const k of next) {
          const more = await provider.inspect(i.target, k, sig);
          for (const e of more) if (!i.evidence.some((old) => old.id === e.id)) i.evidence.push(e);
          completedChecks.push(k);
        }
        if (i.evidence.length === previous) break;
        a = await ask('assess', { target: i.target, type: i.type, evidence: i.evidence });
      }
      d = { ...a, engine: model.name };
    }
    i.diagnosis = { ...d, at: now() };
    const key = i.target.kind + '/' + i.target.name;
    i.plan =
      d.disposition === 'consider_plan'
        ? await provider.plan(i.target, i.type, get('baseline', key), sig)
        : null;
    delete i.review;
    i.planHash = i.plan ? hashPlan(i.plan) : '';
    i.reviewExpires = i.plan ? new Date(Date.now() + 900000).toISOString() : '';
    if (model && i.plan) {
      const hash = hashPlan(i.plan);
      i.review = await ask('review', {
        evidence: i.evidence,
        target: i.target,
        plan: i.plan,
        planHash: hash,
      });
    }
    step(i, i.plan && i.review?.decision !== 'reject' ? 'planned' : 'manual', '诊断与计划校验完成');
  }
  async function verify(i, sig) {
    const ok = await provider.verify(i.target, sig);
    if (ok) i.resolvedAt = now();
    step(i, ok ? 'resolved' : 'manual', ok ? '已验证业务恢复' : '业务恢复未获验证');
  }
  async function execute(i, hash, automatic, sig) {
    ensure(
      writes &&
        i.status === 'planned' &&
        i.plan &&
        i.planHash === hash &&
        Date.parse(i.reviewExpires) > Date.now() &&
        hashPlan(i.plan) === hash &&
        !i.executionCount &&
        policy(i.plan.action)?.enabled,
      '计划或写入授权无效',
      409,
    );
    if (scope)
      ensure(
        inScope(scope, i.target) &&
          scope.actions.includes(i.plan.action) &&
          (!automatic || scope.automatic),
        '动作超出授权',
      );
    ensure(
      i.review?.decision !== 'reject' &&
        (!automatic ||
          (model &&
            policy(i.plan.action).automatic &&
            i.review?.decision === 'approve' &&
            i.review.planHash === hash)),
      '需要人工核查',
      409,
    );
    const fresh = await provider.plan(
      i.target,
      i.type,
      get('baseline', i.target.kind + '/' + i.target.name),
      sig,
    );
    ensure(fresh && hashPlan(fresh) === hash, '当前计划已改变', 409);
    sig.throwIfAborted();
    try {
      await provider.apply(fresh, sig, () => {
        sig.throwIfAborted();
        ensure(Date.parse(i.reviewExpires) > Date.now(), '计划确认已过期', 409);
        i.executionCount++;
        step(i, 'executing', '已登记一次写入');
      });
      await verify(i, sig);
    } catch (e) {
      step(i, 'manual', '执行或验证中止', e.message);
      throw e;
    }
  }
  const state = () => ({
    revision: db.db.prepare('SELECT total_changes() n').get().n,
    settings: {
      version: '0.3.1',
      mode,
      namespace: ns,
      writesEnabled: writes,
      intervalSeconds: interval,
      modelConfigured: !!model,
      model: model?.name || '',
      agentModel: model?.name || 'rules',
      storage: 'SQLite',
    },
    snapshot: get('system', 'snapshot'),
    incidents: list('incident'),
    agentRuns: list('run'),
    audits: list('audit'),
    experiments: list('experiment'),
    policies: policies(),
    inspections: list('inspection'),
  });
  async function exclusive(fn) {
    ensure(!occupied && !stopped, '已有任务运行中', 409);
    occupied = true;
    try {
      return await fn();
    } finally {
      occupied = false;
    }
  }
  function startRun(r, approved = false) {
    ensure(!occupied && !stopped, '已有任务运行中', 409);
    if (!approved) trimTo('run', 99, finishedRun);
    const ctrl = new AbortController();
    pending.set(r.id, ctrl);
    r.status = 'running';
    put('run', r.id, r);
    const signal = AbortSignal.any([stop.signal, ctrl.signal, AbortSignal.timeout(180000)]);
    task = exclusive(async () => {
      const phase = async (name, fn) => {
        const at = now();
        let failed = false;
        try {
          return await fn();
        } catch (e) {
          failed = true;
          throw e;
        } finally {
          r.steps.push({
            id: id(),
            tool: name,
            status: signal.aborted ? 'interrupted' : failed ? 'failed' : 'completed',
            startedAt: at,
            finishedAt: now(),
            durationMs: Date.now() - Date.parse(at),
          });
        }
      };
      try {
        if (!approved) {
          await phase('inspect_cluster', () => inspect('agent', signal));
          const i = list('incident')
            .reverse()
            .find(
              (x) =>
                x.status !== 'resolved' &&
                x.target.name === r.request.workload &&
                (!r.request.incidentId || x.id === r.request.incidentId),
            );
          if (!i) {
            r.status = 'completed';
            r.summary = '未发现匹配的活动事件';
            return;
          }
          await phase('diagnose', () => diagnose(i, signal, r));
          r.incidentId = i.id;
          r.diagnosis = i.diagnosis;
          r.plan = i.plan;
          r.planHash = i.plan ? hashPlan(i.plan) : '';

          if (!i.plan || i.status !== 'planned') {
            r.status = 'needs_attention';
            r.summary = '请核查证据';
            return;
          }
          if (!writes || r.request.mode === 'observe') {
            r.status = 'completed';
            r.summary = '只读诊断完成';
            return;
          }
          if (
            r.request.mode !== 'automatic' ||
            !model ||
            !policy(i.plan.action)?.automatic ||
            i.review?.decision !== 'approve'
          ) {
            r.status = 'awaiting_approval';
            r.approvalDue = i.reviewExpires;
            r.summary = '请核对计划后确认';
            return;
          }
        }
        const i = get('incident', r.incidentId);
        await phase('execute_and_verify', () => execute(i, r.planHash, !approved, signal));
        r.status = i.status === 'resolved' ? 'recovered' : 'needs_attention';
        r.summary = i.timeline.at(-1).title;
      } catch (e) {
        r.status = ctrl.signal.aborted ? 'cancelled' : 'needs_attention';
        r.summary = e.message;
      } finally {
        if (signal.aborted && r.status === 'awaiting_approval') r.status = 'cancelled';
        if (r.status !== 'awaiting_approval') r.finishedAt = now();
        put('run', r.id, r);
        pending.delete(r.id);
      }
    });
    return r;
  }
  const server = http.createServer(
    { requestTimeout: 20000, headersTimeout: 10000, keepAliveTimeout: 5000 },
    async (req, res) => {
      headers(res);
      try {
        ensure(!stopped, '实验台正在停止', 503);
        const p = new URL(req.url, 'http://localhost').pathname;
        checkWeb(req, publicOrigin);
        if (!p.startsWith('/api/')) return await serve(web, p, res);
        if (req.method === 'GET' && p === '/api/platform/ping')
          return json(res, 404, { mode: 'standalone' });
        if (p === '/api/health' && req.method === 'GET') return json(res, 200, { ok: true });
        ensure(!token || same(req.headers.authorization, 'Bearer ' + token), '需要访问令牌', 401);
        stale();
        if (req.method === 'GET') {
          if (p === '/api/state') return json(res, 200, state());
          if (p === '/api/runbooks') return json(res, 200, runbooks);
          throw new Problem('接口不存在', 404);
        }
        ensure(req.method === 'POST', '请求方法错误', 405);
        const b = await body(req);
        const run = /^\/api\/agent\/runs\/([^/]+)\/(approve|cancel)$/.exec(p);
        if (run) {
          const r = get('run', run[1]);
          ensure(r, '任务不存在', 404);
          if (run[2] === 'cancel') {
            ensure(pending.has(r.id) || r.status === 'awaiting_approval', '任务已经结束', 409);
            if (pending.has(r.id)) pending.get(r.id).abort();
            else {
              r.status = 'cancelled';
              r.finishedAt = now();
              put('run', r.id, r);
            }
          } else {
            ensure(
              r.status === 'awaiting_approval' && r.planHash === b.planHash,
              '确认已过期或计划改变',
              409,
            );
            startRun(r, true);
          }
          return json(res, 200, { ok: true });
        }
        if (p === '/api/agent/runs') {
          ensure(
            b.goal === undefined || (typeof b.goal === 'string' && b.goal.length <= 2000),
            '任务描述过长',
          );
          ensure(
            b.maxSteps === undefined ||
              (Number.isInteger(b.maxSteps) && b.maxSteps >= 3 && b.maxSteps <= 16),
            '工具步数需为 3–16',
          );
          ensure(
            b.namespace === ns &&
              targets.some((t) => t.name === b.workload) &&
              ['observe', 'automatic', 'approval'].includes(b.mode),
            '无效任务目标或模式',
          );
          ensure(
            !list('run').some(
              (r) =>
                r.request.workload === b.workload && ['running', 'awaiting_approval'].includes(r.status),
            ),
            '目标已有待处理任务',
            409,
          );
          const r = {
            id: id(),
            request: b,
            trigger: 'operator',
            engine: model?.name || 'rules',
            startedAt: now(),
            steps: [],
            summary: '正在采集',
            fallback: !model,
            modelCalls: 0,
            inputTokens: 0,
            outputTokens: 0,
            promptHash: hashPlan('lab-v3'),
          };
          return json(res, 200, startRun(r));
        }
        await exclusive(async () => {
          const signal = AbortSignal.any([stop.signal, AbortSignal.timeout(90000)]);
          if (p === '/api/inspections') return inspect('operator', signal);
          if (p === '/api/demo/inject' || p === '/api/demo/reset') {
            ensure(mode === 'demo', '仅在模拟环境可用', 403);
            if (p.endsWith('reset')) provider.reset();
            else {
              provider.inject(b.fault);
              const key = id();
              put('experiment', key, { id: key, at: now(), fault: b.fault, status: 'started', mode });
              db.trim(ws, 'experiment', 200);
            }
            return;
          }
          const pol = /^\/api\/policies\/([^/]+)$/.exec(p);
          if (pol) {
            const next = policy(pol[1]);
            ensure(next, '未知策略');
            next.enabled = b.enabled === true;
            next.automatic = next.enabled && b.automatic === true;
            put('policy', next.action, next);
            return;
          }
          const inc = /^\/api\/incidents\/([^/]+)\/(diagnose|execute|verify)$/.exec(p);
          ensure(inc, '接口不存在', 404);
          const i = get('incident', inc[1]);
          ensure(i, '事件不存在', 404);
          if (inc[2] === 'diagnose') {
            ensure(!i.executionCount && i.status !== 'resolved', '事件不能重新诊断', 409);
            return diagnose(i, signal);
          }
          if (inc[2] === 'verify') return verify(i, signal);
          return execute(i, b.planHash, false, signal);
        });
        audit(p, 'lab', '操作已完成');
        json(res, 200, { ok: true });
      } catch (e) {
        json(res, e.status || 500, {
          error: e instanceof Problem ? e.message : '操作未完成，请核对现场状态',
        });
      }
    },
  );
  server.timeout = 100000;
  async function tick() {
    if (occupied || stopped) return;
    stale();
    await exclusive(() => inspect('scheduler', stop.signal));
    for (const i of list('incident')) {
      if (
        !writes ||
        !model ||
        i.status !== 'new' ||
        !policy(runbooks.find((x) => x.type === i.type)?.action)?.automatic
      )
        continue;
      if (list('run').some((r) => r.incidentId === i.id || r.request.incidentId === i.id)) continue;
      startRun({
        id: id(),
        request: { namespace: ns, workload: i.target.name, incidentId: i.id, mode: 'automatic' },
        trigger: 'scheduler',
        engine: model.name,
        startedAt: now(),
        steps: [],
        fallback: false,
        modelCalls: 0,
        inputTokens: 0,
        outputTokens: 0,
        promptHash: hashPlan('lab-v3'),
      });
      break;
    }
  }
  const timer = setInterval(
    () => {
      task = tick().catch(() => {});
    },
    Math.max(1, interval) * 1000,
  );
  const initial = exclusive(() => inspect('startup', stop.signal));
  return {
    server,
    initial,
    async close() {
      stopped = true;
      clearInterval(timer);
      stop.abort();
      const drained = new Promise((ok) => server.close(ok));
      server.closeIdleConnections();
      await drained;
      await Promise.allSettled([initial, task]);
      while (occupied) await new Promise((ok) => setTimeout(ok, 10));
    },
  };
}
if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  const cfg = process.env.AGENT_SCOPE_FILE
    ? scopeOf(JSON.parse(readFileSync(process.env.AGENT_SCOPE_FILE, 'utf8')).scope)
    : undefined;
  const ollama = process.env.AGENT_OLLAMA_URL || 'http://127.0.0.1:11434';
  const model = process.env.AGENT_LLM_MODEL
    ? new Model(
        {
          baseUrl: process.env.AGENT_LLM_BASE_URL,
          model: process.env.AGENT_LLM_MODEL,
          apiKey: process.env.AGENT_LLM_API_KEY,
        },
        process.env.PLATFORM_TRUSTED_MODEL_URL,
      )
    : process.env.AGENT_OLLAMA_MODEL
      ? new Model({ baseUrl: ollama, model: process.env.AGENT_OLLAMA_MODEL }, ollama, 'ollama')
      : null;
  const env = process.env,
    mode = env.AGENT_MODE || 'demo';
  const host = env.AGENT_HOST || '127.0.0.1';
  ensure(
    ['localhost', '::1', '127.0.0.1'].includes(host) || (env.AGENT_PUBLIC_ORIGIN && env.AGENT_API_TOKEN),
    '外部访问需要令牌和来源地址',
  );
  const provider =
    mode === 'kubernetes' && cfg && env.AGENT_KUBECONFIG
      ? new Kubernetes(
          cfg,
          kubeconfigClient(
            env.AGENT_KUBECONFIG,
            env.AGENT_KUBE_CONTEXT,
            cfg.namespace,
            env.KUBECTL || 'kubectl',
          ),
        )
      : undefined;
  const lab = laboratory({
    dir: env.AGENT_DATA_DIR,
    web: env.AGENT_WEB_DIR,
    token: env.AGENT_API_TOKEN,
    mode,
    scope: cfg,
    provider,
    model,
    writes: mode === 'demo' || env.AGENT_ENABLE_WRITES === 'true',
    publicOrigin: env.AGENT_PUBLIC_ORIGIN,
  });
  try {
    await lab.initial;
  } catch (error) {
    await lab.close();
    throw error;
  }
  lab.server.listen(Number(env.PORT || 18080), host, () =>
    console.log('KubePilot 实验台已启动', lab.server.address()),
  );
  for (const sig of ['SIGINT', 'SIGTERM']) process.once(sig, () => lab.close());
}
