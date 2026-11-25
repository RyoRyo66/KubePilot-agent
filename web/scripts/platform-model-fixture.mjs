// Local deterministic protocol fixture. This is NOT a real language model.
import http from 'node:http'
const port = Number(process.env.FIXTURE_PORT || 18083)
export function startFixture(listenPort = port, options = {}) {
  const server = http.createServer(async (req, res) => {
    res.setHeader('Content-Type', 'application/json')
    if (req.url === '/health') { res.end(JSON.stringify({fixture:true})); return }
    if (req.method !== 'POST' || !req.url.endsWith('/chat/completions')) {res.writeHead(404);res.end('{}');return}
    try {
      let raw='';for await(const chunk of req){raw+=chunk;if(raw.length>2e6)throw Error('Too large')}
      const body=JSON.parse(raw)
      if(body.model==='fixture-unavailable'){res.writeHead(503);res.end('{"error":"fixture unavailable"}');return}
      const name=body.tool_choice?.function?.name
      options.onCall?.(name)
      if(name!=='connection_check' && options.delayMs) await new Promise(resolve=>setTimeout(resolve,options.delayMs))
      const input=name==='connection_check'?{}:JSON.parse(body.messages.at(-1).content)
      const ids=(input.incident?.evidence||[]).map(e=>e.id).slice(0,3)
      let args
      if(name==='connection_check') args={ok:true}
      else if(name==='triage_incident') args={
        disposition:'inspect', checks:['service_backends'], conclusion:'[协议测试模型] 需区分选择器漂移和后端未就绪。',
        evidenceIds:ids,recommendations:['核对当前和基线选择器匹配的后端。'],limitations:'固定模拟响应。',
        hypotheses:[{cause:'[协议测试模型] 选择器可能与基线不符。',evidenceIds:ids,check:'复查当前选择器及就绪端点。'}]}
      else if(name==='submit_hypotheses') args={hypotheses:[{cause:'[协议测试模型] 选择器可能与基线不符。',evidenceIds:ids,check:'复查当前选择器及就绪端点。'}]}
      else if(name==='submit_diagnosis') args={conclusion:'[协议测试模型] 实时证据显示服务选择器偏离健康基线，导致就绪端点为空。',evidenceIds:input.firstObservation ? input.incident.evidence.filter(e=>e.id.startsWith('check-')).map(e=>e.id) : ids,recommendations:['按已保存的健康基线恢复选择器，并验证端点与业务响应。'],limitations:'这是确定性测试响应，并非真实大模型诊断。',...(input.firstObservation?{disposition:'consider_plan'}:{})}
      else if(name==='submit_review') args={decision:body.model==='fixture-reject'?'reject':body.model==='fixture-manual'?'manual':'approve',reason:body.model==='fixture-reject'?'[协议测试模型] 拒绝分支测试：停止执行。':body.model==='fixture-manual'?'[协议测试模型] 人工确认分支测试：请核对具体差异。':'[协议测试模型] 计划只恢复授权对象的选择器，绑定现有健康基线及资源版本，具备恢复验证步骤。',evidenceIds:ids,planHash:input.planHash}
      else throw Error('Unsupported fixture tool')
      res.end(JSON.stringify({id:'fixture-response',object:'chat.completion',choices:[{index:0,finish_reason:'tool_calls',message:{role:'assistant',content:null,tool_calls:[{id:'fixture-call',type:'function',function:{name,arguments:JSON.stringify(args)}}]}}]}))
    }catch{res.writeHead(400);res.end('{"error":"invalid fixture request"}')}
  })
  return new Promise(resolve=>server.listen(listenPort,'127.0.0.1',()=>resolve(server)))
}
if(process.argv[1]?.endsWith('platform-model-fixture.mjs'))startFixture().then(()=>console.log(`Protocol fixture only: http://127.0.0.1:${port}`))
