// This command is deliberately restricted to the separately created acceptance cluster.
import { execFileSync, spawn } from 'node:child_process';
import { mkdirSync, readFileSync, writeFileSync, existsSync } from 'node:fs';
import { resolve, join } from 'node:path';
import { setTimeout as sleep } from 'node:timers/promises';
import { randomBytes } from 'node:crypto';
import assert from 'node:assert/strict';
import { connectorImage } from '../server/common.js';
import { monitoring } from './monitoring.js';

const cluster = 'kubepilot-v3-accept',
  ns = 'kube-agent-lab',
  mon = 'kubepilot-monitoring';
const [config, artifactPath] = process.argv.slice(2);
if (!config || !artifactPath) throw Error('需要独立 kubeconfig 和私有验收目录');
const bin = process.env.KUBECTL || 'kubectl',
  args = ['--kubeconfig', resolve(config), '--context', 'kind-' + cluster];
const output = resolve(artifactPath);
mkdirSync(output, { recursive: true, mode: 0o700 });
const kubectl = (a, input) =>
  execFileSync(bin, [...args, ...a], {
    input,
    encoding: 'utf8',
    stdio: ['pipe', 'pipe', 'pipe'],
    windowsHide: true,
    timeout: 180000,
    maxBuffer: 8 * 1024 * 1024,
  });
const nodes = JSON.parse(kubectl(['get', 'nodes', '-o', 'json']));
assert.deepEqual(
  nodes.items.map((n) => n.metadata.name),
  [cluster + '-control-plane'],
);
const apply = (manifest) => kubectl(['apply', '-f', '-'], JSON.stringify(manifest));
const get = (kind, name, n = ns) => JSON.parse(kubectl(['-n', n, 'get', kind, name, '-o', 'json']));
const waitDeployment = (name, n = ns) =>
  kubectl(['-n', n, 'rollout', 'status', 'deployment/' + name, '--timeout=150s']);
const img = process.env.ACCEPT_IMAGE || 'kubepilot-v3:accept',
  port = Number(process.env.ACCEPT_PORT || 18183),
  root = 'http://127.0.0.1:' + port;
const modelURL = 'http://model-fixture.' + ns + '.svc:18083/v1',
  source = 'http://platform.' + ns + '.svc:18081';
const scope = {
  namespace: ns,
  image: connectorImage(process.env.ACCEPT_CONNECTOR_IMAGE || img),
  targets: [{ kind: 'Service', name: 'demo-web', probeUrl: 'http://demo-web.' + ns + '.svc/' }],
  actions: ['restore_selector'],
  automatic: true,
};
const obj = (kind, name, spec, apiVersion = 'v1') => ({
  apiVersion,
  kind,
  metadata: { name, namespace: ns },
  ...spec,
});
function deployment(name, cmd, env, volumes, mounts) {
  const labels = { app: name };
  return obj(
    'Deployment',
    name,
    {
      spec: {
        replicas: 1,
        strategy: { type: 'Recreate' },
        selector: { matchLabels: labels },
        template: {
          metadata: { labels },
          spec: {
            automountServiceAccountToken: false,
            securityContext: {
              runAsUser: 1000,
              runAsNonRoot: true,
              fsGroup: 1000,
              seccompProfile: { type: 'RuntimeDefault' },
            },
            volumes,
            containers: [
              {
                name,
                image: img,
                imagePullPolicy: 'Never',
                command: cmd,
                env: Object.entries(env).map(([name, value]) => ({ name, value })),
                volumeMounts: mounts,
                securityContext: {
                  readOnlyRootFilesystem: true,
                  allowPrivilegeEscalation: false,
                  capabilities: { drop: ['ALL'] },
                },
              },
            ],
          },
        },
      },
    },
    'apps/v1',
  );
}
const svc = (name, port) =>
  obj('Service', name, { spec: { selector: { app: name }, ports: [{ port, targetPort: port }] } });
