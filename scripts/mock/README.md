# 用脚本给现有服务发消息、接收任务

这三个脚本直接连接现有平台（默认 `http://127.0.0.1:18081`），不创建网站或另一个平台。只需要 Python 3.10+，没有第三方依赖。以下命令在 **kube-agent 项目目录**执行；Linux 可把 `python` 换成 `python3`。

| 脚本 | 作用 |
|---|---|
| `send_alert.py` | 向 `/ingest/alertmanager` 发送 firing/resolved 告警，可先注入一个本地模拟故障 |
| `mock_connector.py` | 循环请求 `/connector/poll`，打印收到的任务，向 `/connector/result` 回传采集、计划、修复、验证结果 |
| `mock_model.py` | 可选：接收平台发来的模型请求，返回固定的诊断/复核工具响应；已有模型 API 时不需要运行 |

`common.py` 是共享辅助代码；`config.example.json` 是接入配置样例。脚本只修改本地模拟状态，不执行 kubectl、Shell 命令或真实集群变更。

## 先填一次接入配置

在用于测试的工作空间中完成现有接入设置：

1. **告警**：平台地址用 `http://127.0.0.1:18081`，允许本地 HTTP，生成接收配置。
2. **模型**：已有可用接口直接沿用；没有时按下一节启动可选模拟模型。
3. **执行端**：命名空间 `script-lab`；只授权一个 `Service/payment-service`；健康检查 URL 可填 `http://payment-service.script-lab.svc/health`；只选择“恢复服务选择器”。要自动走到修复，勾选“允许在上述范围内自动修复”；未勾选时平台停在待确认，仍可手动确认计划。生成部署清单。

第三步只用于获取服务端签发的凭证和授权配置，**本次不需要把 YAML 部署到 Kubernetes**。这里的命名空间和 Service 都由脚本模拟。

```powershell
Copy-Item .\scripts\mock\config.example.json .\scripts\mock\config.local.json
```

编辑 `config.local.json`：

- 从第三步 YAML 的 `stringData.connector.json` 复制完整 JSON，作为此文件的内容。这样 `serverUrl`、`token`、`generation`、`scope` 都能与平台一致。
- 在这个 JSON 的最外层补一个 `"webhookToken": "告警接收配置中的 credentials 值"` 字段。注意前一个字段后的逗号。
- `token` 是 **Connector 令牌**，`webhookToken` 是 **告警令牌**，不能互换。重新生成执行端清单后，需要同步更新 JSON 中的 token 和 generation。

本次示例只支持一个 Service 和 `restore_selector` 动作。若当前空间已经有在线 Connector，服务会拒绝另一身份接入；请使用测试配置，不同时运行真实 Connector 和此模拟脚本。

## 开始收发消息

**终端 1：接收服务任务并回传结果，保持运行。**

```powershell
python .\scripts\mock\mock_connector.py --verbose
```

没有任务时只发送心跳；收到任务时打印任务类型和 JSON。`lease` 等凭证在终端隐藏。按 Ctrl+C 停止。

**终端 2：注入故障，发送告警，等待恢复后再发送 resolved。**

```powershell
python .\scripts\mock\send_alert.py --inject --wait-recovery
```

`--inject` 将共享的本地模拟 Service 选择器改为 `app=payment-v2`，随后发送 Alertmanager v4 格式的告警。服务收到通知后触发采集，执行端脚本会依次收到：

```text
RECV collect  -> SEND 资源状态、Service/EndpointSlice 模拟证据
RECV service_backends -> SEND 选择器及后端对照（仅当模型选中该项）
RECV plan     -> SEND 恢复选择器的具体计划
RECV plan     -> SEND 执行前再次核对后的计划
RECV execute  -> SEND 本地模拟写入结果
RECV verify   -> SEND 连续两次模拟恢复检查结果
```

服务中的诊断和自动复核照常运行。脚本会检查对象身份、版本、授权动作、计划哈希和任务租约；执行记录保留，回传失败只重发同一份结果，不重新执行修复。恢复通知使用原告警的指纹和开始时间，因此更新原记录。

使用下方的模拟模型时，`mock-approve` 演示初诊后直接生成计划；`mock-investigate` 演示补查 Service 后端后生成计划；`mock-insufficient` 演示补查后仍缺少证据，保留事件并转人工。真实模型根据本次证据选择路径。

也可以完全手动分开操作：

```powershell
# 注入故障并发送 firing，立即返回
python .\scripts\mock\send_alert.py --inject --verbose

# 只重发同一条 firing，不再次注入故障
python .\scripts\mock\send_alert.py --verbose

# 等接收脚本显示 verify 成功后，再发送对应的恢复通知
python .\scripts\mock\send_alert.py --status resolved --verbose
```

只发告警、没有 `--inject` 时，不会凭告警内容凭空产生可修复故障。若等待超时或模型拒绝，脚本不会发送假的恢复通知；查看原有网页的故障记录与终端输出即可。

发送端和接收端应在同一台机器、使用同一个配置文件运行，二者共享本地模拟状态。`--config 路径` 可以指定其他配置文件。

## 可选：没有模型 API 时

额外开一个终端：

```powershell
python .\scripts\mock\mock_model.py
```

在原有平台的模型设置中填写：

```text
API 基地址：http://127.0.0.1:18083/v1
模型名称：mock-approve
API Key：mock-key
```

然后“测试连接并保存”。这会让真实平台发起真实工具调用，但响应内容是固定模拟值。把模型名称改成 `mock-reject` 可检查阻断分支，改成 `mock-manual` 可检查人工确认分支。模拟模型只监听本机，示例假设平台也在同一台电脑上运行。

## 数据与停止

配置和令牌保存在被 Git 忽略的 `config.local.json`。模拟状态、告警指纹和写入去重记录保存在 `.work/script-mock/`，两个脚本会按接入配置选择相同目录。不要为了重试已提交的任务而删除这些执行记录。

停止接收脚本和可选模型脚本均使用 Ctrl+C。停止发送端等待不会撤销已发送的告警或正在处理的任务；暂停自动修复仍使用平台原有的独立操作。

## 验证记录

2026-11-12 在独立测试数据目录中，用真实平台进程执行了 7 项协议检查：三脚本接入、拒绝重复执行端、完整自动恢复、消息输出脱敏、重复注入生成新事件、拒绝越权目标和过期租约、复核拒绝后不写入且不发送假的恢复通知。两轮恢复每轮只产生一次本地模拟写入。测试结束后已停止测试进程，没有修改日常平台的账号或接入配置。
