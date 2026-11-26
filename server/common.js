import { createHash, randomBytes, timingSafeEqual } from 'node:crypto';
import { isIP } from 'node:net';
import { resolve, sep } from 'node:path';
import { createReadStream } from 'node:fs';
import { stat, realpath } from 'node:fs/promises';
export const now = () => new Date().toISOString();
export const id = () => randomBytes(24).toString('base64url');
export const digest = (value) => createHash('sha256').update(value).digest('hex');
export function stable(value) {
  if (Array.isArray(value)) return value.map(stable);
  if (value && typeof value === 'object')
    return Object.fromEntries(
      Object.keys(value)
        .sort()
        .map((k) => [k, stable(value[k])]),
    );
  return value;
}
export const hashPlan = (p) => digest(JSON.stringify(stable(p)));
export class Problem extends Error {
  constructor(message, status = 400, code) {
    super(message);
    this.status = status;
    this.code = code;
  }
}
export function ensure(ok, message, status, code) {
  if (!ok) throw new Problem(message, status, code);
}
export function string(v, min, max, label) {
  ensure(typeof v === 'string' && v.trim().length >= min && [...v].length <= max, label + '无效');
  return v.trim();
}
export function same(a, b) {
  return (
    typeof a === 'string' &&
    typeof b === 'string' &&
    timingSafeEqual(Buffer.from(digest(a), 'hex'), Buffer.from(digest(b), 'hex'))
  );
}
export function origin(raw, insecure = false) {
  let u;
  try {
    u = new URL(raw);
  } catch {
    throw new Problem('地址格式错误');
  }
  ensure(
    !u.username &&
      !u.password &&
      !u.search &&
      !u.hash &&
      u.pathname === '/' &&
      (u.protocol === 'https:' || (insecure && u.protocol === 'http:')),
    '请输入完整 HTTPS 地址，本地实验可允许 HTTP',
  );
  return u.origin;
}
export const actions = {
  restore_image: 'ImagePullBackOff',
  restore_selector: 'ServiceSelector',
  restore_probe: 'ProbeFailure',
};
// URL.hostname normalizes DNS case. Match complete in-cluster service names only.
export function probeServiceName(url, namespace) {
  const parsed = new URL(url);
  if (!['http:', 'https:'].includes(parsed.protocol) || parsed.username || parsed.password || parsed.hash) return null;
  const host = parsed.hostname.toLowerCase().replace(/\.$/, '');
  const [service, ns, ...suffix] = host.split('.');
  if (ns !== namespace || !['svc', 'svc.cluster.local'].includes(suffix.join('.'))) return null;
  if (
    service.length > 63 ||
    !/^[a-z0-9](?:[-a-z0-9]*[a-z0-9])?$/.test(service) ||
    service.startsWith('kubepilot-')
  )
    return null;
  return service;
}
export function connectorImage(image) {
  // OCI repository with optional registry port and tag, followed by the immutable manifest digest.
  ensure(typeof image === 'string' && image.length <= 255 &&
    /^[a-z0-9]+(?:[._-]+[a-z0-9]+)*(?::[0-9]+(?=\/))?(?:\/[a-z0-9]+(?:[._-]+[a-z0-9]+)*)*(?::[A-Za-z0-9_][A-Za-z0-9_.-]{0,127})?@sha256:[0-9a-f]{64}$/.test(image) &&
    !/\s/.test(image), 'Connector 镜像必须使用仓库地址和固定摘要 repo@sha256:<64 位小写十六进制>');
  return image;
}
export function scopeOf(s) {
  const dns = /^[a-z0-9](?:[-a-z0-9]*[a-z0-9])?$/;
  const ns = string(s.namespace, 1, 63, '命名空间');
  ensure(
    dns.test(ns) && ns !== 'default' && (!ns.startsWith('kube-') || ns === 'kube-agent-lab'),
    '请选择独立业务命名空间',
  );
  ensure(/^[a-zA-Z0-9][a-zA-Z0-9./_:@-]{0,254}$/.test(string(s.image, 1, 255, '镜像')), '镜像名称无效');
  ensure(
    Array.isArray(s.actions) && s.actions.length && s.actions.every((a) => Object.hasOwn(actions, a)),
    '恢复动作无效',
  );
  ensure(
    Array.isArray(s.targets) && s.targets.length >= 1 && s.targets.length <= 20,
    '需要 1–20 个授权资源',
  );
  const seen = new Set();
  const targets = s.targets.map(({ kind, name, probeUrl }) => {
    ensure(
      ['Deployment', 'Service'].includes(kind) &&
        typeof name === 'string' &&
        name.length <= 253 &&
        name.split('.').every((v) => v.length <= 63 && dns.test(v)) &&
        !name.startsWith('kubepilot-'),
      '资源无效',
    );
    const key = kind + '/' + name;
    ensure(!seen.has(key), '重复资源');
    seen.add(key);
    let u;
    try {
      u = new URL(probeUrl);
    } catch {
      throw new Problem('业务检查地址无效');
    }
    ensure(
      ['http:', 'https:'].includes(u.protocol) && !u.username && !u.password && !u.hash,
      '业务检查地址无效',
    );
    return { kind, name, probeUrl: u.href };
  });
  return {
    namespace: ns,
    image: s.image.trim(),
    automatic: s.automatic === true,
    actions: [...new Set(s.actions)],
    targets,
  };
}
export const inScope = (s, t) =>
  !!t && s.namespace === t.namespace && s.targets.some((x) => x.kind === t.kind && x.name === t.name);