async function until(label, fn, seconds = 120) {
  const end = Date.now() + seconds * 1000;
  let last;
  do {
    try {
      const v = await fn();
      if (v) return v;
    } catch (e) {
      last = e;
    }
    await sleep(1000);
  } while (Date.now() < end);
  throw Error(label + ' 超时' + (last ? '，' + last.message : ''));
}
function results() {
  return JSON.parse(
    kubectl([
      'get',
      '--raw',
      '/api/v1/namespaces/' + mon + '/services/prometheus:9090/proxy/api/v1/query?query=probe_success',
    ]),
  ).data.result;
}
const response = () =>
  kubectl(['get', '--raw', '/api/v1/namespaces/' + ns + '/services/demo-web:80/proxy/']).trim();
const endpoints = () =>
  JSON.parse(
    kubectl([
      '-n',
      ns,
      'get',
      'endpointslices',
      '-l',
      'kubernetes.io/service-name=demo-web',
      '-o',
      'json',
    ]),
  )
    .items.flatMap((i) => i.endpoints || [])
    .filter((e) => e.conditions?.ready).length;
console.log('核对独立节点后部署验收组件');
kubectl(['apply', '-f', 'deploy/lab.yaml']);
apply(svc('model-fixture', 18083));
apply({
  apiVersion: 'v1',
  kind: 'List',
  items: [
    obj('PersistentVolumeClaim', 'platform-data', {
      spec: { accessModes: ['ReadWriteOnce'], resources: { requests: { storage: '128Mi' } } },
    }),
    obj('ConfigMap', 'test-model', {
      data: { 'fixture.mjs': readFileSync('test/model-fixture.js', 'utf8') },
    }),
    deployment(
      'platform',
      ['node', 'server/main.js'],
      {
        PLATFORM_HOST: '0.0.0.0',
        PLATFORM_ALLOW_HTTP: 'true',
        PLATFORM_PUBLIC_ORIGIN: root,
        PLATFORM_TRUSTED_MODEL_URL: modelURL,
        PLATFORM_TRUSTED_MODEL_IPS: get('service', 'model-fixture').spec.clusterIP,
        PLATFORM_INTERVAL_SECONDS: '3600',
        PLATFORM_DATA_DIR: '/data',
      },
      [{ name: 'data', persistentVolumeClaim: { claimName: 'platform-data' } }],
      [{ name: 'data', mountPath: '/data' }],
    ),
    svc('platform', 18081),
    deployment(
      'model-fixture',
      ['node', '--preserve-symlinks-main', '/fixture/fixture.mjs'],
      {},
      [{ name: 'fixture', configMap: { name: 'test-model' } }],
      [{ name: 'fixture', mountPath: '/fixture', readOnly: true }],
    ),

  ],
});
for (const n of ['demo-web', 'platform', 'model-fixture']) waitDeployment(n);
const pf = spawn(
  bin,
  [...args, '-n', ns, 'port-forward', '--address', '127.0.0.1', 'svc/platform', port + ':18081'],
  { stdio: ['ignore', 'pipe', 'pipe'], windowsHide: true },
);
let pfError = '';
pf.stderr.on('data', (b) => (pfError += b));
let cookie = '',
  injected = false,
  original,
  caseId;
const accountFile = join(output, 'acceptance-account.json'),
  record = {
    cluster,
    namespace: ns,
    model: '本地确定性工具调用夹具',
    startedAt: new Date().toISOString(),
  };
