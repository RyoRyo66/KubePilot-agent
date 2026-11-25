"""Optional local OpenAI-compatible model fixture. Skip if a real model is configured."""
import argparse
import json
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from common import log


class Handler(BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def reply(self, code, body):
        data = json.dumps(body, ensure_ascii=False).encode("utf-8")
        self.send_response(code); self.send_header("Content-Type", "application/json; charset=utf-8")
        self.send_header("Content-Length", str(len(data))); self.end_headers(); self.wfile.write(data)

    def do_GET(self):
        self.reply(200 if self.path == "/health" else 404, {"simulation": True})

    def do_POST(self):
        if not self.path.rstrip("/").endswith("/chat/completions"):
            return self.reply(404, {"error": "unsupported endpoint"})
        try:
            self.connection.settimeout(10)
            length = int(self.headers.get("Content-Length", "0"))
            if not 0 < length <= 2_000_000:
                raise ValueError("invalid size")
            body = json.loads(self.rfile.read(length))
            name = body["tool_choice"]["function"]["name"]
            value = {} if name == "connection_check" else json.loads(body["messages"][-1]["content"])
            ids = [e["id"] for e in value.get("incident", {}).get("evidence", [])][:3]
            if name == "connection_check":
                args = {"ok": True}
            elif name == "triage_incident":
                inspect = body.get("model") in ("mock-investigate", "mock-insufficient")
                args = {"conclusion": "[模拟模型] 现场显示选择器与基线不符。", "evidenceIds": ids,
                        "recommendations": ["对照健康基线与当前后端就绪状态。"], "limitations": "仅限脚本模拟状态。",
                        "disposition": "inspect" if inspect else "consider_plan",
                        "checks": ["service_backends"] if inspect else [],
                        "hypotheses": [{"cause": "端点缺失可能来自选择器或后端未就绪。", "check": "比较两种选择器的匹配数量。", "evidenceIds": ids}] if inspect else []}
            elif name == "submit_hypotheses":
                args = {"hypotheses": [{"cause": "[模拟模型] Service 选择器可能偏离健康基线。", "evidenceIds": ids, "check": "复采时比较基线选择器与就绪 Pod 标签。"}]}
            elif name == "submit_diagnosis":
                args = {"conclusion": "[模拟模型] Service 选择器偏离健康基线，没有就绪端点。", "evidenceIds": ids, "recommendations": ["按健康基线恢复选择器，随后验证业务状态。"], "limitations": "固定协议响应，不是真实大模型诊断。"}
                if "firstObservation" in value:
                    args["evidenceIds"] = [e["id"] for e in value["incident"]["evidence"] if e["id"].startswith("check-")]
                    args["disposition"] = "need_evidence" if body.get("model") == "mock-insufficient" else "consider_plan"
            elif name == "submit_review":
                decision = {"mock-reject": "reject", "mock-manual": "manual"}.get(body.get("model"), "approve")
                args = {"decision": decision, "reason": "[模拟模型] 用于检查复核分支，执行权限仍由服务和执行端约束。", "evidenceIds": ids, "planHash": value["planHash"]}
            else:
                raise ValueError("unsupported tool")
            log(f"RECV {name} -> SEND 模拟工具响应")
            self.reply(200, {"id": "script-mock-model", "object": "chat.completion", "choices": [{"index": 0, "finish_reason": "tool_calls", "message": {"role": "assistant", "content": None, "tool_calls": [{"id": "mock-call", "type": "function", "function": {"name": name, "arguments": json.dumps(args, ensure_ascii=False)}}]}}]})
        except (ValueError, KeyError, TypeError, IndexError):
            self.reply(400, {"error": "invalid mock model request"})


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--port", type=int, default=18083)
    args = parser.parse_args()
    server = ThreadingHTTPServer(("127.0.0.1", args.port), Handler)
    log(f"模拟模型 http://127.0.0.1:{args.port}/v1；模型名 mock-approve，API Key 可填 mock-key")
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        pass
    finally:
        server.server_close()
