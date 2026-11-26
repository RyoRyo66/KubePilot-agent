// Run on the operator workstation. Only install mutates the chosen cluster.
import { execFileSync } from 'node:child_process';
import { readFileSync } from 'node:fs';
import { X509Certificate } from 'node:crypto';
import { resolve } from 'node:path';
import { pathToFileURL } from 'node:url';
import { ensure, hashPlan, connectorImage, scopeOf } from '../server/common.js';
import { approvalFor } from '../connector/policy.js';
import { Kubernetes } from '../connector/kube.js';
import { kubeconfigClient } from '../connector/kubeconfig.js';

export function guardResources(p, ca) {
  const ns = p.namespace, name = 'kubepilot-admission', connector = 'kubepilot-connector';
  const obj = (kind, name, properties, apiVersion = 'v1') => ({
    apiVersion, kind, metadata: { name, namespace: ns }, ...properties,
  });
  const label = { app: name };
  const cm = obj('ConfigMap','kubepilot-approvals',{data:{'policy.json':JSON.stringify(p)}});
  const history = {...cm, immutable:true, metadata:{namespace:ns,name:'kubepilot-approval-'+hashPlan(p)}};
  const service = obj('Service',name,{spec:{selector:label,ports:[{port:443,targetPort:8443}]}});
  const deploy = obj('Deployment',name,{spec:{replicas:1,selector:{matchLabels:label},template:{
    metadata:{labels:label,annotations:{"kubepilot/approval":hashPlan(p),"kubepilot/ca":hashPlan(ca)}},spec:{automountServiceAccountToken:false,
      securityContext:{runAsNonRoot:true,runAsUser:1000,fsGroup:1000,seccompProfile:{type:'RuntimeDefault'}},
      containers:[{name:'admission',image:connectorImage(p.image),command:['node','connector/admission.js'],
        securityContext:{readOnlyRootFilesystem:true,allowPrivilegeEscalation:false,capabilities:{drop:['ALL']}},
        readinessProbe:{httpGet:{scheme:'HTTPS',port:8443,path:'/ready'}},
        volumeMounts:[{name:'tls',mountPath:'/tls',readOnly:true},{name:'approvals',mountPath:'/approvals',readOnly:true}]}],
      volumes:[{name:'tls',secret:{secretName:name+'-tls',defaultMode:288}},
        {name:'approvals',configMap:{name:history.metadata.name}}]}}}},'apps/v1');
  const user = 'system:serviceaccount:'+ns+':'+connector;
  const webhook = {apiVersion:'admissionregistration.k8s.io/v1',kind:'ValidatingWebhookConfiguration',
    metadata:{name:'kubepilot-'+ns},webhooks:[{name:'repair.kubepilot.local',
      admissionReviewVersions:['v1'],sideEffects:'None',failurePolicy:'Fail',matchPolicy:'Equivalent',
      timeoutSeconds:5,clientConfig:{caBundle:ca,service:{namespace:ns,name,path:'/validate',port:443}},
      matchConditions:[{name:'connector-identity',expression:'request.userInfo.username == '+JSON.stringify(user)}],
      rules:[{apiGroups:[''],apiVersions:['v1'],operations:['UPDATE'],resources:['services'],scope:'Namespaced'},
        {apiGroups:['apps'],apiVersions:['v1'],operations:['UPDATE'],resources:['deployments'],scope:'Namespaced'}]}]};
  const rules = [];
  for (const [kind,group,resource] of [['Deployment','apps','deployments'],['Service','','services']]) {
    const names = Object.keys(p.targets).filter(k=>k.startsWith(kind+'/')).map(k=>k.slice(kind.length+1));
    if (names.length && p.actions.some(a => kind === 'Service' ? a === 'restore_selector' : a !== 'restore_selector'))
      rules.push({apiGroups:[group],resources:[resource],resourceNames:names,verbs:['patch']});
  }
  for (const [kind,group] of [['pods',''],['events',''],['replicasets','apps']]) {
    const names = [...new Set(Object.values(p.targets).flatMap(t=>t.reads[kind] || []))];
    if (names.length) rules.push({apiGroups:[group],resources:kind==='pods'?['pods','pods/log']:[kind],resourceNames:names,verbs:['get']});
  }
  const role = obj('Role','kubepilot-approved',{rules},'rbac.authorization.k8s.io/v1');
  const binding = obj('RoleBinding',role.metadata.name,{roleRef:{apiGroup:'rbac.authorization.k8s.io',kind:'Role',name:role.metadata.name},
    subjects:[{kind:'ServiceAccount',name:connector,namespace:ns}]},'rbac.authorization.k8s.io/v1');
  return {history,cm,service,deploy,webhook,role,binding};
}
export async function prepare(cfg, api) {
  const scope = scopeOf(cfg.scope);
  const kube = new Kubernetes(scope,api,async()=>true);
  const targets = {};
  for (const t of scope.targets) {
    const v = await kube.read(t);
    const b = await kube.baseline(t);
    ensure(b && v.target.uid === b.uid, '目标尚未就绪或已重建');
    const events = t.kind === 'Deployment' ? await kube.events(v) : [];
    const names = objects => [...new Set(objects.map(o=>o.metadata.name))].sort();
    targets[t.kind+'/'+t.name] = approvalFor(b,{pods:names(v.pods),replicasets:names(v.rs || []),events:names(events)});
  }
  return {namespace:scope.namespace,generation:cfg.generation,image:connectorImage(scope.image),actions:scope.actions,targets};
}
async function main() {
  const [mode,file,config,context,expected,caFile] = process.argv.slice(2);
  ensure(file && config && context,'用法见 README：prepare 或 install，需要指定 kubeconfig 和 context');
  const read = path => {
    const bytes = readFileSync(path);
    const encoding = bytes[0] === 0xff && bytes[1] === 0xfe ? 'utf16le' : 'utf8';
    return JSON.parse(bytes.toString(encoding).replace(/^\uFEFF/, ''));
  };
  const bin = process.env.KUBECTL || 'kubectl';
  const run = (ns, args, input) => execFileSync(bin,['--kubeconfig',config,'--context',context,'-n',ns,...args],
    {encoding:'utf8', input, timeout:180000, maxBuffer:16*1024*1024});
  if (mode === 'prepare') {
    const secret = read(file).items.find(o=>o.kind==='Secret' && o.metadata.name==='kubepilot-connector');
    const cfg = JSON.parse(secret.stringData['config.json']);
    ensure(cfg.protocol===4,'需要新版登记清单');
    const policy = await prepare(cfg,kubeconfigClient(config,context,cfg.scope.namespace,bin));
    console.error('审批摘要 '+hashPlan(policy));
    console.log(JSON.stringify(policy,null,2));
    return;
  }
  ensure(mode==='install' && expected && caFile,'安装需要已审核的策略摘要和准入 CA 证书');
  const p = read(file);
  ensure(expected===hashPlan(p),'策略与批准的摘要不一致');
  const ns=p.namespace;
  const caPEM=readFileSync(caFile), ca=new X509Certificate(caPEM);
  const cert=new X509Certificate(Buffer.from(run(ns,['get','secret','kubepilot-admission-tls','-o','jsonpath={.data.tls\\.crt}']),'base64'));
  ensure(cert.checkHost('kubepilot-admission.'+ns+'.svc') && cert.verify(ca.publicKey) &&
    Date.parse(cert.validFrom)<=Date.now() && Date.parse(cert.validTo)>Date.now(),'准入服务证书、有效期或 CA 不匹配');
  const r=guardResources(p,caPEM.toString('base64'));
  const apply=o=>run(ns,['apply','-f','-'],JSON.stringify(o));
  // Establish an independent fail-closed guard before granting any write role.
  apply(r.webhook); apply(r.history); apply(r.cm); apply(r.service); apply(r.deploy);
  run(ns,['rollout','status','deployment/kubepilot-admission','--timeout=120s']);
  apply(r.role); apply(r.binding);
  console.log('已安装审批策略 '+expected+'，命名空间 '+ns);
}
if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href)
  main().catch(e=>{console.error(e.message);process.exitCode=1;});
