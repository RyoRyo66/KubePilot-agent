# KubePilot

KubePilot 是一个用于 Kubernetes 应用故障诊断与受控修复的 Web 平台。它接收 Alertmanager 告警，通过集群内的 Connector 采集证据，结合工作空间知识库和支持工具调用的大模型逐轮排查。对已授权、具备健康基线的对象，平台可以复核修复计划，并在执行后检查业务是否恢复。

## 下载和启动

在 GitHub 仓库首页选择 **Code → Download ZIP**，解压到本机。也可以用 Git 克隆仓库，以便以后更新。准备好 Docker 和 Docker Compose 后，在包含 `compose.platform.yaml` 的目录运行以下命令。

```sh
docker compose --env-file .env.platform.example -f compose.platform.yaml up -d --build
```

首次构建需要联网下载基础镜像和依赖。浏览器打开 [本地 KubePilot](http://127.0.0.1:18081)，注册账号并创建个人工作空间。此时可以浏览平台。要接收真实告警并诊断故障，还需在「接入设置」配置集群可达的平台地址、模型 API 和 Connector。接入记录、账号和工作空间保存在 Docker 命名卷中。

停止本地平台：

```sh
docker compose --env-file .env.platform.example -f compose.platform.yaml down
```

## 接入集群

1. 将平台部署到集群能访问的 HTTPS 地址，在「接入告警」生成接收配置，交给 Alertmanager 使用。
2. 在「连接模型」填写 OpenAI 兼容接口地址、模型名称和 API Key，测试连接。
3. 从同一份源码构建 Connector 镜像，发布到节点能拉取的仓库。在「连接执行端」填写镜像、目标资源与业务健康检查地址，在目标集群应用平台生成的清单。

本地构建 Connector 镜像：

```sh
docker build -f Dockerfile.platform --target connector -t kubepilot-connector:local .
```

平台支持独立复核、范围校验和恢复验证。镜像、Service 选择器和健康检查配置可以按策略恢复；发布超时等复杂故障也可逐轮补查并转人工核查。部署清单包含工作空间的专用连接令牌，应由该工作空间的管理员保管。[平台接入与执行流程](docs/PLATFORM.md)

## 源码入口

| 目录 | 内容 |
| --- | --- |
| `cmd/kubepilot-platform` | Go 平台服务入口 |
| `cmd/kubepilot-connector` | 集群执行端入口 |
| `internal/platform` | 工作空间、告警、调查与修复编排 |
| `internal/core`、`internal/providers` | 证据校验、知识检索、模型及 Kubernetes 适配 |
| `web/src` | Vue 3 / TypeScript 前端 |
| `scripts/mock` | 模拟告警发送、Connector 轮询等接口交互 |

Go 开发者可在仓库根目录执行 `go test ./...`。要构建前端，使用 Node.js 24，在 `web` 下执行 `npm ci` 和 `npm run build`。[模拟脚本使用说明](scripts/mock/README.md)

## 演示与测试脚本

`web/scripts/` 下的 10 个 `.mjs` 文件由 Node.js 运行，用于本地演示和开发验证。正式部署的排障流程由 Go 平台和集群内的 Connector 执行。

**想看一次完整排障演示**，先在 Windows 安装 Go 1.24 或更高版本和 Node.js 24，再从仓库根目录执行以下 PowerShell 脚本。

```powershell
.\scripts\start-flow-demo.ps1
```

打开 [本地演示工作空间](http://localhost:18082)，依次点击「进入演示工作空间」和「注入故障并发送告警」，即可观察采集、诊断、独立复核、模拟修复和恢复验证。演示使用独立数据目录和模拟模型，结束时运行 `.\scripts\stop-flow-demo.ps1`。[全链路演示说明](docs/SIMULATION.md)

**按用途查找脚本**。下表命令均从 `web/` 目录运行。首次开发测试先执行 `npm ci` 和 `npm run build`；浏览器测试默认使用本机 Chrome。

| 脚本 | 用途 | 运行入口 |
| --- | --- | --- |
| [flow-demo.mjs](web/scripts/flow-demo.mjs) | 启动演示平台、模拟模型和演示用 Connector，供人在网页上操作 | `..\scripts\start-flow-demo.ps1` |
| [flow-demo-e2e.mjs](web/scripts/flow-demo-e2e.mjs) | 在浏览器中连续跑两次模拟排障，核对每次写入次数、恢复结果和报告导出 | `npm run test:simulation` |
| [platform-e2e.mjs](web/scripts/platform-e2e.mjs) | 在独立平台上测试注册、创建空间、三步接入，以及复核通过、拒绝和人工核查 | `npm run test:platform` |
| [knowledge-e2e.mjs](web/scripts/knowledge-e2e.mjs) | 启动临时平台，在网页上导入、搜索、移除知识库资料，检查小屏显示 | `node scripts/knowledge-e2e.mjs` |
| [accounts-maintenance-e2e.mjs](web/scripts/accounts-maintenance-e2e.mjs) | 测试命令行重置密码、旧会话失效，以及重启后工作空间是否保留 | `npm run test:accounts` |
| [e2e.mjs](web/scripts/e2e.mjs) | 测试早期实验版的页面导航、巡检、故障处置和操作记录 | `npm run test:e2e` |
| [console-e2e.mjs](web/scripts/console-e2e.mjs) | 测试实验版工作台的 JSON 编辑、请求执行、面板拖动和键盘操作 | `npm run test:console` |
| [agent-e2e.mjs](web/scripts/agent-e2e.mjs) | 测试实验版 Agent 读取证据、调用工具、等待确认和执行受控修复 | `npm run test:agent` |
| [model-fixture.mjs](web/scripts/model-fixture.mjs) | 为实验版 Agent 提供固定的模型工具调用响应，便于复现排障过程 | `node scripts/model-fixture.mjs` |
| [platform-model-fixture.mjs](web/scripts/platform-model-fixture.mjs) | 为 B/S 平台模拟模型连接、诊断和复核响应，平台测试和全链路演示会自行启动它 | `node scripts/platform-model-fixture.mjs`（单独调试时） |

不同测试需要的本地服务如下。

- **全链路测试**需要一个新启动的演示实例（默认 `localhost:18082`），也需要普通 B/S 平台在 `127.0.0.1:18081` 运行，以验证两个工作空间的登录会话相互隔离。手动观看演示只需启动演示实例。
- **平台、账号和知识库测试**会自行启动临时平台。先构建 `bin/kubepilot-platform.exe`；平台端到端测试还需要 `bin/kubepilot-connector.exe`。平台和账号测试默认分别占用 18084 和 18085 端口。[Windows 构建命令](docs/PLATFORM.md#复现验证)
- **三个实验版测试**（`test:e2e`、`test:console`、`test:agent`）会重置演示数据。先使用独立数据目录启动 `kubepilot` 的 demo 后端，再在测试终端设置 `$env:E2E_ALLOW_DEMO_RESET = '1'`。Agent 测试还需将该后端连接到 `model-fixture.mjs`。实验版后端和全链路演示默认都用 18082 端口，请分开运行。[实验版 Agent 启动步骤](docs/AGENT.md#验证结果)
