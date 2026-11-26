import { actions, ensure, hashPlan, now } from './common.js';
export const scenarios = {
  ImagePullBackOff: 'checkout-api',
  ServiceSelector: 'payment-service',
  ProbeFailure: 'inventory-api',
  CrashLoopBackOff: 'config-worker',
  OOMKilled: 'memory-worker',
  Pending: 'report-worker',
};
export const runbooks = Object.entries(scenarios).map(([type]) => ({
  type,
  title: type,
  conclusion: '依据资源状态和业务探测核实 ' + type,
  checks: ['检查对象身份、事件与最近容器日志', '确认健康基线和业务响应'],
  action: Object.keys(actions).find((a) => actions[a] === type) || '',
  source: '内置排障手册',
}));
export class Demo {
  constructor(ns, read, write) {
    this.ns = ns;
    this.read = read;
    this.write = write;
    if (!this.read()) this.write({ version: 1, faults: {} });
  }
  spec(type) {
    return {
      kind: type === 'ServiceSelector' ? 'Service' : 'Deployment',
      namespace: this.ns,
      name: scenarios[type],
    };
  }
  target(type) {
    const t = this.spec(type);
    return {
      ...t,
      uid: 'lab-' + t.name,
      version: String(this.read().version),
      ...(t.kind === 'Deployment' ? { container: 'app' } : {}),
    };
  }
  inject(fault) {
    ensure(Object.hasOwn(scenarios, fault), '演示类型无效');
    const world = this.read();
    ensure(!world.faults[fault], '演示故障已存在', 409);
    world.faults[fault] = true;
    world.version++;
    this.write(world);
  }
  reset() {
    this.write({ version: this.read().version + 1, faults: {} });
  }
  async baseline(t) {
    const type = Object.keys(scenarios).find((k) => scenarios[k] === t.name);
    return type && !this.read().faults[type] ? { uid: 'lab-' + t.name, id: 'healthy-' + t.name } : null;
  }
  async collect() {
    const findings = [],
      resources = [];
    const faults = this.read().faults;
    for (const [type, name] of Object.entries(scenarios)) {
      const t = this.target(type),
        bad = !!faults[type];
      resources.push({ ...t, status: bad ? 'NotReady' : 'Ready', ready: bad ? 0 : 1, desired: 1 });
      if (bad)
        findings.push({
          type,
          title: type + ' · ' + name,
          target: t,
          severity: 'warning',
          evidence: await this.inspect(t, 'workload'),
        });
    }
    return {
      at: now(),
      cluster: 'demo',
      nodes: 1,
      readyNodes: 1,
      pods: 5,
      readyPods: 5 - findings.filter((f) => f.target.kind === 'Deployment').length,
      services: 1,
      warnings: [],
      resources,
      findings,
    };
  }
  async inspect(t, check) {
    return [
      {
        id: hashPlan([t, check]).slice(0, 16),
        source: '模拟集群 / ' + check,
        summary: t.name,
        detail: '模拟器现场状态 ' + JSON.stringify(this.read().faults),
        at: now(),
      },
    ];
  }
  async plan(t, type, b) {
    const action = runbooks.find((r) => r.type === type)?.action;
    if (
      !action ||
      !b ||
      !this.read().faults[type] ||
      hashPlan(t) !== hashPlan(this.target(type)) ||
      b.uid !== t.uid
    )
      return null;
    return {
      action,
      summary: '恢复演示资源的健康基线',
      before: '模拟故障配置',
      after: '已验证的演示配置',
      target: t,
      baselineId: b.id,
      checks: ['核对版本', '验证健康'],
    };
  }
  async apply(p, signal, beforeCommit = () => {}) {
    const type = actions[p.action],
      world = this.read(),
      fresh = await this.plan(p.target, type, { uid: p.target.uid, id: 'healthy-' + p.target.name });
    ensure(fresh && hashPlan(fresh) === hashPlan(p), '演示版本已改变');
    signal?.throwIfAborted();
    beforeCommit();
    delete world.faults[type];
    world.version++;
    this.write(world);
  }
  async verify(t) {
    const type = Object.keys(scenarios).find((k) => scenarios[k] === t.name);
    return !!type && t.uid === this.target(type).uid && !this.read().faults[type];
  }
}
