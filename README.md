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
