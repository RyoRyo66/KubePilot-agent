# KubePilot 0.3

KubePilot 接收 Alertmanager 通知，通过部署在业务命名空间里的 Connector 采集证据，由 OpenAI 兼容模型诊断、补采和独立复核。容器日志与 Kubernetes 事件在 Connector 内提取固定诊断信号；平台在入库和模型调用前再次筛选证据。完整日志留在集群内，未知文本显示为“无已识别信号”。现场核查、写入授权、资源版本检查和恢复验证由程序执行。平台采用 Node.js 24 与 SQLite，前端沿用 0.2 发布版的 Vue 应用。

## 在本机启动平台

在项目根目录运行以下命令。前端构建需要 Node.js 24 和 npm。后端只使用 Node.js 内置模块。

```powershell
npm.cmd --prefix web ci
npm.cmd --prefix web run build
npm.cmd start
```

浏览器打开 `http://127.0.0.1:18081`，注册账号并创建个人工作空间。`npm.cmd run lab` 在 18080 端口启动另一个独立的单机实验台。模拟故障和重置操作只对模拟资源生效。`npm.cmd test` 使用临时目录运行回归测试。

## 接入实际集群

1. 在「接入告警」填写集群可以访问的平台 origin，生成接收配置，合并到现有 Alertmanager 的 receivers 和路由。跨网络部署使用 HTTPS。收到 resolved 通知仅更新告警，业务恢复需要 Connector 回读。
2. 在「连接模型」填写 API 基地址、模型名和密钥。平台使用真实工具调用测试连接。只有相同基地址可以沿用已存密钥。管理员可通过 `PLATFORM_TRUSTED_MODEL_URL` 明确指定一个自托管模型基地址。使用私网域名时还需将服务的精确 IP 填入 `PLATFORM_TRUSTED_MODEL_IPS`（多个地址用英文逗号分隔）。
3. 在「连接执行端」填写命名空间、实际存在的资源、健康检查地址、允许的动作和新版 Connector 镜像。填写受信仓库中本次发布的完整镜像名及不可变摘要（形如 `registry.example.com/team/kubepilot-connector@sha256:…`）。已有工作空间若使用旧标签，请重新登记并应用新清单，恢复心跳。按页面生成的清单部署。需要可用的默认 StorageClass 或已绑定的同名 PVC。Connector 主动向平台轮询，平台无需保存集群 kubeconfig。

以有仓库权限的终端构建、推送镜像。请将示例地址替换为节点能拉取的仓库，私库拉取凭证由集群管理员配置。

```sh
docker build --target connector -t registry.example.com/team/kubepilot-connector:0.3.1-r1 .
docker push registry.example.com/team/kubepilot-connector:0.3.1-r1
# 从 push 输出复制完整 digest，与仓库名拼成 repo@sha256:…，填入平台并下载新清单。
kubectl --context YOUR_TEST_CONTEXT apply -f downloaded-connector.json
```

业务检查示例为 `http://demo-web.kube-agent-lab.svc/`。Connector 从相同命名空间的 Service 获取 ClusterIP，核对 DNS 后才发送请求。对另有登记的固定业务站点，管理员可在 Connector 设置 `CONNECTOR_PROBE_ORIGINS`。首次登记需要两次健康基线采样。基线和一次写入记录保存在 Connector 的 PVC。重新登记后需要应用新清单并重新建立基线。

## 从告警到恢复

平台按工作空间鉴别 Webhook，去重并触发巡检。Connector 定向读取授权对象、Pod、相关事件和日志。模型基于本次证据及检索出的知识段落进行初诊，可要求补充工作负载、事件、日志或业务端点检查。最多三轮诊断，引用的证据编号必须出现在本次输入中。

Connector 从可信健康基线生成恢复计划。第二次独立模型调用复核具体计划和证据。平台在执行前再次询问 Connector，核对计划摘要、资源清单、授权和期限。自动修复须通过独立复核并显式启用，复核要求人工确认时在页面核对后执行，复核拒绝时停止。Connector 先对绑定 UID 与 resourceVersion 的 JSON Patch 做服务端预检。预检通过后，在提交前持久登记本次写入。预检被拒绝时可以重新诊断；提交结果未知时保留登记，转人工核查。随后回读资源、检查就绪状态和业务 HTTP。超时、中断或状态改变会留下记录供人工核查。

连续两次采集到相同的健康配置、且业务检查通过后，Connector 才更新基线。支持恢复 Deployment 普通容器和初始容器镜像、探针，以及 Service 选择器。OOM、调度失败、配额拒绝、容器反复退出等场景可诊断、补采和转人工。恢复镜像需要可确认的不可变镜像引用。知识库支持中文检索、自建操作手册和证据/知识分开展示。

## 数据与版本升级

`data/platform-v3` 下保存 `kubepilot.db`、WAL 相关文件和 `storage.key`。工作空间配置、事件内容和自建知识文档使用 AES-256-GCM 加密。账号密码使用独立盐的 PBKDF2 摘要。备份应在服务停止后复制完整目录并限制读取权限。

0.2 升级使用操作员独占、导入期间只读的普通文件备份（目录和文件均不接受链接）。使用停机备份的副本，在离线机器导入到全新目录。原卷作为回滚数据。示例命令中的路径需替换。

```sh
node ops/import-v2.js /backup/kubepilot-v2 /srv/kubepilot/platform-v3
```

