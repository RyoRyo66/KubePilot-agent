# 实验记录原则

正式对照实验需要在真实 kind / 虚拟机 Kubernetes 环境中执行，不能使用 Web 演示模式的数据代替。

目前提供故障注入和复位素材。正式 A/B/C 批量运行器、模型质量评价和统计分析尚未完成，不能宣称已经开展了180次实验。

## 拟定实验条件

- A：Kubernetes 原生控制器与自愈能力。
- B：固定规则诊断与预设动作。
- C：智能体诊断和工具流程，保持与 B 相同的可用证据、动作权限及业务恢复判据。
- 六类故障每类每组计划10次，共180次；正常状态、未知异常、服务超时与并发变更另行记录。
- 固定集群、应用、镜像 digest、模型、提示词、负载与观测窗口；每轮复位后检查业务健康再开始。

当前规则和 Ollama 增强模式共用确定性的修复执行器。正式评价时必须清楚说明各组真正不同的部分，不把相同执行逻辑包装为不同算法效果。

## 每轮至少记录

`run_id`、`group`、`fault_type`、`injected_at`、`detected_at`、`diagnosed_at`、`action_started_at`、`recovery_confirmed_at`、`ground_truth`、`diagnosis_correct`、`eligible_for_auto_repair`、`repair_success`、`unexpected_write_count`、`human_intervention`、`timeout`、`cluster_version`、`model_version`、`prompt_hash`、`evidence_path`。

自动修复成功率的分母为预先定义且满足前置条件的用例；同时报告全部事件的自动处理覆盖率。失败与超时不得从样本中悄悄移除。人工复位不计为自动修复，未实际验证业务恢复时不填写恢复成功。
