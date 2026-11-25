import assert from 'node:assert/strict'
import { spawn } from 'node:child_process'
import { mkdir, mkdtemp } from 'node:fs/promises'
import { once } from 'node:events'
import { createServer } from 'node:net'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { chromium } from 'playwright'

const root=fileURLToPath(new URL('../../',import.meta.url))
const scratchRoot=path.join(root,'.work')
await mkdir(scratchRoot,{recursive:true})
const scratch=await mkdtemp(path.join(scratchRoot,'knowledge-qa-'))
const socket=createServer()
socket.listen(0,'127.0.0.1')
await once(socket,'listening')
const addr='127.0.0.1:'+socket.address().port
await new Promise(resolve=>socket.close(resolve))
const base='http://'+addr
const server=spawn(process.env.PLATFORM_TEST_BINARY||path.join(root,'bin',process.platform==='win32'?'kubepilot-platform.exe':'kubepilot-platform'),[],{
  cwd:root, windowsHide:true,stdio:'inherit',
  env:{...process.env,PLATFORM_ADDR:addr,PLATFORM_PUBLIC_ORIGIN:'',PLATFORM_REGISTRATION_CLOSED:'false',PLATFORM_DATA_DIR:path.join(scratch,'data'),PLATFORM_WEB_DIR:path.join(root,'web','dist')},
})
let browser
try {
  let ready=false
  for(let i=0;i<60;i++){
    if(server.exitCode!==null)throw Error('Platform exited before startup')
    try{ready=(await fetch(base+'/api/platform/ping')).ok}catch{}
    if(ready)break
    await new Promise(resolve=>setTimeout(resolve,150))
  }
  assert.ok(ready)
  browser=await chromium.launch({channel:process.env.E2E_BROWSER||'chrome',headless:true})
  const ctx=await browser.newContext({viewport:{width:1440,height:900}})
  async function post(url,data){
    const response=await ctx.request.post(base+url,{data,headers:{'X-Requested-With':'KubePilot'}})
    assert.ok(response.ok(),url+' '+response.status())
  }
  const password='isolated-browser-test-password'
  await post('/api/auth/register',{username:'knowledge-owner',password,confirmation:password})
  await post('/api/workspace/create',{name:'知识库验证空间'})
  const page=await ctx.newPage()
  const errors=[]
  page.on('pageerror',e=>errors.push(e.message))
  await page.goto(base)
  await page.getByRole('button',{name:'知识库',exact:true}).click()
  await page.getByText('尚未导入资料。内置手册已可用于诊断。').waitFor()
  await page.getByRole('button',{name:'导入资料',exact:true}).click()
  await page.getByLabel('选择文件').setInputFiles({
    name:'入库作业复盘.md',mimeType:'text/markdown',buffer:Buffer.from('# 镜像源切换\nRegistryNotReady 时核对最近一次拉取事件。'),
  })
  await page.getByRole('button',{name:'保存并建立索引'}).click()
  await page.getByRole('status').filter({hasText:'资料已加入索引'}).waitFor()
  const input=page.getByRole('searchbox',{name:'检索关键词'})
  await input.fill('RegistryNotReady')
  await input.press('Enter')
  await page.locator('.kb-results article').filter({hasText:'入库作业复盘'}).waitFor()
  for(const [width,height] of [[1440,900],[768,1024],[390,844]]){
    await page.setViewportSize({width,height})
    const button=await page.getByRole('button',{name:'检索',exact:true}).boundingBox()
    assert.ok(button && button.height>=(width===390?42:38),'search button tap area')
    await page.screenshot({path:path.join(scratch,'library-'+width+'.png')})
    assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth && document.querySelector('.p-main').scrollWidth<=document.querySelector('.p-main').clientWidth))
  }
  await page.getByText('入库作业复盘',{exact:true}).click()
  await page.getByRole('button',{name:'移除资料',exact:true}).click()
  await page.getByRole('button',{name:'确认移除'}).click()
  await page.getByText('没有匹配段落。可尝试事件原文或业务资源名称。').waitFor()
  assert.deepEqual(errors,[])
  console.log('PASS workspace knowledge import, retrieval, mobile removal and empty result;',scratch)
} finally {
  await browser?.close()
  if(server.exitCode===null&&server.signalCode===null){const exit=once(server,'exit');server.kill();await exit}
}
