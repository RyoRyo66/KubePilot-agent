// A loopback-only, disposable demonstration. All Kubernetes operations use -demo.
// Real platform/Connector HTTP and policy checks are retained; the model is a fixture.
import http from 'node:http'
import net from 'node:net'
import { spawn } from 'node:child_process'
import { randomBytes } from 'node:crypto'
import { mkdir, readFile, writeFile } from 'node:fs/promises'
import { fileURLToPath } from 'node:url'
import path from 'node:path'
import { startFixture } from './platform-model-fixture.mjs'

const root=path.resolve(path.dirname(fileURLToPath(import.meta.url)),'../..')
const port=Number(process.env.FLOW_DEMO_PORT||18082), backendPort=Number(process.env.FLOW_DEMO_BACKEND_PORT||18086)
if(![port,backendPort].every(p=>Number.isInteger(p)&&p>1024&&p<65536)||port===backendPort)throw Error('Invalid demo ports')
const base=`http://localhost:${port}`, host=new URL(base).host
const instance=randomBytes(8).toString('hex'), control=randomBytes(32).toString('hex')
const parent=path.join(root,'.work','flow-demo'), dir=path.join(parent,`${Date.now()}-${instance}`), connectorDir=path.join(dir,'connector')
const activeFile=process.env.FLOW_DEMO_ACTIVE_FILE||path.join(parent,'active.json')
const executable=name=>path.join(process.env.FLOW_DEMO_BIN_DIR||path.join(root,'bin'),name+(process.platform==='win32'?'.exe':''))
const username='flow-demo', password=randomBytes(32).toString('base64url')
const scope={namespace:'demo-lab',image:'kubepilot-connector:0.2.0',targets:[{kind:'Service',name:'payment-service',probeUrl:'http://payment-service.demo-lab.svc/health'}],actions:['restore_selector'],automatic:true}
let platform,connector,fixture,proxy,cookie='',owner='',workspace='',config,generation,modelURL,ready=false,closing=false,runBusy=false,current=null
const children=[]
const now=()=>new Date().toISOString()
const sleep=ms=>new Promise(resolve=>setTimeout(resolve,ms))
const steps=[['inject','注入故障'],['alert','接收告警'],['collect','现场采集'],['diagnose','模型诊断'],['review','自动复核'],['execute','受控修复'],['verify','恢复验证'],['resolved','告警恢复']]
async function freePort(p){await new Promise((resolve,reject)=>{const s=net.createServer();s.once('error',()=>reject(Error(`端口 ${p} 已占用，拒绝使用其他服务`)));s.listen(p,'127.0.0.1',()=>s.close(resolve))})}
function launch(binary,args,extra={}){
 const child=spawn(binary,args,{cwd:root,env:{...process.env,...extra},windowsHide:true,stdio:['ignore','pipe','pipe']})
 child.lines=[];for(const stream of [child.stdout,child.stderr])stream.on('data',b=>{child.lines.push(String(b));if(child.lines.length>40)child.lines.shift()})
 child.on('error',e=>{child.launchError=e});children.push(child);return child
}
async function stop(child){if(!child||child.exitCode!==null||child.signalCode!==null)return;await new Promise(resolve=>{child.once('exit',resolve);child.kill()})}
async function waitFor(fn,label,timeout=45000){const end=Date.now()+timeout;while(Date.now()<end){if(closing)throw Error('模拟环境正在停止');const value=await fn();if(value)return value;await sleep(250)}throw Error(`${label}超时，请检查模拟执行端状态`)}
async function backendRequest(route,body,headers={}){
 // Node fetch may normalize Host to the transport destination. Use the HTTP
 // client explicitly so the backend validates the browser-facing origin.
 return new Promise((resolve,reject)=>{
  const request=http.request({hostname:'127.0.0.1',port:backendPort,path:route,method:body===undefined?'GET':'POST',headers:{Host:host,Cookie:cookie,...(body===undefined?{}:{'Content-Type':'application/json','X-Requested-With':'KubePilot'}),...headers}},response=>{
   const chunks=[];let length=0
   response.on('data',b=>{length+=b.length;if(length>4e6){request.destroy(Error('平台响应过大'));return}chunks.push(b)})
   response.on('end',()=>{const responseHeaders=new Headers();for(const [key,value]of Object.entries(response.headers)){for(const v of Array.isArray(value)?value:[value])if(v!==undefined)responseHeaders.append(key,v)}resolve(new Response(Buffer.concat(chunks),{status:response.statusCode,headers:responseHeaders}))})
   response.on('error',reject)
  })
  request.setTimeout(15000,()=>request.destroy(Error('平台接口超时')));request.on('error',reject);request.end(body===undefined?undefined:JSON.stringify(body))
 })
}
async function api(route,body){
 const res=await backendRequest('/api'+route,body);const value=await res.json()
 if(!res.ok)throw Error(value.error||`平台请求失败 (${res.status})`);return value
}
async function login(){const r=await backendRequest('/api/auth/login',{username,password},{Cookie:''});if(!r.ok)throw Error('模拟账号登录失败');return {cookie:r.headers.get('set-cookie'),body:await r.json()}}
async function platformState(){return api('/platform/state')}
async function startPlatform(closed){
 platform=launch(executable('kubepilot-platform'),[],{PLATFORM_ADDR:`127.0.0.1:${backendPort}`,PLATFORM_PUBLIC_ORIGIN:base,PLATFORM_ALLOW_HTTP:'true',PLATFORM_REGISTRATION_CLOSED:String(closed),PLATFORM_DATA_DIR:path.join(dir,'platform'),PLATFORM_WEB_DIR:path.join(root,'web','dist')})
 await waitFor(async()=>{if(platform.launchError||platform.exitCode!==null)throw Error('模拟平台启动失败');try{return(await backendRequest('/api/platform/ping')).ok}catch{return false}},'平台启动',15000)
}
async function startConnector(inject=false){
 connector=launch(executable('kubepilot-connector'),['-config',path.join(dir,'connector.json'),'-data',connectorDir,'-demo',...(inject?['-demo-fault','ServiceSelector']:[])],{KUBECONFIG:''})
 await waitFor(()=>{if(connector.launchError||connector.exitCode!==null)throw Error('模拟 Connector 启动失败');return connector.lines.some(s=>s.includes('connector started'))},'执行端启动',10000)
}
async function scanHealthy(){
 await waitFor(async()=>!(await platformState()).busy,'等待现有任务')
 const started=Date.now();await api('/platform/scan',{})
 return waitFor(async()=>{const s=await platformState();return !s.busy&&Date.parse(s.lastScan)>=started&&s.snapshot?.resources.find(r=>r.kind==='Service'&&r.name==='payment-service')?.status==='healthy'?s:null},'健康状态采集')
}
function stage(id,status,detail,at=now()){
 const s=current.steps.find(s=>s.id===id);if(s.status==='done')return
 s.status=status;s.detail=detail;if(status==='done')s.at=at
}
function syncCase(c){
 current.incidentId=c.incident.id;current.case=c
 const i=c.incident, timeline=i.timeline||[], at=status=>timeline.find(t=>t.status===status)?.at||now()
 stage('collect','done',`Connector 返回 ${i.evidence.length} 条现场证据，确认 ServiceSelector 异常`,i.firstSeen)
 if(i.diagnosis){stage('diagnose','done','模拟模型完成结构化诊断；依据来自本次采集的证据',timeline.find(t=>t.title==='诊断完成')?.at)}else stage('diagnose','running','等待模型工具调用返回')
 if(i.plan){current.before=i.plan.before;current.after=i.plan.after;stage('review','running','校验授权、健康基线、目标版本，并发起独立复核')}
 if(c.review)stage('review','done',`复核结论：${c.review.decision}；绑定当前计划哈希`,c.review.at)
 if(i.status==='executing')stage('execute','running','执行前再次检查计划，Connector 正在提交限定字段变更')
 if(timeline.some(t=>t.status==='verifying')){stage('execute','done','Connector 已返回变更成功，进入恢复验证',at('verifying'));stage('verify','running','等待连续两次模拟业务检查')}
 if(i.status==='resolved')stage('verify','done',timeline.find(t=>t.status==='resolved')?.detail||'恢复验证通过',i.resolvedAt)
}
async function saveReport(){await writeFile(path.join(dir,`report-${current.id}.json`),JSON.stringify({...current,scope:'模拟集群 + 模拟模型；平台和 Connector 通过真实 HTTP 协议运行。'},null,2),{mode:0o600})}
async function simulate(){
 try{
  // Refresh the private runner session without altering the browser's session.
  if(!(await api('/auth/session')).authenticated){const signed=await login();cookie=signed.cookie.split(';')[0]}
  const s=await platformState()
  if(s.config.generation!==generation||s.config.baseUrl!==modelURL||s.config.model!=='fixture-approve'||!s.config.scope.automatic)throw Error('演示配置已变化或自动修复已暂停，请停止并重新启动模拟环境')
  stage('inject','running','先采集健康状态，再注入预设故障')
  const healthy=await scanHealthy();const known=new Set(healthy.cases.map(c=>c.incident.id))
  const journalBefore=Object.keys(JSON.parse(await readFile(path.join(connectorDir,'journal.json'),'utf8').catch(()=> '{}'))).length
  await stop(connector);await startConnector(true)
  const world=JSON.parse(await readFile(path.join(connectorDir,'demo-world.json'),'utf8'))
  if(!world.faults.ServiceSelector)throw Error('故障注入未生效')
  stage('inject','done','模拟 Service selector 变为 app=payment-v2，与健康 Pod 标签不匹配')
  const startsAt=now(), fingerprint='demo-'+current.id
  const payload={version:'4',groupKey:'{}:{alertname="KubeServiceNoReadyEndpoints"}',status:'firing',receiver:'kubepilot',groupLabels:{alertname:'KubeServiceNoReadyEndpoints'},commonLabels:{namespace:scope.namespace,service:'payment-service',simulation:'true'},externalURL:'http://alertmanager.simulation.invalid',alerts:[{status:'firing',labels:{alertname:'KubeServiceNoReadyEndpoints',namespace:scope.namespace,service:'payment-service',severity:'critical',simulation:'true'},annotations:{summary:'[模拟] payment-service 没有就绪端点',description:'Service 选择器偏离健康基线，模拟业务不可用。'},startsAt,fingerprint}]}
  current.payload=payload;stage('alert','running','正在通过真实接收地址投递 Alertmanager v4 格式通知')
  const response=await fetch(base+'/ingest/alertmanager',{method:'POST',headers:{'Content-Type':'application/json',Authorization:'Bearer '+config.webhookToken},body:JSON.stringify(payload),signal:AbortSignal.timeout(10000)})
  if(response.status!==202)throw Error(`Webhook 未被接收 (${response.status})`)
  const received=await platformState();const alert=received.alerts.find(a=>a.fingerprint===fingerprint)
  if(!alert)throw Error('平台尚未归档本次告警')
  current.alertId=alert.id;stage('alert','done','平台已鉴权、接收并归档 firing 通知；由通知触发巡检',alert.receivedAt);stage('collect','running','等待 Connector 领取采集任务')
  await waitFor(async()=>{
   const st=await platformState();const c=st.cases.find(c=>!known.has(c.incident.id)&&c.incident.type==='ServiceSelector')
   if(c)syncCase(c)
   if(c&&!st.busy&&['manual','rejected','planned'].includes(c.incident.status))throw Error(`流程已停止：${c.incident.timeline.at(-1)?.detail||c.incident.status}`)
   if(!st.busy&&st.lastError&&!c)throw Error(st.lastError)
   return c?.incident.status==='resolved'&&!st.busy
  },'全链路执行',120000)
  const after=await scanHealthy();const completed=after.cases.find(c=>c.incident.id===current.incidentId);syncCase(completed)
  const finalWorld=JSON.parse(await readFile(path.join(connectorDir,'demo-world.json'),'utf8'))
  const journal=JSON.parse(await readFile(path.join(connectorDir,'journal.json'),'utf8'))
  current.writeCount=Object.keys(journal).length-journalBefore
  if(finalWorld.faults.ServiceSelector||current.writeCount!==1||completed.incident.executionCount!==1||completed.review?.decision!=='approve')throw Error('修复后读回检查或执行次数检查未通过')
  current.resource=after.snapshot.resources.find(r=>r.name==='payment-service'&&r.kind==='Service')
  stage('resolved','running','业务验证通过后，再投递告警恢复通知')
  const resolved={...payload,status:'resolved',alerts:[{...payload.alerts[0],status:'resolved',endsAt:now()}]}
  const rr=await fetch(base+'/ingest/alertmanager',{method:'POST',headers:{'Content-Type':'application/json',Authorization:'Bearer '+config.webhookToken},body:JSON.stringify(resolved),signal:AbortSignal.timeout(10000)})
  if(rr.status!==202)throw Error('恢复通知发送失败')
  const final=await platformState();const finalAlert=final.alerts.find(a=>a.id===current.alertId)
  if(finalAlert?.status!=='resolved'||finalAlert.count!==2)throw Error('恢复通知读回检查未通过')
  current.alert=finalAlert;stage('resolved','done','同一条告警已更新为 resolved；业务恢复与通知恢复分别验证',finalAlert.receivedAt)
  current.status='resolved';current.finishedAt=now();await saveReport()
 }catch(e){current.status='failed';current.error=e.message;current.finishedAt=now();await saveReport().catch(()=>{})}
 finally{runBusy=false}
}
function json(res,code,value,headers={}){res.writeHead(code,{'Content-Type':'application/json; charset=utf-8','Cache-Control':'no-store','X-Content-Type-Options':'nosniff','X-Frame-Options':'DENY',...headers});res.end(JSON.stringify(value))}
function browserCookie(raw=''){
 const item=raw.split(';').map(v=>v.trim()).find(v=>v.startsWith('kubepilot_demo_session='))
 return item?item.replace(/^kubepilot_demo_session=/,'kubepilot_session='):''
}
function demoCookie(raw){return raw.replace(/^kubepilot_session=/,'kubepilot_demo_session=')}
async function ownerRequest(req){
 const result=await backendRequest('/api/auth/session',undefined,{Cookie:browserCookie(req.headers.cookie)});const session=await result.json()
 return session.authenticated&&session.account.id===owner&&session.workspace?.id===workspace&&(!req.headers['x-kubepilot-account']||req.headers['x-kubepilot-account']===owner)&&(!req.headers['x-kubepilot-workspace']||req.headers['x-kubepilot-workspace']===workspace)
}
async function handle(req,res){
 if(req.headers.host!==host)return json(res,403,{error:'请使用模拟环境的 localhost 地址'})
 if(req.headers.origin&&req.headers.origin!==base||req.headers['sec-fetch-site']==='cross-site')return json(res,403,{error:'不允许跨站访问'})
 const route=new URL(req.url,base).pathname
 if(route==='/api/demo/health')return json(res,ready?200:503,{product:'kubepilot-flow-demo',ready,instance})
 if(req.method==='POST'&&route.startsWith('/api/')){
  if(req.headers['x-requested-with']!=='KubePilot'||!req.headers['content-type']?.startsWith('application/json'))return json(res,403,{error:'需要应用 JSON 请求'})
 }
 if(route==='/api/demo/shutdown'&&req.method==='POST'){
  if(req.headers.authorization!=='Bearer '+control)return json(res,401,{error:'管理凭证无效'})
  json(res,200,{stopping:true});setImmediate(()=>shutdown());return
 }
 if(route==='/api/platform/ping'&&req.method==='GET')return json(res,200,{product:'kubepilot-platform',version:'0.2.0',simulation:true})
 if(route==='/api/demo/enter'&&req.method==='POST'){
  if(!ready)return json(res,503,{error:'模拟环境正在启动，请稍后重试'})
  const signed=await login();return json(res,200,{...signed.body,registrationOpen:false},{'Set-Cookie':demoCookie(signed.cookie)})
 }
 if(route.startsWith('/api/demo/')){
  if(!await ownerRequest(req))return json(res,401,{error:'请先进入模拟工作空间'})
  if(route==='/api/demo/state'&&req.method==='GET')return json(res,200,{simulation:true,ready,running:runBusy,run:current})
  if(route==='/api/demo/report'&&req.method==='GET'){if(!current)return json(res,409,{error:'尚未运行模拟'});return json(res,200,current)}
  if(route==='/api/demo/run'&&req.method==='POST'){
   if(runBusy)return json(res,409,{error:'已有模拟正在运行，请等待完成'})
   if(current?.status==='failed')return json(res,409,{error:'上次流程已停止。请导出记录，重启模拟环境后重试'})
   current={id:randomBytes(6).toString('hex'),status:'running',startedAt:now(),steps:steps.map(([id,title])=>({id,title,status:'pending',detail:'尚未执行'})),modelRequests:[],writeCount:0}
   runBusy=true;json(res,202,{accepted:true,id:current.id});void simulate();return
  }
  return json(res,404,{error:'模拟接口不存在'})
 }
 if(req.method==='POST'&&(route.startsWith('/api/platform/knowledge')||/^\/api\/platform\/setup\//.test(route)||/^\/api\/workspace\//.test(route)||['/api/auth/register','/api/auth/login','/api/auth/password'].includes(route)))return json(res,403,{error:'此隔离演示使用预置模拟配置，请在正式工作空间配置真实接入'})
 if(runBusy&&req.method==='POST'&&route==='/api/platform/scan')return json(res,409,{error:'全链路模拟正在采集与处理，请等待本次完成'})
 const upstream=http.request({host:'127.0.0.1',port:backendPort,path:req.url,method:req.method,headers:{...req.headers,host,cookie:browserCookie(req.headers.cookie)}},reply=>{const headers={...reply.headers};if(headers['set-cookie'])headers['set-cookie']=headers['set-cookie'].map(demoCookie);res.writeHead(reply.statusCode,headers);reply.pipe(res)})
 upstream.on('error',()=>{if(!res.headersSent)json(res,502,{error:'模拟平台暂时不可达'});else res.destroy()});req.pipe(upstream)
}
async function shutdown(){
 if(closing)return;closing=true;ready=false
 if(proxy){proxy.close();proxy.closeAllConnections()}
 await stop(connector);await stop(platform)
 if(fixture){fixture.closeAllConnections();await new Promise(resolve=>fixture.close(resolve))}
 await writeFile(activeFile,JSON.stringify({stopped:true,instance,base,dir},null,2),{mode:0o600}).catch(()=>{})
 process.exit(0)
}
process.on('SIGINT',shutdown);process.on('SIGTERM',shutdown)
try{
 await freePort(port);await freePort(backendPort);await mkdir(connectorDir,{recursive:true});await mkdir(path.dirname(activeFile),{recursive:true})
 await writeFile(path.join(connectorDir,'demo-world.json'),JSON.stringify({version:1,faults:{}}),{mode:0o600})
 fixture=await startFixture(0,{delayMs:1700,onCall:tool=>{if(runBusy&&tool!=='connection_check')current.modelRequests.push({tool,at:now(),simulated:true})}});modelURL='http://127.0.0.1:'+fixture.address().port
 await startPlatform(false)
 const registration=await backendRequest('/api/auth/register',{username,password,confirmation:password});if(!registration.ok)throw Error('创建模拟账号失败');cookie=registration.headers.get('set-cookie').split(';')[0]
 owner=(await registration.json()).account.id
 workspace=(await api('/workspace/create',{name:'全链路模拟空间',description:'隔离的模拟集群与模型；不使用真实 Kubernetes 凭证。'})).workspace.id
 const source=await api('/platform/setup/source',{publicUrl:base,allowHttp:true});const webhookToken=source.config.match(/credentials: "([^"]+)"/)[1]
 await api('/platform/setup/model',{baseUrl:modelURL,model:'fixture-approve',apiKey:'simulation-not-a-real-key'})
 const manifest=(await api('/platform/setup/connector',scope)).manifest
 generation=(await platformState()).config.generation
 config={serverUrl:base,token:manifest.match(/"token":"([^"]+)"/)[1],allowHttp:true,generation,scope,webhookToken}
 const {webhookToken:_,...enrollment}=config;await writeFile(path.join(dir,'connector.json'),JSON.stringify(enrollment),{mode:0o600})
 await stop(platform);await startPlatform(true)
 proxy=http.createServer((req,res)=>handle(req,res).catch(()=>{if(!res.headersSent)json(res,500,{error:'模拟控制服务异常，请查看本地日志'});else res.destroy()}))
 await new Promise((resolve,reject)=>{proxy.once('error',reject);proxy.listen(port,'127.0.0.1',resolve)})
 await startConnector()
 await waitFor(async()=>{const s=await platformState();return s.ready&&s.connection.provider==='demo'},'执行端心跳与基线')
 await scanHealthy();await api('/workspace/complete',{})
 ready=true
 await writeFile(activeFile,JSON.stringify({instance,base,dir,pid:process.pid,platformPid:platform.pid,connectorPid:connector.pid,control},null,2),{mode:0o600})
 console.log(`全链路模拟已就绪：${base}`)
 console.log('进入演示工作空间后，点击“注入故障并发送告警”。集群与模型均为模拟。')
}catch(e){console.error('模拟环境启动失败：'+e.message);for(const child of children.reverse())await stop(child);if(fixture)fixture.close();if(proxy)proxy.close();process.exit(1)}
