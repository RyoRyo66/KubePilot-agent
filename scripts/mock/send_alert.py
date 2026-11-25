"""Send one Alertmanager-format notification to the existing platform."""
import argparse
import json
import time
import uuid
from common import DEFAULT_CONFIG, database, dump, load_config, log, now, post, token, world


def send(config, status, inject=False, verbose=False):
    credential = token(config, "webhookToken")
    token(config, "token")  # Both scripts must address the same persisted mock state.
    with database(config) as db:
        state = world(db)
        notice = state["notification"]
        if inject:
            if status != "firing":
                raise ValueError("--inject 仅用于 firing 通知")
            if state["fault"]:
                raise ValueError("模拟故障已存在；重发同一条告警时去掉 --inject，避免覆盖正在处理的故障")
            notice = {"startsAt": now(), "fingerprint": "script-" + uuid.uuid4().hex}
            db.execute("UPDATE world SET fault=1,version=version+1,verified=0,notification=? WHERE id=1", (json.dumps(notice),))
            log("已注入本地模拟故障：selector app=payment-v2")
        elif status == "resolved":
            if not notice:
                raise ValueError("没有原始通知，无法发送对应的 resolved")
            if state["fault"] or state["verified"] != state["version"]:
                raise ValueError("执行端尚未确认恢复验证成功，暂不发送 resolved")
        elif not notice:
            notice = {"startsAt": now(), "fingerprint": "script-" + uuid.uuid4().hex}
            db.execute("UPDATE world SET notification=? WHERE id=1", (json.dumps(notice),))
    alert = {"status": status, "labels": {"alertname": "KubeServiceNoReadyEndpoints", "namespace": config["scope"]["namespace"], "service": config["scope"]["targets"][0]["name"], "severity": "critical", "simulation": "true"}, "annotations": {"summary": "[脚本模拟] Service 没有就绪端点", "description": "模拟选择器偏离健康基线，请执行端补采证据。"}, **notice}
    if status == "resolved":
        alert["endsAt"] = now()
    payload = {"version": "4", "receiver": "kubepilot", "status": status, "alerts": [alert]}
    code, reply = post(config, "/ingest/alertmanager", payload, credential)
    log(f"SEND {status} -> /ingest/alertmanager: HTTP {code}，指纹 {notice['fingerprint']}")
    if verbose:
        dump(payload); dump(reply)
    return notice


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--config", default=DEFAULT_CONFIG)
    parser.add_argument("--status", choices=("firing", "resolved"), default="firing")
    parser.add_argument("--inject", action="store_true", help="先修改共享的本地模拟状态，再发告警")
    parser.add_argument("--wait-recovery", action="store_true", help="等待执行端验证成功，再发送同一条告警的 resolved")
    parser.add_argument("--timeout", type=int, default=120)
    parser.add_argument("--verbose", action="store_true")
    args = parser.parse_args()
    if args.wait_recovery and args.status != "firing":
        parser.error("--wait-recovery 只能与 firing 配合")
    if args.timeout < 1 or args.timeout > 3600:
        parser.error("--timeout 需要为 1–3600 秒")
    config = load_config(args.config)
    notice = send(config, args.status, args.inject, args.verbose)
    if args.wait_recovery:
        log("等待服务诊断、复核、派发修复，以及执行端回传验证结果…")
        deadline = time.monotonic() + args.timeout
        while time.monotonic() < deadline:
            with database(config) as db:
                state = world(db)
            if state["notification"] != notice:
                raise ValueError("模拟通知已被另一次操作改变，停止等待")
            if not state["fault"] and state["verified"] == state["version"]:
                send(config, "resolved", verbose=args.verbose)
                return
            time.sleep(0.5)
        raise ValueError("等待超时；未发送恢复通知。请查看接收脚本和平台的复核/执行记录")


if __name__ == "__main__":
    try:
        main()
    except KeyboardInterrupt:
        log("已停止等待，已发送的通知及模拟状态保留")
    except (ValueError, OSError) as error:
        log(str(error)); raise SystemExit(1)
