import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtempSync, mkdirSync, writeFileSync, symlinkSync, linkSync, rmSync, chmodSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { origin, checkTransport, hashPlan, publicPlan } from '../server/common.js';
import { Store } from '../server/database.js';
import { laboratory } from '../server/lab.js';
import { Kubernetes } from '../connector/kube.js';
import { permittedUpdate } from '../connector/admission.js';
import { approved, approvalFor } from '../connector/policy.js';
import { guardResources } from '../ops/authorize.js';
const target={kind:'Deployment',namespace:'app',name:'orders',uid:'u',version:'1',container:'web'};
const image='registry.example/img@sha256:'+'a'.repeat(64);
const token='fixture-private-marker';
const probe={httpGet:{path:'/health',port:80,httpHeaders:[{name:'X-Local',value:token}]}};
const before={kind:'Deployment',metadata:{name:'orders',namespace:'app',uid:'u',resourceVersion:'1'},
  spec:{template:{spec:{containers:[{name:'web',image,readinessProbe:{exec:{command:['check',token]}}}]}}}};
const base={uid:'u',kind:'Deployment',containers:{web:{group:'containers',image,pinned:image,probes:{readinessProbe:probe}}}};
base.id=hashPlan(base);
const record=approvalFor(base,{pods:['orders-pod'],events:[],replicasets:['orders-rs']});
const policy={namespace:'app',generation:1,image,actions:['restore_probe'],targets:{'Deployment/orders':record}};
const after=structuredClone(before);
after.spec.template.spec.containers[0].readinessProbe=probe;
const review={namespace:'app',name:'orders',kind:{kind:'Deployment',group:'apps'},resource:{resource:'deployments'},
  userInfo:{username:'system:serviceaccount:app:kubepilot-connector'},oldObject:before,object:after,operation:'UPDATE'};

test('探针计划公开结构、绑定真实内容，只有现场生成的计划能落到 API', async () => {
  const kube=new Kubernetes({namespace:'app',actions:['restore_probe']},async()=>before);
  kube.read=async()=>({object:before,target});
  const plan=await kube.plan(target,'ProbeFailure',base);
  assert.ok(!JSON.stringify(plan).includes(token));
  assert.match(plan.before,/exec/);
  assert.match(plan.after,/httpGet/);
  const historical=publicPlan({...plan,before:JSON.stringify({readinessProbe:probe}),after:JSON.stringify({startupProbe:probe})});
  assert.ok(!JSON.stringify(historical).includes(token));
  const writes=[];
  kube.api=async (_path,opts)=>{if(opts?.patch) writes.push(opts.patch); return before};
  await assert.rejects(kube.apply(structuredClone(plan)),/现场/);
  await kube.apply(plan);
  assert.equal(writes.length,2);
  assert.deepEqual(writes[1].at(-1).value,probe);
  const changed=structuredClone(base);
  changed.containers.web.probes.readinessProbe.httpGet.httpHeaders[0].value='another-fixture';
  assert.notEqual((await kube.plan(target,'ProbeFailure',changed)).changeId,plan.changeId);
});
test('集群准入逐字段复核批准值，RBAC 仅列出已审批名称', () => {
  assert.equal(permittedUpdate(review,policy),true);
  const concurrent=structuredClone(after); concurrent.spec.replicas=2;
  assert.equal(permittedUpdate({...review,object:concurrent},policy),false);
  assert.equal(permittedUpdate({...review,subResource:'scale'},policy),false);
  assert.equal(permittedUpdate({...review,object:before},policy),false);
  assert.equal(approved(record,base),true);
  assert.equal(approved(record,{...base,uid:'new'}),false);
  assert.equal(approved(undefined,base),false);
  const r=guardResources(policy,'test-ca');
  assert.equal(r.webhook.webhooks[0].failurePolicy,'Fail');
  assert.equal(r.deploy.spec.template.spec.automountServiceAccountToken,false);
  assert.ok(r.role.rules.every(x=>x.resourceNames.length && !x.verbs.includes('list')));
  assert.deepEqual(r.role.rules.find(x=>x.resources.includes('pods/log')).resourceNames,['orders-pod']);
  assert.equal(r.history.immutable,true);
});
test('受保护接口要求 TLS 或回环，外部业务探针登记也必须使用 TLS', async () => {
  for(const url of ['http://example.org','http://10.1.2.3','http://localhost.example.org']) assert.throws(()=>origin(url,true));
  assert.equal(origin('http://[::1]:81',true),'http://[::1]:81');
  const req={socket:{remoteAddress:'192.0.2.40'},headers:{'x-forwarded-proto':'https'}};
  assert.throws(()=>checkTransport(req,'https://example.org'));
  checkTransport(req,'https://example.org',['192.0.2.40']);
  assert.throws(()=>checkTransport(req,'http://127.0.0.1'));
  const k=new Kubernetes({namespace:'app'},async()=>{throw Error('unexpected I/O')},undefined,['http://probe.example']);
  await assert.rejects(k.business({probeUrl:'http://probe.example/'}),/业务探测/);
});
test('实验台拒绝弱令牌，错误令牌受限而有效令牌仍可用', async t=>{
  const dir=mkdtempSync(join(tmpdir(),'kp-labguard-'));
  t.after(()=>rmSync(dir,{recursive:true,force:true}));
  assert.throws(()=>laboratory({dir,token:'short'}),/随机/);
  const token='0123456789abcdefABCDEF0123456789abcd';
  const lab=laboratory({dir,token});
  t.after(()=>lab.close());
  await lab.initial;
  await new Promise(ok=>lab.server.listen(0,'127.0.0.1',ok));
  const url='http://127.0.0.1:'+lab.server.address().port+'/api/state';
  for(let i=0;i<13;i++){
    const r=await fetch(url);assert.equal(r.status,i===12?429:401);await r.arrayBuffer();
  }
  const good=await fetch(url,{headers:{Authorization:'Bearer '+token}});
  assert.equal(good.status,200);await good.arrayBuffer();
  await lab.close();
});
test('持久化层在打开 SQLite 或密钥前拒绝目录链接、硬链接与宽松权限',t=>{
  const parent=mkdtempSync(join(tmpdir(),'kp-storage-'));t.after(()=>rmSync(parent,{recursive:true,force:true}));
  const root=join(parent,'data');mkdirSync(root,{mode:0o700});
  symlinkSync(root,join(parent,'alias'),'junction');
  assert.throws(()=>new Store(join(parent,'alias')),/链接/);
  const outside=join(parent,'ordinary');writeFileSync(outside,Buffer.alloc(32),{mode:0o600});
  linkSync(outside,join(root,'storage.key'));
  assert.throws(()=>new Store(root),/链接/);
  if(process.platform!=='win32'){
    const open=join(parent,'open');mkdirSync(open);chmodSync(open,0o755);
    assert.throws(()=>new Store(open),/权限/);
  }
});