export function json(res, status, value) {
  if (res.destroyed || res.headersSent) return;
  res.writeHead(status, { 'Content-Type': 'application/json; charset=utf-8' });
  res.end(JSON.stringify(value));
}
export async function body(req, limit = 65536) {
  ensure(req.headers['content-type']?.split(';')[0].trim() === 'application/json', '需要 JSON', 415);
  let n = 0;
  const chunks = [];
  for await (const chunk of req) {
    n += chunk.length;
    ensure(n <= limit, '请求过大', 413);
    chunks.push(chunk);
  }
  try {
    const v = JSON.parse(Buffer.concat(chunks));
    ensure(v && typeof v === 'object' && !Array.isArray(v), '需要 JSON 对象');
    return v;
  } catch {
    throw new Problem('无效 JSON');
  }
}
export function headers(res) {
  for (const [k, v] of Object.entries({
    'Cache-Control': 'no-store',
    'X-Content-Type-Options': 'nosniff',
    'X-Frame-Options': 'DENY',
    'Referrer-Policy': 'no-referrer',
  }))
    res.setHeader(k, v);
}
export function checkWeb(req, publicOrigin) {
  const local = /^(?:localhost|127\.0\.0\.1|\[::1\])(?::[0-9]+)?$/i;
  ensure(
    publicOrigin
      ? req.headers.host?.toLowerCase() === new URL(publicOrigin).host
      : local.test(req.headers.host || ''),
    '访问地址不匹配',
    403,
  );
  ensure(req.headers['sec-fetch-site'] !== 'cross-site', '禁止跨站请求', 403);
  const expected = publicOrigin || 'http://' + req.headers.host;
  ensure(!req.headers.origin || req.headers.origin === expected, '请求来源不匹配', 403);
  if (req.method === 'POST')
    ensure(req.headers['x-requested-with'] === 'KubePilot', '缺少应用请求标识', 403);
}
export async function serve(web, pathname, res) {
  let decoded;
  try {
    decoded = decodeURIComponent(pathname === '/' ? '/index.html' : pathname);
  } catch {
    throw new Problem('无效路径编码', 400);
  }
  const root = await realpath(web).catch(() => null);
  ensure(root, '页面资源不存在', 404);
  const file = await realpath(resolve(root, '.' + decoded)).catch(() => null);
  ensure(file && file.startsWith(root + sep), '资源不存在', 404);
  const info = await stat(file).catch(() => null);
  ensure(info?.isFile(), '页面资源不存在', 404);
  const ext = file.split('.').pop(),
    types = {
      html: 'text/html; charset=utf-8',
      js: 'text/javascript',
      css: 'text/css',
      ico: 'image/x-icon',
      svg: 'image/svg+xml',
      png: 'image/png',
    };
  res.writeHead(200, { 'Content-Type': types[ext] || 'application/octet-stream' });
  const stream = createReadStream(file);
  stream.on('error', () => res.destroy());
  stream.pipe(res);
}
export function peer(req, trusted) {
  const remote = req.socket.remoteAddress?.replace(/^::ffff:/, '');
  const real = req.headers['x-real-ip'];
  return trusted.includes(remote) && typeof real === 'string' && isIP(real) ? real : remote;
}
