# 全链路模拟操作说明

演示入口：**http://localhost:18082**。原来的账号平台仍是 **http://127.0.0.1:18081**，两者使用独立数据目录与不同名称的会话 Cookie。

## 如何运行

在 `kube-agent` 项目目录执行：

```powershell
.\scripts\start-flow-demo.ps1
```

脚本首次运行会构建独立的演示平台和 Connector 二进制；缺少前端构建时会一并构建。代码更新后可以先停止，再加 `-Rebuild` 重新构建。页面打开后：

1. 点击 **进入演示工作空间**，不需要注册账号或填写密钥。
2. 点击 **注入故障并发送告警**。
3. 查看 8 个阶段的状态与时间；完成后应显示 **全链路验证通过**。
4. 在下方故障详情核对原始证据、诊断、独立复核及修复计划。通过“查看告警报文”和“导出本次记录”保存演示材料。
5. 点击 **再次注入并发送告警**，重复运行；每次产生新的告警与故障记录，历史记录保留。

停止模拟环境：

```powershell
.\scripts\stop-flow-demo.ps1
```

停止会关闭该演示所属的平台、Connector、模拟模型和控制入口，保留报告，不操作原来的 18081 服务。再次启动创建一个新的隔离演示数据目录。故障流程中止时，先导出记录，再停止和重新启动演示环境；不会为了重试而清除执行去重日志。

## 模拟哪种故障

目标是 `demo-lab` 命名空间内的 `Service/payment-service`。健康基线为 `selector: { app: payment }`。故障注入把模拟选择器改成 `app=payment-v2`，导致匹配不到健康 Pod、没有就绪端点。

只授权 `restore_selector` 一个恢复动作，并对这一个模拟对象开启受控自动修复。界面与服务端均限制演示接入配置变更，避免误填真实模型密钥或真实集群凭证。

| 阶段 | 实际经过的程序路径 | 检查结果 |
|---|---|---|
| 注入故障 | Connector `-demo -demo-fault ServiceSelector` | 读回模拟对象文件，确认故障确实存在 |
| 接收告警 | `POST /ingest/alertmanager` | 专用令牌鉴权，HTTP 202，告警已归档 |
| 现场采集 | 平台下发 collect，Connector 轮询领取 | 获得本次现场证据并生成新事件 |
| 模型诊断 | `triage_incident` 初诊；本场演示选择 `service_backends` 补查，再调用 `submit_diagnosis` | 展示模型选择的检查、实际执行结果和新增证据引用。初诊依据充分的事件直接进入计划阶段 |
| 自动复核 | 独立请求 `submit_review` | 检查证据和具体计划，绑定计划哈希 |
| 受控修复 | 固定动作、范围、UID、版本、基线、哈希和次数检查 | Connector 执行一次模拟写入，记录执行日志 |
| 恢复验证 | verify，再次 collect 读回 | 连续两次模拟检查通过，重新采集资源为 healthy |
| 告警恢复 | 投递同指纹、同 startsAt 的 resolved 通知 | 更新原告警为 resolved，接收计数为 2 |

模拟控制器会先采集健康状态，再注入故障并立即发送通知，保证这次采集发生在告警归档之后。进度来自真实接口响应、事件时间线、独立模型请求记录和执行后的读回结果，不按固定时长填充成功状态。接收告警只触发采集；告警内容不会直接变成修复命令。

告警恢复通知在独立业务恢复验证之后发送。平台本身继续把“通知已恢复”和“业务验证通过”分开记录。

## 哪些是真实运行，哪些是模拟

- **真实运行的部分**：Go 平台服务、Go Connector 进程、HTTP 接收与派发、账号鉴权、授权检查、诊断/复核的两次工具调用、一次执行限制、去重与持久化、事件与告警归档、页面查询和记录导出。
- **模拟的部分**：Kubernetes 对象、端点与日志、健康基线、资源变更、业务健康检查，以及模型诊断/复核内容。模型是本地确定性协议服务，不需要 API Key，不代表真实大模型诊断能力。
- **没有启动的服务**：Prometheus、Alertmanager、真实 Kubernetes 和真实模型服务。这里只发送符合 Alertmanager v4 格式的 Webhook 请求。

正常监控链路是 **Prometheus 告警规则 → Alertmanager 分组与路由 → 本平台 Webhook**，参见 [Prometheus 告警概述](https://prometheus.io/docs/alerting/latest/overview/) 和 [Alertmanager Webhook 配置](https://prometheus.io/docs/alerting/latest/configuration/#webhook_config)。本演示从最后的 Webhook 投递处模拟输入，不验证 Prometheus 规则是否正确或 Alertmanager 路由是否可达。

真实模型和真实集群验证在 18081 的个人空间完成，按 [平台三步接入说明](PLATFORM.md) 配置。模拟结果可以说明软件处理流程，不能当作论文中的真实集群修复率或大模型准确率。

## 运行文件与端口

| 项目 | 位置或端口 |
|---|---|
| 浏览器与 Webhook 入口 | `localhost:18082`，只监听 loopback |
| 演示平台内部端口 | `127.0.0.1:18086` |
| 模拟模型 | 动态选择的本机端口 |
| 演示二进制 | `bin/demo/` |
| 独立运行数据 | `.work/flow-demo/<时间戳-实例ID>/` |
| 本次报告 | 同一运行目录内的 `report-<运行ID>.json` |
| 启动/停止控制信息 | `.work/flow-demo/active.json`，包含本地停止凭证，不要公开 |
| 脚本日志 | `.work/flow-demo/runner.stdout.log`、`runner.stderr.log` |

默认不占用 18081，重复启动会返回已有演示地址。端口已被其他进程占用时拒绝借用；需要更改可运行 `start-flow-demo.ps1 -Port 18092 -BackendPort 18096`。控制器不会读取用户 kubeconfig；所有 Connector 子进程都显式指定 `-demo`。

运行演示验证（会在当前演示空间执行两轮模拟，建议使用新启动的空演示实例）：

```powershell
cd web
npm run test:simulation
```

如使用自定义端口，先设置 `$env:FLOW_DEMO_TEST_URL='http://localhost:18092'`。验证结果及 1440、768、390 宽度截图保存在 `docs/platform-review/simulation/`。
