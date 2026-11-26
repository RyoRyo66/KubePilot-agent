import { digest, ensure, id, string } from './common.js';
const guides = [
  [
    'ImagePullBackOff',
    '镜像拉取失败',
    '检查容器等待原因、镜像标识与仓库访问。仅在镜像偏离已验证的健康基线时恢复镜像。',
  ],
  [
    'ServiceSelector',
    'Service 没有就绪端点',
    '对比 Service selector、Pod 标签与 EndpointSlice 就绪状态。恢复后验证业务请求。',
  ],
  ['ProbeFailure', '容器探针失败', '对照 Deployment 的探针和健康基线。区分应用故障与探针配置改变。'],
  ['OOMKilled', '内存不足', '检查上次容器退出原因、内存限制和负载。退出码 137 单独不能证明 OOM。'],
  ['Pending', 'Pod 无法调度', '检查 PodScheduled 条件、配额、资源请求与调度事件。'],
  ['CrashLoopBackOff', '容器反复退出', '检查上一次退出状态和日志，再检查配置引用、探针与启动参数。'],
  [
    'FailedCreate',
    '副本创建失败',
    '检查 ReplicaSet 的 FailedCreate 事件和资源配额。配额拒绝可能导致集群内没有对应 Pod，应保留控制器事件并人工核实。',
  ],
  [
    'WorkloadUnavailable',
    '工作负载不可用',
    '对照 observedGeneration、可用副本数与 Pod 条件。证据不足时补采事件、日志和端点，记录仍待核查的原因。',
  ],
];
export const builtins = guides.map(([key, title, content]) => ({
  id: 'guide-' + key,
  title: key + ' · ' + title,
  content,
}));
export function docs(db, ws) {
  return [...builtins, ...db.list(ws, 'doc')];
}
export function addDoc(db, ws, b) {
  const title = string(b.title, 1, 100, '标题'),
    content = string(b.content, 1, 32768, '内容');
  ensure(
    Buffer.byteLength(content) <= 32768 && db.count(ws, 'doc') < 100,
    '资料容量已满或内容过大',
  );
  const key = 'doc-' + id();
  db.put(ws, 'doc', key, { id: key, title, content });
}
function terms(str) {
  const text = str.toLowerCase();
  const words = text.match(/[a-z0-9_-]+/g) || [];
  for (const group of text.match(/\p{Script=Han}+/gu) || []) {
    for (let i = 0; i < group.length; i++) words.push(group.slice(i, i + 2));
  }
  return new Set(words);
}
export function search(db, ws, query) {
  const q = terms(query);
  const hits = [];
  for (const doc of docs(db, ws)) {
    const version = digest(doc.content).slice(0, 12);
    let i = 0;
    for (const part of doc.content.match(/[\s\S]{1,700}/gu) || []) {
      const t = terms(doc.title + ' ' + part);
      let score = 0;
      for (const w of q) if (t.has(w)) score++;
      if (score)
        hits.push({
          score,
          documentId: doc.id,
          title: doc.title,
          section: '第 ' + (i + 1) + ' 段',
          version,
          text: part,
        });
      i++;
    }
  }
  return hits
    .sort((a, b) => b.score - a.score)
    .slice(0, 5)
    .map(({ score, ...v }) => v);
}
