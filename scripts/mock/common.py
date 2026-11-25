"""Standard-library helpers for the three standalone protocol scripts."""
import contextlib
import datetime as dt
import hashlib
import json
import os
from pathlib import Path
import re
import sqlite3
import sys
import urllib.error
import urllib.parse
import urllib.request

DEFAULT_CONFIG = Path(__file__).with_name("config.local.json")
ROOT = Path(__file__).resolve().parents[2]
for stream in (sys.stdout, sys.stderr):
    if hasattr(stream, "reconfigure"):
        stream.reconfigure(encoding="utf-8", errors="replace")


def now():
    return dt.datetime.now(dt.timezone.utc).isoformat(timespec="milliseconds").replace("+00:00", "Z")


def load_config(path):
    try:
        config = json.loads(Path(path).read_text(encoding="utf-8-sig"))
    except FileNotFoundError:
        raise ValueError("请先将 config.example.json 复制为 config.local.json，填写服务端生成的接入配置") from None
    url = urllib.parse.urlsplit(config.get("serverUrl", ""))
    if url.scheme not in ("http", "https") or not url.hostname or url.username or url.password or url.path not in ("", "/") or url.query or url.fragment:
        raise ValueError("serverUrl 必须是平台源地址，不包含路径、用户名或查询参数")
    if url.scheme == "http" and not config.get("allowHttp"):
        raise ValueError("本地 HTTP 测试请显式设置 allowHttp=true")
    scope = config.get("scope", {})
    targets = scope.get("targets", [])
    if len(targets) != 1 or targets[0].get("kind") != "Service" or scope.get("actions") != ["restore_selector"]:
        raise ValueError("本示例只模拟一个 Service；授权目标需为一个 Service，动作只选 restore_selector")
    for value in (scope.get("namespace", ""), targets[0].get("name", "")):
        if not re.fullmatch(r"[a-z0-9][a-z0-9.-]{0,252}", value):
            raise ValueError("命名空间或 Service 名称无效")
    if type(config.get("generation")) is not int or config["generation"] < 1:
        raise ValueError("generation 请使用当前部署清单中的值")
    config["serverUrl"] = config["serverUrl"].rstrip("/")
    return config


def token(config, key):
    value = config.get(key, "")
    if not isinstance(value, str) or len(value) < 32 or "REPLACE_" in value:
        raise ValueError(f"请在配置中填写有效的 {key}，两个接口的令牌不能混用")
    return value


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def post(config, route, value, credential):
    body = json.dumps(value, ensure_ascii=False).encode("utf-8")
    request = urllib.request.Request(config["serverUrl"] + route, data=body, method="POST", headers={"Content-Type": "application/json", "Authorization": "Bearer " + credential})
    # Do not forward credentials to a redirect or an environment-provided proxy.
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())
    try:
        with opener.open(request, timeout=8) as response:
            return response.status, json.load(response)
    except urllib.error.HTTPError as error:
        raw = error.read(16384)
        try:
            message = json.loads(raw).get("error", "请求被拒绝")
        except (ValueError, AttributeError):
            message = "请求被拒绝"
        raise ValueError(f"{route}: HTTP {error.code}，{message}") from None
    except (urllib.error.URLError, TimeoutError):
        raise ValueError(f"{route}: 服务不可达或请求超时") from None


def log(message):
    print(f"[{now()[11:19]}] {message}", flush=True)


def redacted(value):
    if isinstance(value, dict):
        return {k: ("<redacted>" if k.lower() in ("lease", "token", "webhooktoken", "authorization", "apikey") else redacted(v)) for k, v in value.items()}
    if isinstance(value, list):
        return [redacted(v) for v in value]
    return value


def dump(value):
    print(json.dumps(redacted(value), ensure_ascii=False, indent=2), flush=True)


def state_path(config):
    material = config["serverUrl"] + "|" + str(config["generation"]) + "|" + config.get("token", "")
    key = hashlib.sha256(material.encode()).hexdigest()[:20]
    return ROOT / ".work" / "script-mock" / key / "state.sqlite3"


@contextlib.contextmanager
def database(config):
    path = state_path(config)
    path.parent.mkdir(parents=True, exist_ok=True)
    connection = sqlite3.connect(path, timeout=10, isolation_level=None)
    try:
        connection.execute("CREATE TABLE IF NOT EXISTS world (id INTEGER PRIMARY KEY, version INTEGER NOT NULL, fault INTEGER NOT NULL, verified INTEGER NOT NULL, notification TEXT)")
        connection.execute("INSERT OR IGNORE INTO world VALUES (1, 1, 0, 0, NULL)")
        connection.execute("CREATE TABLE IF NOT EXISTS executions (job TEXT PRIMARY KEY, hash TEXT NOT NULL, at TEXT NOT NULL)")
        connection.execute("BEGIN IMMEDIATE")
        yield connection
        connection.commit()
    except BaseException:
        connection.rollback()
        raise
    finally:
        connection.close()


def world(connection):
    row = connection.execute("SELECT version,fault,verified,notification FROM world WHERE id=1").fetchone()
    return {"version": row[0], "fault": bool(row[1]), "verified": row[2], "notification": json.loads(row[3]) if row[3] else None}


def connector_id(config):
    return "script-mock-" + state_path(config).parent.name


def target(config, version):
    name = config["scope"]["targets"][0]["name"]
    return {"kind": "Service", "namespace": config["scope"]["namespace"], "name": name, "uid": "script-mock-uid-" + name, "version": str(version)}


def plan(config, state, incident):
    expected = target(config, state["version"])
    if not state["fault"] or incident.get("type") != "ServiceSelector" or incident.get("target") != expected:
        raise ValueError("模拟对象已经变化、异常不存在或事件目标不匹配，拒绝执行")
    # Field insertion order matches core.Plan/core.Target for Go's PlanHash.
    return {"action": "restore_selector", "summary": "[脚本模拟] 将 Service 选择器恢复到健康基线", "before": "selector: { app: payment-v2 }", "after": "selector: { app: payment }", "baselineId": "script-mock-baseline-" + expected["name"], "target": expected, "checks": ["限定模拟对象", "健康基线存在", "UID 和版本匹配", "一次写入", "恢复后验证"]}


def plan_hash(value):
    raw = json.dumps(value, ensure_ascii=False, separators=(",", ":"))
    for char, escaped in (("&", "\\u0026"), ("<", "\\u003c"), (">", "\\u003e"), ("\u2028", "\\u2028"), ("\u2029", "\\u2029")):
        raw = raw.replace(char, escaped)
    return hashlib.sha256(raw.encode("utf-8")).hexdigest()


@contextlib.contextmanager
def single_connector(config):
    """OS lock releases on exit; the sender can still access the SQLite state."""
    path = state_path(config).with_suffix(".lock")
    path.parent.mkdir(parents=True, exist_ok=True)
    with path.open("a+b") as lock:
        if path.stat().st_size == 0:
            lock.write(b"0"); lock.flush()
        lock.seek(0)
        try:
            if os.name == "nt":
                import msvcrt
                msvcrt.locking(lock.fileno(), msvcrt.LK_NBLCK, 1)
            else:
                import fcntl
                fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except OSError:
            raise ValueError("同一配置的模拟执行端已经运行，请先停止另一个终端") from None
        yield