账号保留原密码摘要，工作空间保留归属、配置、告警、故障历史和自建知识。导入后重新登录、测试模型并部署 0.3 Connector。旧会话、待执行租约和待批准计划均失效。切换前用独立端口核对人数、空间归属和历史数据。不要将 0.2 程序指向新版目录。

## 部署与实验

`compose.platform.yaml` 用于 B/S 平台，`compose.yaml` 用于独立模拟实验台。平台容器以普通用户运行，Compose 将端口绑定在宿主机环回地址。在外部反向代理配置 HTTPS 和 `PLATFORM_PUBLIC_ORIGIN`。代理需要覆盖客户端提供的 X-Real-IP，仅将实际代理对端 IP 列入 `PLATFORM_TRUSTED_PROXIES`。Compose 默认关闭注册。首次创建账号时显式设置 `PLATFORM_REGISTRATION_CLOSED=false`，完成后恢复默认值并重建平台容器。`PLATFORM_INTERVAL_SECONDS` 控制定时巡检周期，默认 60 秒，告警可以提前触发巡检。

隔离实集群验收使用 `deploy/kind.yaml` 和 `ops/accept-kind.js`。必须使用专用集群名 `kubepilot-v3-accept` 及单独 kubeconfig。验收程序只接受该集群唯一的同名 control-plane 节点。它部署真实 Prometheus、Alertmanager、Blackbox Exporter、应用、平台和 Connector。模型使用清楚标记的确定性 HTTP 夹具。验收器保存实际对象 UID、版本、端点数、业务响应、告警记录、诊断与复核时间线。若验收失败，程序只在注入的选择器仍存在时撤销本次实验变更，并将人工清理标入报告。验收成功后保留工作区供查看。

```sh
kind create cluster --name kubepilot-v3-accept --config deploy/kind.yaml --kubeconfig /private/accept.kubeconfig
npm --prefix web ci
npm --prefix web run build
docker build -f Dockerfile.local -t kubepilot-v3:accept .
kind load docker-image kubepilot-v3:accept --name kubepilot-v3-accept
# 设置 ACCEPT_CONNECTOR_IMAGE 为上述已发布、验收节点可拉取的完整 repo@sha256:… 后执行。
node ops/accept-kind.js /private/accept.kubeconfig /private/kubepilot-accept
```

验收时节点需能获取 `node:24-bookworm-slim`、`prom/prometheus:v3.13.5`、`prom/alertmanager:v0.35.0` 和 `prom/blackbox-exporter:v0.29.1`。可事先拉取并用 `kind load docker-image` 加载。多架构镜像出现层缺失时，先用 `docker image save --platform linux/amd64 -o accept-images.tar` 加上所需镜像名导出节点架构，再用 `kind load image-archive accept-images.tar --name kubepilot-v3-accept` 加载。

`ACCEPT_IMAGE` 可覆盖平台镜像；Connector 必须提供完整镜像摘要，可单独通过 `ACCEPT_CONNECTOR_IMAGE` 指定。设置 `ACCEPT_REVIEW_GATE=true` 会再运行一次相同故障，核对暂停自动修复后写入次数为零，再由测试程序提交已复核计划的确认，最终保持自动修复暂停。

验收端口默认为 18183，可通过 `ACCEPT_PORT` 设置。端口转发与浏览器地址需一致。验收专用用户名和随机密码只写在私有输出目录的 `acceptance-account.json`，不要提交该文件。查看保留的页面可运行以下命令。

```sh
kubectl --kubeconfig /private/accept.kubeconfig --context kind-kubepilot-v3-accept -n kube-agent-lab port-forward --address 127.0.0.1 svc/platform 18183:18081
```

### 0.3.1 升级

平台和 Connector 配套升级。沿用各自的 0.3 数据卷，启动时自动补充 SQLite 字段和索引。已有工作空间需将执行端镜像更新到 0.3.1，再应用页面生成的清单，等待心跳与基线就绪。新增事件具备 90 天写入窗口，届满后 Connector 同时拒绝该事件的写入并可回收其写入记录。旧事件缺少可信创建时间，其防重复记录继续保留。

Connector 每 4 秒独立上报心跳，单次长轮询最多等待 5 秒。新协议心跳超过 16 秒可判离线，已有任务继续受原租约约束。已领取且提交状态不明的写操作需要核查集群现场。

### 0.3.1 修订 1

本修订沿用平台版本 0.3.1、Connector 协议 3 和原数据卷。构建平台与 Connector 时使用独立镜像标签 `0.3.1-r1`。已有执行端需要更新工作空间的镜像地址并应用重新生成的清单，使运行镜像、健康检查和 RBAC 同步更新。

健康检查默认监听集群 Pod 内的 `127.0.0.1:18086`。需要换端口时修改清单中的 `CONNECTOR_HEALTH_PORT`，存活和就绪探针读取同一环境变量。端口被占用时 Connector 等待重试。Service 探测支持 `<服务>.<命名空间>.svc` 和 `<服务>.<命名空间>.svc.cluster.local`，清单仅为范围内的业务 Service 追加读取权限。

平台在派发执行租约前持久登记一次写入尝试。任务在排队阶段暂停、取消或到期时仍可重新诊断。已领取任务的提交结果未知时保留登记，Connector 停止时为已登记 PATCH 保留原租约期限。