const reportPath = join(output, 'acceptance-' + Date.now() + '.json');
async function api(path, payload) {
  const r = await fetch(root + '/api' + path, {
    method: payload === undefined ? 'GET' : 'POST',
    headers: { cookie, 'Content-Type': 'application/json', 'X-Requested-With': 'KubePilot' },
    signal: AbortSignal.timeout(60000),
    body: payload === undefined ? undefined : JSON.stringify(payload),
  });
  const v = await r.json();
  if (!r.ok) throw Error(path + ' ' + r.status + ' ' + (v.error || ''));
  if (r.headers.has('set-cookie')) cookie = r.headers.get('set-cookie').split(';')[0];
  return v;
}
try {
  await until(
    '平台端口转发',
    async () => {
      if (pf.exitCode !== null) throw Error(pfError);
      return (await api('/platform/ping')).product === 'kubepilot-platform';
    },
    20,
  );
  let acct;
  if (existsSync(accountFile)) {
    acct = JSON.parse(readFileSync(accountFile, 'utf8'));
    await api('/auth/login', acct);
  } else {
    acct = {
      username: 'accept-' + randomBytes(4).toString('hex'),
      password: randomBytes(24).toString('base64url'),
    };
    await api('/auth/register', { ...acct, confirmation: acct.password });
    writeFileSync(accountFile, JSON.stringify(acct, null, 2), { mode: 0o600, flag: 'wx' });
  }
  if (!(await api('/auth/session')).workspace)
    await api('/workspace/create', {
      name: '独立 kind 验收',
      description: '真实 Kubernetes 和监控，本地模型夹具',
    });
  await until('已有工作空间巡检完成', async () => !(await api('/platform/state')).busy);
  const { config: receiver } = await api('/platform/setup/source', {
    publicUrl: source,
    allowHttp: true,
  });
  await api('/platform/setup/model', {
    baseUrl: modelURL,
    model: 'kind-acceptance-fixture',
    apiKey: '',
  });
  const { manifest } = await api('/platform/setup/connector', scope);
  apply(JSON.parse(manifest));
  waitDeployment('kubepilot-connector');
  console.log('等待 Connector 核验健康基线');
  await until('健康基线', async () => (await api('/platform/state')).ready);
  apply(monitoring(receiver, scope.targets[0].probeUrl, mon));
  for (const n of ['prometheus', 'blackbox', 'alertmanager']) waitDeployment(n, mon);
  await until('Prometheus 健康业务采样', () => results().some((r) => r.value[1] === '1'));
  await until('初始巡检', async () => {
    const s = await api('/platform/state');
    return !s.busy && s.lastScan;
  });
  const before = await api('/platform/state'),
    old = new Set(before.cases.map((c) => c.incident.id));
  original = get('service', 'demo-web');
  assert.deepEqual(original.spec.selector, { app: 'demo-web' });
  record.before = {
    selector: original.spec.selector,
    uid: original.metadata.uid,
    resourceVersion: original.metadata.resourceVersion,
    endpoints: endpoints(),
    http: response(),
  };
  const changed = { app: 'kubepilot-acceptance-mismatch' };
  kubectl([
    '-n',
    ns,
    'patch',
    'svc',
    'demo-web',
    '--type=json',
    '--patch',
    JSON.stringify([
      { op: 'test', path: '/metadata/resourceVersion', value: original.metadata.resourceVersion },
      { op: 'replace', path: '/spec/selector', value: changed },
    ]),
  ]);
  injected = true;
  record.injectedAt = new Date().toISOString();
  console.log('已在独立集群注入选择器偏移，等待真实告警');
  await until('Prometheus 探测失败', () => results().some((r) => r.value[1] === '0'));
  const recovered = await until(
    '告警触发的诊断、复核和恢复',
    async () => {
      const s = await api('/platform/state');
      const c = s.cases.find(
        (c) => !old.has(c.incident.id) && c.incident.target.uid === original.metadata.uid,
      );
      if (c) {
        caseId = c.incident.id;
        if (c.incident.status === 'manual' || c.incident.status === 'rejected') {
          record.stopped = c;
          throw Error('事件停止 ' + c.incident.timeline.at(-1).detail);
        }
        if (c.incident.status === 'resolved') return c;
      }
      return false;
    },
    150,
  );
  assert.equal(recovered.incident.executionCount, 1);
  assert.equal(recovered.review.decision, 'approve');
  record.case = recovered;
  const after = get('service', 'demo-web');
  assert.equal(after.metadata.uid, original.metadata.uid);
  assert.deepEqual(after.spec.selector, original.spec.selector);
  assert.ok(endpoints() > 0);
  assert.equal(response(), 'healthy');
  record.after = {
    uid: after.metadata.uid,
    resourceVersion: after.metadata.resourceVersion,
    selector: after.spec.selector,
    endpoints: endpoints(),
    http: response(),
  };
  const alert = await until('Alertmanager 恢复通知', async () => {
    const s = await api('/platform/state');
    return s.alerts.find(
      (a) =>
        a.name === 'KubePilotBusinessUnavailable' &&
        a.status === 'resolved' &&
        a.count >= 2 &&
        Date.parse(a.receivedAt) >= Date.parse(record.injectedAt),
    );
  });
  record.alert = alert;
  record.prometheus = results().map((r) => ({ job: r.metric.job, value: r.value[1] }));
  record.account = acct.username;
  if(process.env.ACCEPT_REVIEW_GATE==='true'){
    await api('/platform/pause',{});
    const previous = new Set((await api('/platform/state')).cases.map(c=>c.incident.id));
    kubectl(['-n',ns,'patch','svc','demo-web','--type=json','--patch',JSON.stringify([{op:'test',path:'/metadata/resourceVersion',value:after.metadata.resourceVersion},{op:'replace',path:'/spec/selector',value:changed}])]);
    console.log('自动修复已暂停，验证相同故障在批准前保持零写入');
    const planned=await until('等待人工确认',async()=>{const s=await api('/platform/state');return !s.busy && s.cases.find(x=>!previous.has(x.incident.id)&&x.incident.status==='planned');});
    assert.equal(planned.incident.executionCount,0);assert.deepEqual(get('service','demo-web').spec.selector,changed);
    record.manualGate={id:planned.incident.id,beforeApproval:{writes:planned.incident.executionCount,status:planned.incident.status,endpoints:endpoints()}};
    await api('/platform/cases/'+planned.incident.id+'/approve',{planHash:planned.review.planHash});
    const fixed=await until('确认后验证恢复',async()=>{const s=await api('/platform/state');return s.cases.find(x=>x.incident.id===planned.incident.id&&x.incident.status==='resolved');});
    assert.equal(fixed.incident.executionCount,1);assert.equal(response(),'healthy');
    record.manualGate.afterApproval={writes:fixed.incident.executionCount,status:fixed.incident.status,endpoints:endpoints(),http:response()};
    await until('第二次恢复通知',async()=>{const s=await api('/platform/state');return s.alerts.some(a=>a.id!==alert.id&&a.name===alert.name&&a.status==='resolved'&&Date.parse(a.receivedAt)>=Date.parse(planned.incident.firstSeen));});
  }
  record.success = true;
  injected = false;
  console.log('验收通过', caseId, record.after.http);
} finally {
  if (injected && original) {
    const s = get('service', 'demo-web');
    if (
      s.metadata.uid === original.metadata.uid &&
      s.spec.selector?.app === 'kubepilot-acceptance-mismatch'
    ) {
      kubectl([
        '-n',
        ns,
        'patch',
        'svc',
        'demo-web',
        '--type=json',
        '--patch',
        JSON.stringify([
          { op: 'test', path: '/metadata/resourceVersion', value: s.metadata.resourceVersion },
          { op: 'replace', path: '/spec/selector', value: original.spec.selector },
        ]),
      ]);
      record.operatorCleanup = true;
    }
  }
  record.finishedAt = new Date().toISOString();
  writeFileSync(reportPath, JSON.stringify(record, null, 2));
  console.log('验收记录', reportPath);
  pf.kill();
}
