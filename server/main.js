import http from 'node:http';
import { resolve } from 'node:path';
import { pathToFileURL } from 'node:url';
import { Accounts, Budget } from './auth.js';
import { withRuntimeStore } from './database.js';
import { Pipeline } from './pipeline.js';
import { modelKey } from './model.js';
import { modelEndpoint } from './network.js';
import { addDoc, docs, search } from './knowledge.js';
import { manifest, webhook } from './manifest.js';
import {
  body,
  checkWeb,
  ensure,
  headers,
  json,
  origin,
  peer,
  Problem,
  scopeOf,
  serve,
  string,
} from './common.js';

export function application(options = {}) {
  return withRuntimeStore(options.dir ?? 'data/platform-v3', (db) => createPlatform(db, options));
}
function createPlatform(db, options) {
  const {
    web = 'web/dist',
    publicOrigin: rawOrigin = '',
    closed = false,
    trustedProxy = [],
    trustedModel = '',
    interval = 60000,
  } = options;
  const publicOrigin = rawOrigin ? origin(rawOrigin, options.allowHTTP === true) : '';
  const modelTests = new Set();
  const modelBudget = new Budget(8192);
  const accounts = new Accounts(db, closed, publicOrigin.startsWith('https:')),
    agent = new Pipeline(db, { trusted: trustedModel, modelFactory: options.modelFactory });
  const last = new Map();
  let stopping = false;
  function tick() {
    if (stopping) return;
    for (const [ws, v] of agent.bus.connections) {
      try {
        if (!agent.bus.online(ws, db.config(ws)) && !agent.bus.jobs.has(ws)) {
          agent.bus.connections.delete(ws);
          last.delete(ws);
          continue;
        }
        if (agent.work.has(ws)) continue;
        if (
          Date.now() - (last.get(ws) || 0) > interval ||
          (agent.pending.has(ws) && Date.now() - (last.get(ws) || 0) > 3000)
        ) {
          agent.pending.delete(ws);
          last.set(ws, Date.now());
          agent.launch(ws, (s) => agent.scan(ws, s));
        }
      } catch (error) {
        console.error('Workspace scheduler:', error.name);
      }
    }
  }
  const timer = setInterval(tick, 1000);
  timer.unref();
  const server = http.createServer(
    { requestTimeout: 20000, headersTimeout: 10000 },
    async (req, res) => {
      headers(res);
      try {
        ensure(!stopping, '平台正在停止', 503);
        const url = new URL(req.url, 'http://localhost'),
          p = url.pathname,
          method = req.method;
        if (p.startsWith('/connector/') || p === '/ingest/alertmanager') {
          ensure(method === 'POST', '请求方法错误', 405);
          const kind = p === '/ingest/alertmanager' ? 'webhook' : 'connector';
          const token = /^Bearer ([A-Za-z0-9_-]{25,128})$/.exec(req.headers.authorization || '')?.[1];
          const ws = token && db.machine(token, kind);
          ensure(ws, '接入凭证无效', 401);
          const b = await body(req, kind === 'connector' ? 1024 * 1024 : 256 * 1024);
          ensure(db.machine(token, kind) === ws, '授权已改变', 401);
          const cfg = db.config(ws);
          if (kind === 'webhook') return json(res, 200, agent.ingest(ws, b));
          if (p === '/connector/heartbeat') {
            agent.bus.touch(ws, cfg, b);
            return json(res, 200, { ok: true });
          }
          if (p === '/connector/poll') {
            const cancel = new AbortController();
            res.once('close', () => cancel.abort());
            return json(res, 200, await agent.bus.poll(ws, cfg, b, () => db.config(ws), cancel.signal));
          }
          ensure(p === '/connector/result', '接口不存在', 404);
          agent.bus.result(ws, cfg, b);
          return json(res, 200, { ok: true });
        }
        checkWeb(req, publicOrigin);
        if (!p.startsWith('/api/')) {
          ensure(method === 'GET', '请求方法错误', 405);
          return await serve(web, p, res);
        }
        if (method === 'GET' && p === '/api/platform/ping')
          return json(res, 200, { product: 'kubepilot-platform', version: '0.3.1' });
        ensure(method === 'GET' || method === 'POST', '请求方法错误', 405);
        const b =
          method === 'POST' ? await body(req, p === '/api/platform/knowledge' ? 192 * 1024 : 65536) : {};
        const token = accounts.token(req),
          user = accounts.user(token);
        const recheck = () => ensure(accounts.user(token)?.id === user?.id && !!user, '登录已失效', 401);
        if (!['/api/auth/session', '/api/auth/register', '/api/auth/login'].includes(p)) {
          recheck();
          ensure(
            !req.headers['x-kubepilot-account'] || req.headers['x-kubepilot-account'] === user.id,
            '账号已改变',
            409,
            'session_changed',
          );
        }
        if (p.startsWith('/api/auth/')) {
          const action = p.slice(10);
          ensure(method === (action === 'session' ? 'GET' : 'POST'), '请求方法错误', 405);
          return json(res, 200, await accounts.handle(action, b, peer(req, trustedProxy), req, res));
        }
        const ws = db.workspace(user.id);
        ensure(
          !req.headers['x-kubepilot-workspace'] || ws?.id === req.headers['x-kubepilot-workspace'],
          '工作空间已改变',
          409,
          'session_changed',
        );
        if (p === '/api/workspace/create' && method === 'POST') {
          ensure(!ws, '工作空间已存在', 409);
          return json(res, 200, {
            workspace: db.createSpace(
              user.id,
              string(b.name, 1, 60, '工作空间名称'),
              string(b.description ?? '', 0, 500, '说明'),
            ),
          });
        }
        ensure(ws, '请先创建工作空间', 409, 'workspace_required');
        const key = ws.id;
        if (p === '/api/workspace/update' && method === 'POST') {
          ws.name = string(b.name, 1, 60, '名称');
          ws.description = string(b.description ?? '', 0, 500, '说明');
          db.update(ws);
          return json(res, 200, { workspace: ws });
        }
        if (p === '/api/workspace/complete' && method === 'POST') {
          ensure(agent.view(key).ready, '接入尚未就绪');
          ws.onboardingComplete = true;
          db.update(ws);
          return json(res, 200, { workspace: ws });
        }
        const cfg = db.config(key);
        if (method === 'GET') {
          if (p === '/api/platform/state') return json(res, 200, agent.view(key));
          if (p === '/api/platform/knowledge') return json(res, 200, docs(db, key));
          if (p === '/api/platform/knowledge/search')
            return json(res, 200, search(db, key, string(url.searchParams.get('q'), 1, 500, '检索词')));
          throw new Problem('接口不存在', 404);
        }
        let reply = { ok: true };
        const idle = () => ensure(!agent.work.has(key), '请等待当前任务结束', 409);
        const invalidate = () => {
          for (const c of agent.activeCases(key)) {
            if (c.incident.status === 'planned') {
              delete c.review;
              delete c.reviewExpires;
              agent.step(key, c, 'manual', '配置已改变，请重新诊断');
            }
          }
        };
        switch (p) {
          case '/api/platform/setup/source': {
            idle();
            const next = origin(b.publicUrl, b.allowHttp === true);
            ensure(
              !cfg.generation || next === cfg.publicUrl,
              '已部署的 Connector 需要沿用登记地址',
              409,
            );
            cfg.publicUrl = next;
            cfg.allowHttp = b.allowHttp === true;
            db.config(key, cfg);
            reply = { config: webhook(cfg) };
            break;
          }
          case '/api/platform/setup/source-config':
            ensure(cfg.publicUrl, '请先保存接收地址');
            reply = { config: webhook(cfg) };
            break;
          case '/api/platform/setup/model': {
            idle();
            const { base } = modelEndpoint(b.baseUrl, trustedModel);
            const apiKey = modelKey(b.apiKey ?? '') || (base === cfg.baseUrl ? cfg.apiKey : '');
            ensure(apiKey || !cfg.apiKey || base === cfg.baseUrl, '切换模型地址后请重新填写 API Key');
            const next = {
              ...cfg,
              model: string(b.model, 1, 150, '模型名称'),
              baseUrl: base,
              apiKey,
              modelTested: true,
            };
            ensure(!modelTests.has(key) && modelTests.size < 8, '连接测试进行中，请稍后重试', 429);
            modelBudget.charge(key, 6, 60000);
            modelTests.add(key);
            try {
              await agent.modelFactory(next).call('connection', {}, AbortSignal.timeout(52000));
            } finally {
              modelTests.delete(key);
            }
            recheck();
            idle();
            const fresh = db.config(key);
            ensure(
              fresh.baseUrl === cfg.baseUrl && fresh.model === cfg.model && fresh.apiKey === cfg.apiKey,
              '模型已被其他请求修改，请重试',
              409,
            );
            Object.assign(fresh, { baseUrl: base, model: next.model, apiKey, modelTested: true });
            db.tx(() => {
              db.config(key, fresh);
              invalidate();
              db.event(key, 'model.connected', '工具调用测试通过');
            });
            break;
          }
          case '/api/platform/setup/connector':
            idle();
            ensure(cfg.publicUrl, '请先配置接收地址');
            cfg.scope = scopeOf(b);
            cfg.generation++;
            db.tx(() => {
              db.rotate(key, cfg, 'connector');
              db.config(key, cfg);
              invalidate();
              db.event(key, 'connector.enroll', '已生成新的执行端授权');
            });
            agent.bus.waiters.get(key)?.();
            agent.bus.connections.delete(key);
            reply = { manifest: manifest(cfg) };
            break;
          case '/api/platform/setup/manifest':
            ensure(cfg.generation, '请先登记 Connector');
            reply = { manifest: manifest(cfg) };
            break;
          case '/api/platform/knowledge':
            addDoc(db, key, b);
            break;
          case '/api/platform/scan':
            reply = agent.launch(key, (s) => agent.scan(key, s));
            break;
          case '/api/platform/pause':
            cfg.scope.automatic = false;
            db.config(key, cfg);
            agent.bus.pauseAutomatic(key);
            db.event(key, 'pause', '后续自动写入已暂停');
            break;
          default: {
            const doc = /^\/api\/platform\/knowledge\/([^/]+)\/remove$/.exec(p),
              incident = /^\/api\/platform\/cases\/([^/]+)\/(retry|approve)$/.exec(p);
            if (doc) {
              ensure(db.del(key, 'doc', doc[1]).changes, '仅能删除自己的资料', 404);
            } else if (incident) reply = agent.command(key, incident[1], incident[2], b);
            else throw new Problem('接口不存在', 404);
          }
        }
        json(res, 200, reply);
      } catch (e) {
        if (!(e instanceof Problem)) console.error('request failed:', e.name);
        json(res, e.status || 500, {
          error: e instanceof Problem ? e.message : '服务处理失败',
          code: e instanceof Problem ? e.code : undefined,
        });
      }
    },
  );
  server.timeout = 65000;
  return {
    server,
    db,
    agent,
    async close() {
      stopping = true;
      clearInterval(timer);
      const drained = new Promise((ok) => server.close(ok));
      server.closeIdleConnections();
      await agent.close();
      await drained;
    },
  };
}
if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  const host = process.env.PLATFORM_HOST || '127.0.0.1',
    publicOrigin = process.env.PLATFORM_PUBLIC_ORIGIN || '';
  ensure(
    publicOrigin || ['127.0.0.1', '::1', 'localhost'].includes(host),
    '对外监听需要配置 PLATFORM_PUBLIC_ORIGIN',
  );
  const cadence = Number(process.env.PLATFORM_INTERVAL_SECONDS || 60);
  ensure(
    Number.isInteger(cadence) && cadence >= 10 && cadence <= 86400,
    '巡检周期需要在 10–86400 秒之间',
  );
  const app = application({
    dir: process.env.PLATFORM_DATA_DIR,
    web: process.env.PLATFORM_WEB_DIR,
    publicOrigin,
    interval: cadence * 1000,
    closed: process.env.PLATFORM_REGISTRATION_CLOSED === 'true',
    allowHTTP: process.env.PLATFORM_ALLOW_HTTP === 'true',
    trustedProxy: (process.env.PLATFORM_TRUSTED_PROXIES || '').split(',').filter(Boolean),
    trustedModel: process.env.PLATFORM_TRUSTED_MODEL_URL,
  });
  app.server.listen(Number(process.env.PORT || 18081), host, () =>
    console.log('KubePilot 0.3 平台已启动', app.server.address()),
  );
  let closing = false;
  for (const signal of ['SIGINT', 'SIGTERM'])
    process.on(signal, () => {
      if (!closing) {
        closing = true;
        void app.close();
      }
    });
}
