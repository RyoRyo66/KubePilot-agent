import assert from 'node:assert/strict'
import { spawn } from 'node:child_process'
import { mkdir, writeFile } from 'node:fs/promises'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const root=path.resolve(path.dirname(fileURLToPath(import.meta.url)),'../..')
const data=path.join(root,'.work',`accounts-maintenance-${Date.now()}`)
const binary=process.env.PLATFORM_TEST_BINARY||path.join(root,'bin','kubepilot-platform.exe')
const base='http://127.0.0.1:18085', username='maintenance-fixture', password='maintenance-fixture-password', replacement='maintenance-fixture-replacement'
let server
await mkdir(data,{recursive:true})
const env={...process.env,PLATFORM_ADDR:'127.0.0.1:18085',PLATFORM_DATA_DIR:data,PLATFORM_PUBLIC_ORIGIN:'',PLATFORM_REGISTRATION_CLOSED:'false',PLATFORM_WEB_DIR:path.join(root,'web','dist')}
async function stop(){if(!server||server.exitCode!==null||server.signalCode!==null)return;await new Promise(resolve=>{server.once('exit',resolve);server.kill()})}
async function start(){
  server=spawn(binary,[],{env,cwd:root,windowsHide:true,stdio:'ignore'})
  for(let i=0;i<50;i++){try{if((await fetch(base+'/api/platform/ping')).ok)return}catch{}await new Promise(r=>setTimeout(r,100))}
  throw Error('Maintenance QA server failed to start')
}
async function api(route,body,cookie=''){
 const result=await fetch(base+route,{method:body===undefined?'GET':'POST',headers:{Cookie:cookie,'Content-Type':'application/json','X-Requested-With':'KubePilot'},body:body===undefined?undefined:JSON.stringify(body)})
 return {status:result.status,body:await result.json(),cookie:result.headers.get('set-cookie')?.split(';')[0]||''}
}
async function reset(){
 const child=spawn(binary,['reset-password','--data',data,'--username',username,'--password-stdin'],{env,cwd:root,windowsHide:true,stdio:['pipe','pipe','pipe']})
 let output='';child.stdout.on('data',b=>output+=b);child.stderr.on('data',b=>output+=b)
 child.stdin.end(replacement+'\n'+replacement+'\n')
 const code=await new Promise((resolve,reject)=>{child.once('exit',resolve);child.once('error',reject)})
 assert.equal(output.includes(password),false);assert.equal(output.includes(replacement),false)
 return {code,output}
}
try{
 let occupied=false;try{await fetch(base+'/api/platform/ping');occupied=true}catch{}assert.equal(occupied,false,'QA port already in use')
 await start()
 const registration=await api('/api/auth/register',{username,password,confirmation:password});assert.equal(registration.status,200)
 const cookie=registration.cookie
 const creation=await api('/api/workspace/create',{name:'保留的个人空间'},cookie);assert.equal(creation.status,200)
 assert.equal((await api('/api/platform/setup/source',{publicUrl:base,allowHttp:true},cookie)).status,200)
 const locked=await reset();assert.notEqual(locked.code,0);assert.match(locked.output,/数据目录正在使用/)
 await stop()
 assert.equal((await reset()).code,0)
 await start()
 assert.equal((await api('/api/platform/state',undefined,cookie)).status,401)
 assert.equal((await api('/api/auth/login',{username,password})).status,401)
 const login=await api('/api/auth/login',{username,password:replacement});assert.equal(login.status,200)
 assert.equal(login.body.workspace.id,creation.body.workspace.id)
 assert.equal(login.body.workspace.name,'保留的个人空间')
 assert.equal((await api('/api/platform/state',undefined,login.cookie)).body.config.publicUrl,base)
 const result={at:new Date().toISOString(),checks:['running server excludes offline account writer','hidden stdin password reset succeeds','old session and password rejected after restart','new password preserves workspace ID and configuration']}
 await writeFile(path.join(root,'docs','platform-review','account','maintenance-results.json'),JSON.stringify(result,null,2))
 console.log('PASS account maintenance: exclusive lock, CLI password reset, session revocation and persistent workspace')
}finally{await stop()}
