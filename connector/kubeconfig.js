import { execFile, spawn } from 'node:child_process';
import { promisify } from 'node:util';
import { ensure } from '../server/common.js';
const run = promisify(execFile);
// Kubeconfig mode is only selected by the standalone process administrator.
// The Connector image uses the projected ServiceAccount API client instead.
export function kubeconfigClient(file, context, namespace, bin = 'kubectl') {
  ensure(file && context && namespace, '单机真实模式需指定 kubeconfig、context 和命名空间');
  return async (path, { patch, text = false, signal } = {}) => {
    let args = [
      '--kubeconfig',
      file,
      '--context',
      context,
      '--namespace',
      namespace,
      '--request-timeout=10s',
    ];
    const u = new URL(path, 'http://kubernetes');
    const [group, version, ns, resource, name, sub] = (() => {
      const m =
        /^\/(api|apis\/(?:apps|discovery\.k8s\.io))\/(v1)\/namespaces\/([a-z0-9-]+)\/([a-z]+)(?:\/([a-z0-9][a-z0-9.-]*))?(?:\/(log))?$/.exec(
          u.pathname,
        );
      return m?.slice(1) || [];
    })();
    ensure(
      u.origin === 'http://kubernetes' &&
        path === u.pathname + u.search &&
        version &&
        ns === namespace &&
        !u.hash,
      '资源不在本地范围',
    );
    const object =
      ((group === 'api' && resource === 'services') ||
        (group === 'apis/apps' && resource === 'deployments')) &&
      name &&
      !sub;
    const list =
      !name &&
      ((group === 'api' && ['pods', 'events'].includes(resource)) ||
        (group === 'apis/apps' && resource === 'replicasets') ||
        (group === 'apis/discovery.k8s.io' && resource === 'endpointslices'));
    const log = group === 'api' && resource === 'pods' && name && sub === 'log';
    const keys = [...u.searchParams.keys()];
    const allowed = patch
      ? ['dryRun']
      : list
        ? ['limit', 'continue', 'labelSelector', 'fieldSelector']
        : log
          ? ['container', 'tailLines', 'previous']
          : [];
    ensure(keys.length === new Set(keys).size && keys.every((k) => allowed.includes(k)), '无效采集参数');
    if (patch) {
      ensure(object && (!keys.length || u.searchParams.get('dryRun') === 'All'), '写入不在授权范围');
      args.push('patch', resource, name, '--type=json', '--patch', JSON.stringify(patch), '-o', 'json');
      if (keys.length) args.push('--dry-run=server');
    } else {
      ensure(object || list || log, '采集不在授权范围');
      args.push('get', '--raw', path);
    }
    if (text && log) {
      // kubectl may emit a very long final line. Keep the tail while reading stdout.
      return await new Promise((resolve, reject) => {
        const child = spawn(bin, args, {
          signal,
          timeout: 15000,
          windowsHide: true,
          stdio: ['ignore', 'pipe', 'ignore'],
        });
        let tail = Buffer.alloc(0);
        child.stdout.on('data', (part) => {
          tail = Buffer.from(Buffer.concat([tail, part]).subarray(-16384));
        });
        child.on('error', reject);
        child.on('close', (code) => {
          if (code === 0) resolve(tail.toString('utf8'));
          else reject(new Error('kubectl 未能读取容器日志'));
        });
      });
    }
    let result;
    try {
      result = await run(bin, args, {
        signal,
        timeout: 15000,
        maxBuffer: 4 * 1024 * 1024,
        windowsHide: true,
      });
    } catch {
      throw Error('kubectl 请求未完成，请检查指定 context 的连接、权限和资源版本');
    }
    return text ? result.stdout : JSON.parse(result.stdout);
  };
}
