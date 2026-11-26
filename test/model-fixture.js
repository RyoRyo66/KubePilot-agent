// Deterministic Chat Completions double for the isolated acceptance cluster.
import http from 'node:http';
import { pathToFileURL } from 'node:url';
import { resolve } from 'node:path';
export function modelFixture() {
  return http.createServer(async (req, res) => {
    if (req.method !== 'POST' || req.url !== '/v1/chat/completions') {
      res.writeHead(404).end();
      return;
    }
    try {
      let raw = '';
      for await (const p of req) {
        raw += p;
        if (raw.length > 512000) throw new Error();
      }
      const b = JSON.parse(raw),
        tool = b.tools[0].function.name,
        input = JSON.parse(b.messages.at(-1).content);
      const refs = input.evidence?.slice(0, 2).map((e) => e.id);
      const output =
        tool === 'connection'
          ? { connected: true }
          : tool === 'review'
            ? {
                decision: 'approve',
                reason: '验收夹具核对证据、基线和计划摘要',
                evidenceIds: refs,
                planHash: input.planHash,
              }
            : {
                conclusion: '验收夹具观察到服务缺少就绪端点',
                evidenceIds: refs,
                recommendations: ['核对 Service 选择器与健康基线，验证端点和业务 HTTP'],
                limitations: '此模型为确定性接口测试夹具',
                disposition: input.completedChecks.length ? 'consider_plan' : 'inspect',
                checks: ['workload', 'events'],
              };
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(
        JSON.stringify({
          choices: [
            {
              finish_reason: 'tool_calls',
              message: {
                tool_calls: [
                  { type: 'function', function: { name: tool, arguments: JSON.stringify(output) } },
                ],
              },
            },
          ],
        }),
      );
    } catch {
      res.writeHead(400).end();
    }
  });
}
if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href)
  modelFixture().listen(18083, '0.0.0.0');
