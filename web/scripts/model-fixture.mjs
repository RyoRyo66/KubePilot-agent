// Local protocol fixture for browser/API tests. This is not an actual LLM.
import http from 'node:http';
const port = Number(process.env.FIXTURE_PORT || 18083);
const server = http.createServer(async (request, response) => {
  if (request.method === 'GET' && request.url === '/health') {
    response.writeHead(200, { 'Content-Type': 'application/json' }); response.end('{"fixture":true}'); return;
  }
  if (request.url !== '/v1/chat/completions' || request.method !== 'POST') { response.writeHead(404); response.end(); return; }
  const chunks = []; for await (const chunk of request) chunks.push(chunk);
  const body = JSON.parse(Buffer.concat(chunks).toString());
  const results = body.messages.filter(message => message.role === 'tool');
  let name = 'read_evidence', args = { source: 'all' };
  if (results.length === 1) { name = 'search_runbooks'; args = { query: '目标异常' }; }
  if (results.length >= 2) {
    const evidence = JSON.parse(results[0].content);
    name = 'submit_diagnosis';
    args = { conclusion: '协议测试夹具：已读取异常证据，需要依据健康基线核对配置。', evidenceIds: evidence.map(item => item.id), recommendations: ['核对事件、对象版本与可信健康基线'], limitations: '这是本地协议测试响应，不代表真实模型的诊断能力。' };
  }
  response.writeHead(200, { 'Content-Type': 'application/json' });
  response.end(JSON.stringify({ choices: [{ finish_reason: 'tool_calls', message: { role: 'assistant', content: null, tool_calls: [{ id: 'fixture-' + results.length, type: 'function', function: { name, arguments: JSON.stringify(args) } }] } }], usage: { prompt_tokens: 40, completion_tokens: 20 } }));
});
server.listen(port, '127.0.0.1', () => console.log('Protocol fixture http://127.0.0.1:' + port));
for (const signal of ['SIGINT', 'SIGTERM']) process.on(signal, () => server.close());
