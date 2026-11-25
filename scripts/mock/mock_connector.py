"""Poll service jobs and reply with simulated collection/repair results. No kubectl."""
import argparse
import datetime as dt
import time
from common import DEFAULT_CONFIG, connector_id, database, dump, load_config, log, now, plan, plan_hash, post, single_connector, target, token, world


def collect(config, state):
    obj = target(config, state["version"])
    evidence = [{"id": "mock-selector", "source": "Script Mock / Service", "summary": "选择器与健康基线不一致", "detail": "[模拟] selector app=payment-v2；健康 Pod labels app=payment；基线 selector app=payment", "at": now()}, {"id": "mock-endpoints", "source": "Script Mock / EndpointSlice", "summary": "Service 没有就绪端点", "detail": "[模拟] ready endpoints: 0；关联 Pod 已就绪", "at": now()}]
    finding = {"key": f"{obj['namespace']}/{obj['name']}/ServiceSelector", "type": "ServiceSelector", "title": "[脚本模拟] Service 选择器异常", "severity": "critical", "target": obj, "evidence": evidence}
    resource = {"kind": "Service", "name": obj["name"], "namespace": obj["namespace"], "ready": 0 if state["fault"] else 2, "desired": 2, "status": "warning" if state["fault"] else "healthy"}
    return {"at": now(), "cluster": "script-mock", "nodes": 1, "readyNodes": 1, "pods": 2, "readyPods": 2, "services": 1, "resources": [resource], "findings": [finding] if state["fault"] else [], "warnings": ["脚本模拟数据；未访问真实 Kubernetes"]}


def handle(config, job):
    result = {"id": job.get("id", ""), "lease": job.get("lease", ""), "connectorId": connector_id(config), "ok": False, "detail": ""}
    try:
        remaining = (dt.datetime.fromisoformat(job["expiresAt"].replace("Z", "+00:00")) - dt.datetime.now(dt.timezone.utc)).total_seconds()
        if not result["id"] or not result["lease"] or job.get("owner") != result["connectorId"] or job.get("generation") != config["generation"] or not 0 < remaining <= 90:
            raise ValueError("任务归属、配置版本或租约不合法")
        action = job["action"]
        incident = job.get("incident") or {}
        if action != "collect":
            expected = target(config, 1)
            if any(incident.get("target", {}).get(k) != expected[k] for k in ("kind", "namespace", "name", "uid")):
                raise ValueError("任务目标不属于本地模拟对象")
        if action == "verify":
            with database(config) as db:
                before = world(db)
            time.sleep(1)
            with database(config) as db:
                after = world(db)
            if before["fault"] or after["fault"] or before["version"] != after["version"]:
                raise ValueError("两次模拟恢复检查未通过")
            result.update(ok=True, detail="[脚本模拟] 连续两次检查通过，模拟选择器与就绪端点恢复正常")
            return result, after["version"]
        with database(config) as db:
            state = world(db)
            if action == "collect":
                result["snapshot"] = collect(config, state)
                result["detail"] = "[脚本模拟] 已返回资源、事件与端点证据"
            elif action in ("service_backends", "target_events"):
                obj = target(config, state["version"])
                if incident["target"] != obj:
                    raise ValueError("补查对象版本已变化")
                detail = "[模拟] 没有新的目标事件"
                if action == "service_backends":
                    matched = 0 if state["fault"] else 2
                    detail = f"[模拟] 当前选择器匹配 {matched} 个就绪 Pod；基线匹配 2 个就绪 Pod。"
                result["check"] = {"target": obj, "evidence": {"id": "check-" + action + "-" + obj["uid"],
                    "source": "Script Mock / " + action, "summary": action, "at": now(), "detail": detail}}
            elif action == "plan":
                result["plan"] = plan(config, state, incident)
                result["detail"] = "[脚本模拟] 已按健康基线生成限定字段修复计划"
            elif action == "execute":
                if job.get("automatic") and not config["scope"].get("automatic"):
                    raise ValueError("本地配置未授权自动执行")
                fresh = plan(config, state, incident)
                if job.get("plan") != fresh or job.get("planHash") != plan_hash(fresh):
                    raise ValueError("计划、对象版本或计划哈希不匹配")
                if db.execute("SELECT 1 FROM executions WHERE job=?", (job["id"],)).fetchone():
                    raise ValueError("该任务曾执行过，拒绝重复写入")
                db.execute("INSERT INTO executions VALUES (?,?,?)", (job["id"], job["planHash"], now()))
                db.execute("UPDATE world SET fault=0,version=version+1,verified=0 WHERE id=1")
                result["detail"] = "[脚本模拟] 已恢复 selector app=payment，仅修改本地模拟状态"
            else:
                raise ValueError("不支持的动作；脚本不会执行任意命令")
        result["ok"] = True
    except (ValueError, KeyError, TypeError) as error:
        result["error"] = str(error)
    return result, None


def run(config, verbose):
    credential = token(config, "token")
    name = config["scope"]["targets"][0]["name"]
    connection = {"id": connector_id(config), "namespace": config["scope"]["namespace"], "provider": "demo", "generation": config["generation"], "baselines": {"Service/" + name: "ready"}}
    with single_connector(config):
        log(f"轮询 {config['serverUrl']}/connector/poll；Ctrl+C 停止")
        log("仅收发模拟数据。默认健康，运行 send_alert.py --inject 注入故障")
        connected = False
        while True:
            code, reply = post(config, "/connector/poll", {"connection": connection}, credential)
            if not connected:
                log(f"心跳成功 HTTP {code}；执行端 {connection['id']}"); connected = True
            job = reply.get("job")
            if not job:
                time.sleep(1); continue
            log(f"RECV {job['action']}，任务 {job['id']}")
            if verbose:
                dump(job)
            result, verified_version = handle(config, job)
            if verbose:
                dump(result)
            # Retry delivery of the SAME result, never repeat the mutation.
            for attempt in range(3):
                try:
                    post(config, "/connector/result", result, credential)
                    break
                except ValueError:
                    if attempt == 2:
                        raise
                    time.sleep(1)
            if verified_version is not None and result["ok"]:
                with database(config) as db:
                    db.execute("UPDATE world SET verified=? WHERE id=1 AND version=? AND fault=0", (verified_version, verified_version))
            log(f"SEND {job['action']} 结果已接收，ok={result['ok']}；{result.get('error') or result['detail']}")
            time.sleep(0.25)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--config", default=DEFAULT_CONFIG)
    parser.add_argument("--verbose", action="store_true", help="打印任务/结果 JSON，隐藏令牌与 lease")
    args = parser.parse_args()
    try:
        run(load_config(args.config), args.verbose)
    except KeyboardInterrupt:
        log("模拟执行端已停止，状态与执行记录保留")
    except (ValueError, OSError) as error:
        log(str(error)); raise SystemExit(1)
