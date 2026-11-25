<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, ref } from 'vue'
import { Activity, ArrowRight, Check, CircleAlert, Clock3, Code2, Download, Loader2, Play, RefreshCw, ShieldCheck, Square, X } from 'lucide-vue-next'
import type { AgentRequest, AgentRun, Incident, State } from '../types'
import ModalLayer from './ModalLayer.vue'
import EmptyState from './EmptyState.vue'

const props = defineProps<{ state: State | null; api: (path: string, body?: unknown) => Promise<any>; refresh: (manual?: boolean) => Promise<boolean>; connectionError: string }>()
const emit = defineEmits<{ openIncident: [incident: Incident]; policies: [] }>()
const selectedID = ref(sessionStorage.getItem('kubepilot-agent-run') || '')
const workload = ref('')
const goal = ref('分析异常原因，给出处置计划，并在授权后验证恢复。')
const mode = ref<AgentRequest['mode']>('approval')
const pending = ref('')
const error = ref('')
const confirming = ref<AgentRun | null>(null)
const runs = computed(() => [...(props.state?.agentRuns ?? [])].reverse())
const selected = computed(() => runs.value.find(r => r.id === selectedID.value) || runs.value[0])
const targets = computed(() => [...new Set([...(props.state?.snapshot?.resources.map(r => r.name) ?? []), ...(props.state?.incidents.map(i => i.target.name) ?? [])])].sort())
const live = (r: AgentRun) => ['running', 'cancelling'].includes(r.status)
const terminal = (r: AgentRun) => !['running', 'cancelling', 'awaiting_approval'].includes(r.status)
const running = computed(() => runs.value.some(live))
const model = computed(() => props.state?.settings.agentModel || 'rules')
const linkedIncident = computed(() => props.state?.incidents.find(i => i.id === selected.value?.incidentId))
const blocked = computed(() => {
  const run = selected.value
  if (!run || run.status !== 'awaiting_approval') return ''
  if (!props.state?.settings.writesEnabled) return '当前环境只读，不能执行变更。'
  if (!props.state.policies.some(p => p.action === run.plan?.action && p.enabled)) return '对应处置策略未启用。'
  if (run.approvalDue && Date.parse(run.approvalDue) < Date.now()) return '确认已过期，请重新创建任务。'
  if (linkedIncident.value?.executionCount) return '该事件已经执行过变更，请核对实际结果。'
  return ''
})
const labels: Record<string, string> = { running: '运行中', cancelling: '正在停止', awaiting_approval: '等待确认', completed: '分析完成', recovered: '已验证恢复', needs_attention: '需人工核查', failed: '任务失败', cancelled: '已取消', interrupted: '已中断' }
const tools: Record<string, string> = { inspect_cluster: '采集集群状态', model_decision: '模型选择下一步', read_evidence: '读取目标证据', search_runbooks: '检索排障知识', submit_diagnosis: '提交结构化诊断', build_repair_plan: '生成受控计划', execute_and_verify: '执行变更并验证恢复', refresh_after_repair: '重新采集集群状态', verify_recovery: '验证已有事件' }
const modes = { observe: '只读分析', approval: '确认后处置', automatic: '按策略自动处置' }
const time = (v: string) => new Date(v).toLocaleString('zh-CN', { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', second: '2-digit' })
const json = (value: unknown) => JSON.stringify(value, null, 2)
function confirmPlan() { if (selected.value) confirming.value = JSON.parse(JSON.stringify(selected.value)) }
function select(r: AgentRun) { selectedID.value = r.id; sessionStorage.setItem('kubepilot-agent-run', r.id); error.value = '' }
async function action(key: string, fn: () => Promise<void>) {
  if (pending.value) return
  pending.value = key; error.value = ''
  try { await fn(); await props.refresh() }
  catch (e) { error.value = (e as Error).message }
  finally { pending.value = '' }
}
async function start() {
  if (!workload.value.trim() || !props.state) return
  await action('start', async () => {
    const run: AgentRun = await props.api('/agent/runs', { namespace: props.state!.settings.namespace, workload: workload.value.trim(), goal: goal.value.trim(), mode: mode.value, maxSteps: 8 })
    select(run)
  })
  if (!error.value && innerWidth <= 900) {
    await nextTick()
    document.getElementById('agent-run-detail')?.scrollIntoView({ block: 'start', behavior: matchMedia('(prefers-reduced-motion: reduce)').matches ? 'instant' : 'smooth' })
  }
}
async function approve() {
  const run = confirming.value
  if (!run) return
  await action('approve', async () => { await props.api('/agent/runs/' + run.id + '/approve', { planHash: run.planHash }); confirming.value = null })
}
async function cancel() {
  const run = selected.value
  if (run) await action('cancel', async () => { await props.api('/agent/runs/' + run.id + '/cancel', {}) })
}
function download() {
  if (!selected.value) return
  const url = URL.createObjectURL(new Blob([json(selected.value)], { type: 'application/json' }))
  const anchor = document.createElement('a'); anchor.href = url; anchor.download = selected.value.id + '.json'; anchor.click()
  setTimeout(() => URL.revokeObjectURL(url), 1000)
}
let timer: ReturnType<typeof setInterval> | undefined
let polling = false
onMounted(() => {
  timer = setInterval(async () => {
    if (polling || pending.value || !running.value || document.visibilityState !== 'visible') return
    polling = true; try { await props.refresh() } finally { polling = false }
  }, 1200)
})
onUnmounted(() => clearInterval(timer))
</script>

<template>
  <div class="agent-workspace page-view">
    <header class="page-heading"><div><p class="eyebrow">KUBEPILOT · AGENT RUNTIME</p><h1>Agent 任务</h1><p class="page-description">给出目标，让证据、工具与验证形成完整的排障过程。</p></div><button class="button secondary" :disabled="!!pending" @click="refresh(true)"><RefreshCw :size="15" />同步状态</button></header>
    <div class="agent-provenance"><span class="agent-engine"><Activity :size="15" />{{ model === 'rules' ? '规则编排 · 模型未接入' : model }}</span><span>{{ state?.settings.mode === 'demo' ? '独立演示环境' : 'Kubernetes 集群' }} · {{ state?.settings.namespace || '连接中' }}</span></div>
    <p v-if="connectionError || error" class="notice error" role="alert"><CircleAlert :size="16" />{{ error || connectionError }}</p>
    <div class="agent-layout">
      <aside class="agent-request-panel">
        <form class="agent-request-form" @submit.prevent="start">
          <div class="agent-section-heading"><h2>新建排障任务</h2><span>NEW RUN</span></div>
          <label for="agent-workload">目标工作负载</label><input id="agent-workload" v-model="workload" list="agent-targets" placeholder="例如 checkout-api" :disabled="!!pending" required maxlength="253" autocomplete="off" /><datalist id="agent-targets"><option v-for="name in targets" :key="name" :value="name" /></datalist>
          <label for="agent-goal">任务描述</label><textarea id="agent-goal" v-model="goal" rows="3" maxlength="600" :disabled="!!pending" />
          <label for="agent-mode">执行方式</label><select id="agent-mode" v-model="mode" :disabled="!!pending"><option value="approval">确认后处置</option><option value="observe">只读分析</option><option value="automatic">按策略自动处置</option></select>
          <p class="agent-form-note">{{ mode === 'automatic' ? '仅自动执行已启用的自动策略；模型失败会停止自动写入。' : mode === 'observe' ? '采集、分析和生成建议，不提交资源变更。' : '先展示具体计划，由你确认后再重新校验并执行。' }}</p>
          <button class="button primary full-width" :disabled="!state || !workload.trim() || !!pending || running"><Loader2 v-if="pending === 'start'" :size="15" class="spin" /><Play v-else :size="14" />{{ running ? '已有任务正在运行' : '启动 Agent' }}</button>
        </form>
        <div class="agent-history-heading"><h2>最近任务</h2><span>{{ runs.length }}</span></div>
        <nav class="agent-history" aria-label="Agent 任务历史"><button v-for="run in runs" :key="run.id" :class="{ selected: selected?.id === run.id }" :aria-current="selected?.id === run.id ? 'true' : undefined" @click="select(run)"><span><strong>{{ run.request.workload }}</strong><small>{{ time(run.startedAt) }}</small></span><span class="pill" :class="run.status">{{ labels[run.status] || run.status }}</span></button></nav>
        <p v-if="!runs.length" class="agent-history-empty">启动任务后，运行记录会保存在这里。</p>
      </aside>
      <section id="agent-run-detail" class="agent-detail-panel" aria-label="Agent 运行详情">
        <template v-if="selected">
          <header class="agent-run-heading"><div><p class="eyebrow">{{ selected.id }}</p><h2>{{ selected.request.workload }}</h2><p>{{ modes[selected.request.mode] }} · {{ selected.trigger === 'scheduler' ? '定时触发' : '手动触发' }}</p></div><span class="pill" :class="selected.status"><Loader2 v-if="live(selected)" :size="12" class="spin" />{{ labels[selected.status] || selected.status }}</span></header>
          <p class="agent-run-summary" role="status">{{ selected.summary }}</p>
          <div class="agent-run-meta"><span>模型调用 <strong>{{ selected.modelCalls }} 次</strong></span><span>输入 / 输出 tokens <strong>{{ selected.inputTokens }} / {{ selected.outputTokens }}</strong></span><span :class="{ 'warning-text': selected.fallback }">{{ selected.fallback ? '规则降级 · ' : '' }}{{ selected.engine }}</span></div>
          <div class="agent-run-actions"><button class="text-button" @click="download"><Download :size="14" />导出运行记录</button><button v-if="linkedIncident" class="text-button" @click="emit('openIncident', linkedIncident)">查看关联事件<ArrowRight :size="14" /></button><button v-if="!terminal(selected)" class="text-button agent-stop" :disabled="!!pending || selected.status === 'cancelling'" @click="cancel"><Square :size="11" />停止任务</button></div>
          <section v-if="selected.diagnosis" class="agent-diagnosis"><h3>诊断结论</h3><p>{{ selected.diagnosis.conclusion }}</p><div class="agent-evidence-refs"><code v-for="reference in selected.diagnosis.evidenceIds" :key="reference">{{ reference }}</code></div><p class="agent-limitations">{{ selected.diagnosis.limitations }}</p></section>
          <section v-if="selected.plan" class="agent-plan"><div class="agent-section-heading"><h3>处置计划</h3><span>{{ selected.plan.action }}</span></div><p>{{ selected.plan.summary }}</p><div class="diff"><div><span>当前配置</span><pre>{{ selected.plan.before }}</pre></div><div><span>目标配置 · 可信基线</span><pre>{{ selected.plan.after }}</pre></div></div>
            <template v-if="selected.status === 'awaiting_approval'"><p class="agent-approval-note"><Clock3 :size="14" />确认有效期至 {{ time(selected.approvalDue!) }}</p><p v-if="blocked" class="notice warning">{{ blocked }}<button @click="emit('policies')">查看策略</button></p><button class="button primary" :disabled="!!pending || !!blocked || running" @click="confirmPlan"><ShieldCheck :size="15" />核对并执行计划</button></template>
          </section>
          <div class="agent-section-heading agent-trace-heading"><h3>工具调用记录</h3><span>{{ selected.steps.length }} STEPS</span></div>
          <ol class="agent-trace"><li v-for="(step, index) in selected.steps" :key="step.id"><span class="agent-step-marker" :class="step.status"><Loader2 v-if="step.status === 'running'" class="spin" :size="14" /><Check v-else-if="step.status === 'completed'" :size="13" /><CircleAlert v-else :size="13" /></span><details :open="step.status === 'running' || step.status === 'failed'"><summary><span><strong>{{ tools[step.tool] || step.tool }}</strong><code>{{ step.tool }}</code></span><span>{{ step.status === 'running' ? '运行中' : step.durationMs + ' ms' }}<small>{{ String(index + 1).padStart(2, '0') }}</small></span></summary><div class="agent-step-body"><p v-if="step.error" class="error-text">{{ step.error }}</p><label>INPUT</label><pre>{{ json(step.arguments) }}</pre><label v-if="step.output">OUTPUT</label><pre v-if="step.output">{{ json(step.output) }}</pre></div></details></li></ol>
        </template>
        <EmptyState v-else title="从一个目标开始" description="Agent 会采集当前状态，调用证据与知识工具，生成可核对的诊断和处置计划。"><template #icon><Code2 :size="32" /></template><p class="agent-empty-note">每一步都有输入、输出和实际状态。尚未配置模型时，明确使用规则编排。</p></EmptyState>
      </section>
    </div>
    <ModalLayer :open="!!confirming" title-id="agent-approval-title" @close="confirming = null"><template v-if="confirming"><div class="modal-heading"><h2 id="agent-approval-title">确认本次处置计划</h2><button class="icon-button" aria-label="关闭计划确认" @click="confirming = null"><X :size="18" /></button></div><p class="modal-description">{{ confirming.request.namespace }} / {{ confirming.request.workload }}<br />{{ state?.settings.mode === 'demo' ? '本次修改独立演示资源。' : '本次将修改实际集群资源。' }}执行前仍会重新校验对象版本、基线及策略。</p><div class="diff"><div><span>当前配置</span><pre>{{ confirming.plan?.before }}</pre></div><div><span>目标配置</span><pre>{{ confirming.plan?.after }}</pre></div></div><p class="agent-hash">计划摘要 {{ confirming.planHash?.slice(0, 16) }}</p><p v-if="error" class="error-text" role="alert">{{ error }}</p><div class="modal-actions"><button class="button secondary" :disabled="!!pending" @click="confirming = null">取消</button><button class="button primary" :disabled="!!pending" @click="approve"><Loader2 v-if="pending" :size="14" class="spin" />确认执行此计划</button></div></template></ModalLayer>
  </div>
</template>

<style scoped>
.agent-workspace { max-width: 1500px; margin: 0 auto; }
.agent-provenance { display: flex; align-items: center; justify-content: space-between; gap: 14px; padding: 11px 14px; margin-bottom: 22px; border: 1px solid var(--line); background: #222126; font-size: 12px; color: var(--secondary); }
.agent-engine { display: flex; align-items: center; gap: 8px; color: #c3b9cc; overflow-wrap: anywhere; }
.agent-layout { display: grid; grid-template-columns: 302px minmax(0,1fr); align-items: start; border: 1px solid var(--line); background: var(--surface); }
.agent-request-panel { min-width: 0; position: sticky; top: 0; }
.agent-request-form { padding: 22px; }
.agent-section-heading { display: flex; justify-content: space-between; align-items: center; gap: 12px; margin-bottom: 20px; }
.agent-section-heading h2, .agent-section-heading h3 { font-size: 14px; font-weight: 500; }
.agent-section-heading > span { color: var(--muted); font-size: 10px; letter-spacing: .5px; }
.agent-request-form > label { display: block; color: var(--secondary); font-size: 12px; margin: 17px 0 8px; }
.agent-request-form input,.agent-request-form select,.agent-request-form textarea { width: 100%; min-width: 0; padding: 9px 11px; border: 1px solid #59565c; border-radius: 2px; color: var(--text); background: #171719; font-size: 13px; line-height: 1.7; }
.agent-request-form textarea { resize: vertical; min-height: 92px; max-height: 240px; }
.agent-form-note { margin: 12px 0 20px; color: var(--secondary); font-size: 12px; line-height: 1.9; min-height: 44px; }
.agent-history-heading { display: flex; align-items: center; gap: 10px; padding: 18px 22px 10px; border-top: 1px solid var(--line); }
.agent-history-heading h2 { font-size: 13px; font-weight: 500; }
.agent-history-heading > span { font-size: 10px; color: var(--muted); }
.agent-history { max-height: 410px; overflow: auto; padding: 0 8px 10px; }
.agent-history > button { display: flex; align-items: center; justify-content: space-between; width: 100%; gap: 8px; padding: 13px 12px; border: 1px solid transparent; text-align: left; }
.agent-history > button:hover { background: #242426; }
.agent-history > button.selected { background: #292a30; border-color: #515a72; }
.agent-history > button > span:first-child { flex: 1; min-width: 0; }
.agent-history strong { display: block; font-family: Consolas,monospace; font-size: 12px; font-weight: 400; overflow-wrap: anywhere; }
.agent-history small { display: block; color: var(--muted); font-size: 10px; margin-top: 5px; }
.agent-history .pill { font-size: 10px; padding: 3px 5px; }
.agent-history-empty { padding: 8px 22px 26px; font-size: 12px; color: var(--muted); line-height: 1.9; }
.agent-detail-panel { min-width: 0; min-height: 680px; padding: 27px 30px; border-left: 1px solid var(--line); background: #161616; }
.agent-detail-panel > .empty-state { min-height: 610px; }
.agent-empty-note { max-width: 410px; color: var(--muted); font-size: 12px; line-height: 1.9; }
.agent-run-heading { display: flex; justify-content: space-between; align-items: flex-start; gap: 15px; }
.agent-run-heading > div { min-width: 0; }
.agent-run-heading h2 { font-size: 27px; letter-spacing: -.3px; margin: 10px 0 6px; overflow-wrap: anywhere; }
.agent-run-heading p:last-child { font-size: 12px; color: var(--secondary); }
.agent-run-heading .eyebrow { font-size: 10px; }
.agent-run-summary { padding: 17px 0; color: #c6c4ca; font-size: 13px; line-height: 1.9; overflow-wrap: anywhere; }
.agent-run-meta { display: flex; flex-wrap: wrap; gap: 10px 25px; font-size: 11px; color: var(--muted); padding: 14px 0; border-top: 1px solid #393939; border-bottom: 1px solid #393939; }
.agent-run-meta strong { color: var(--secondary); font-weight: 400; margin-left: 8px; }
.agent-run-meta > span:last-child { margin-left: auto; overflow-wrap: anywhere; }
.agent-run-actions { display: flex; flex-wrap: wrap; align-items: center; gap: 10px 22px; margin-top: 11px; }
.agent-stop { margin-left: auto; color: #d7aaa3; }
.agent-diagnosis { margin-top: 27px; padding: 20px; border: 1px solid #514853; background: #242027; }
.agent-diagnosis > p { font-size: 14px; line-height: 1.9; margin-top: 12px; overflow-wrap: anywhere; }
.agent-evidence-refs { display: flex; flex-wrap: wrap; gap: 7px; margin-top: 14px; }
.agent-evidence-refs code { padding: 3px 6px; border: 1px solid #534c59; font-size: 10px; color: #c9bcd2; overflow-wrap: anywhere; }
.agent-diagnosis > .agent-limitations { font-size: 12px; color: #b9adb9; line-height: 1.9; }
.agent-plan { margin: 28px 0; }
.agent-plan .agent-section-heading { margin-bottom: 12px; }
.agent-plan > p { font-size: 12px; line-height: 1.9; color: var(--secondary); margin-bottom: 13px; }
.agent-plan .agent-approval-note { display: flex; align-items: center; gap: 7px; font-size: 11px; margin: 14px 0; color: #c4b28a; }
.agent-plan .notice { margin-top: 14px; }
.agent-trace-heading { margin-top: 30px; }
.agent-trace { list-style: none; padding: 0; margin: 0; }
.agent-trace > li { position: relative; display: flex; align-items: flex-start; gap: 14px; padding-bottom: 17px; }
.agent-trace > li:not(:last-child)::before { content: ""; position: absolute; top: 23px; bottom: 0; left: 11px; width: 1px; background: #46464a; }
.agent-step-marker { display: grid; place-items: center; width: 23px; height: 23px; margin-top: 3px; flex-shrink: 0; border: 1px solid #5c5c62; border-radius: 50%; background: #252528; color: #b0b0ba; }
.agent-step-marker.completed { border-color: #586c59; color: #abc6a8; }
.agent-step-marker.failed { border-color: #835852; color: #dda79d; }
.agent-trace details { min-width: 0; flex: 1; border: 1px solid #414147; background: #1e1e21; }
.agent-trace summary { display: flex; align-items: center; justify-content: space-between; gap: 10px; list-style: none; padding: 12px 14px; cursor: pointer; }
.agent-trace summary::-webkit-details-marker { display: none; }
.agent-trace summary::after { content: "+"; margin-left: 8px; color: var(--muted); font-size: 15px; }
.agent-trace details[open] summary::after { content: "−"; }
.agent-trace summary:hover { background: #26262a; }
.agent-trace summary > span:first-child { min-width: 0; flex: 1; }
.agent-trace summary strong { display: block; font-size: 13px; font-weight: 500; }
.agent-trace summary code { display: block; font-size: 10px; color: var(--muted); margin-top: 4px; overflow-wrap: anywhere; }
.agent-trace summary > span:last-child { font-size: 10px; color: var(--muted); text-align: right; white-space: nowrap; }
.agent-trace summary small { display: block; color: #7e7d86; font-size: 10px; margin-top: 6px; }
.agent-step-body { padding: 0 14px 14px; font-size: 12px; line-height: 1.8; }
.agent-step-body > label { display: block; color: var(--muted); font-size: 10px; letter-spacing: .8px; margin: 10px 0 7px; }
.agent-step-body pre { margin: 0; padding: 12px; max-height: 300px; overflow: auto; color: #c4c3cd; background: #131316; border: 1px solid #3a3940; font-size: 11px; white-space: pre-wrap; overflow-wrap: anywhere; }
.agent-hash { margin-top: 16px; color: var(--muted); font-family: Consolas,monospace; font-size: 11px; }
.pill.awaiting_approval,.pill.needs_attention { color: var(--amber); background: var(--amber-soft); }
.pill.running { color: #b9c9ef; background: #2c3446; }
.pill.cancelled,.pill.interrupted { color: #c0b7c5; background: #302b34; }
@media(max-width:1250px) { .agent-layout { grid-template-columns: 270px minmax(0,1fr); } .agent-request-form { padding: 18px; } .agent-detail-panel { padding: 23px; } }
@media(max-width:900px) { .agent-layout { grid-template-columns: 1fr; } .agent-request-panel { position: static; } .agent-detail-panel { border-left: 0; border-top: 1px solid var(--line); min-height: 340px; } .agent-detail-panel > .empty-state { min-height: 300px; } .agent-history { max-height: 170px; } .agent-form-note { min-height: 0; } .agent-provenance { align-items: flex-start; flex-direction: column; gap: 8px; } }
@media(max-width:680px) { .agent-detail-panel { padding: 20px 15px; } .agent-request-form { padding: 20px 16px; } .agent-run-heading h2 { font-size: 23px; } .agent-run-meta { gap: 9px; } .agent-run-meta > span { width: 100%; } .agent-run-meta > span:last-child { margin-left: 0; } .agent-diagnosis { padding: 14px; } .agent-trace > li { gap: 9px; } .agent-trace summary { padding: 11px; } .agent-run-actions { gap: 8px 14px; } .agent-stop { margin-left: 0; } .agent-run-heading .pill { font-size: 10px; } }
</style>
