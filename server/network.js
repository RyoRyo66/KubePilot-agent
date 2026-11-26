import http from 'node:http';
import https from 'node:https';
import { lookup } from 'node:dns/promises';
import { BlockList, isIP } from 'node:net';
import { ensure, Problem } from './common.js';
const deny = new BlockList(),
  deny6 = new BlockList(),
  privateNet = new BlockList();
for (const [net, bits] of [
  ['0.0.0.0', 8],
  ['127.0.0.0', 8],
  ['169.254.0.0', 16],
  ['100.64.0.0', 10],
  ['192.0.0.0', 24],
  ['198.18.0.0', 15],
  ['224.0.0.0', 4],
  ['240.0.0.0', 4],
])
  deny.addSubnet(net, bits, 'ipv4');
for (const [net, bits] of [
  ['::', 96],
  ['::ffff:0:0', 96],
  ['64:ff9b::', 96],
  ['64:ff9b:1::', 48],
  ['2002::', 16],
  ['2001::', 32],
  ['fe80::', 10],
  ['fec0::', 10],
  ['ff00::', 8],
])
  deny6.addSubnet(net, bits, 'ipv6');
for (const [net, bits] of [
  ['10.0.0.0', 8],
  ['172.16.0.0', 12],
  ['192.168.0.0', 16],
])
  privateNet.addSubnet(net, bits, 'ipv4');
privateNet.addSubnet('fc00::', 7, 'ipv6');
export function permitted(ip, policy, allowedIPs) {
  if (policy === 'admin-local' && (ip === '127.0.0.1' || ip === '::1'))
    return !allowedIPs || allowedIPs.includes(ip);
  const family = isIP(ip) === 4 ? 'ipv4' : 'ipv6';
  const reserved = family === 'ipv4' ? deny : deny6;
  return (
    !!isIP(ip) &&
    !ip.includes('%') &&
    !reserved.check(ip, family) &&
    (!privateNet.check(ip, family) || (policy !== 'public' && allowedIPs?.includes(ip) === true))
  );
}
const temporaryErrors = new Set([
  'EAI_AGAIN',
  'ECONNRESET',
  'ECONNREFUSED',
  'ENETUNREACH',
  'EHOSTUNREACH',
  'ETIMEDOUT',
  'EPIPE',
]);
function transportError(error, deadline, signal) {
  if (signal?.aborted) return signal.reason;
  if (error instanceof Problem) return error;
  const retryable = deadline.aborted || temporaryErrors.has(error?.code);
  return new Problem(
    '外部服务连接失败或超时',
    502,
    retryable ? 'transient_network' : 'connection_failed',
  );
}
// DNS is resolved once. Dial the selected address while preserving TLS SNI and Host.
export async function request(
  url,
  {
    method = 'GET',
    body,
    headers = {},
    policy = 'public',
    allowedIPs,
    timeout = 12000,
    limit = 512 * 1024,
    signal,
  } = {},
) {
  const u = new URL(url),
    host = u.hostname.replace(/^\[|\]$/g, '');
  ensure(
    ['http:', 'https:'].includes(u.protocol) && !u.username && !u.password && !u.hash,
    '出站地址无效',
  );
  const deadline = AbortSignal.timeout(timeout);
  const cancel = signal ? AbortSignal.any([deadline, signal]) : deadline;
  const addresses = isIP(host)
    ? [{ address: host, family: isIP(host) }]
    : await new Promise((ok, no) => {
        const abort = () => no(transportError(cancel.reason, deadline, signal));
        cancel.throwIfAborted();
        cancel.addEventListener('abort', abort, { once: true });
        lookup(host, { all: true, verbatim: true })
          .then(ok, (error) => no(transportError(error, deadline, signal)))
          .finally(() => cancel.removeEventListener('abort', abort));
      });
  ensure(
    addresses.length && addresses.every((a) => permitted(a.address, policy, allowedIPs)),
    '目的地址不在允许的网络范围',
  );
  if (allowedIPs)
    ensure(
      addresses.every((a) => allowedIPs.includes(a.address)),
      '业务域名解析与 Service 地址不符',
    );
  const selected = addresses[0];
  return new Promise((ok, no) => {
    const transport = u.protocol === 'https:' ? https : http;
    const req = transport.request(
      u,
      {
        method,
        headers,
        signal: cancel,
        agent: false,
        autoSelectFamily: false,
        lookup: (_host, options, cb) =>
          options.all ? cb(null, [selected]) : cb(null, selected.address, selected.family),
      },
      (res) => {
        let n = 0;
        const chunks = [];
        res.on('data', (part) => {
          n += part.length;
          if (n > limit) {
            res.destroy(new Problem('响应过大'));
            return;
          }
          chunks.push(part);
        });
        res.on('error', (error) => no(transportError(error, deadline, signal)));
        res.on('end', () =>
          ok({
            status: res.statusCode,
            retryAfter: res.headers['retry-after'],
            body: Buffer.concat(chunks).toString('utf8'),
          }),
        );
      },
    );
    req.on('error', (error) => no(transportError(error, deadline, signal)));
    req.end(body);
  });
}
export function modelEndpoint(raw, trusted = '') {
  let u;
  try {
    u = new URL(raw);
  } catch {
    throw new Problem('模型接口地址无效');
  }
  ensure(!u.username && !u.password && !u.search && !u.hash, '模型地址不能带凭证或查询参数');
  const base = u.href.replace(/\/+$/, '');
  const local = !!trusted && base === trusted.replace(/\/+$/, '');
  ensure(u.protocol === 'https:' || (local && u.protocol === 'http:'), '远程模型需要 HTTPS');
  return { base, policy: local ? 'admin-local' : 'public' };
}
