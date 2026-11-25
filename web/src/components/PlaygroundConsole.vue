<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from 'vue'
import { Activity, AlignLeft, ArrowRight, Box, Boxes, ChevronRight, CircleAlert, ClipboardCheck, Code2, Copy, FileStack, FlaskConical, Link2, Loader2, Plus, RefreshCw, ScanLine, Search, Settings2, Upload, X, Download, Clock3 } from 'lucide-vue-next'
import type { ConsoleAction, ConsoleRequest, ConsoleResult, Incident, Runbook, State } from '../types'
import CodeEditor from './CodeEditor.vue'
import ModalLayer from './ModalLayer.vue'
import EmptyState from './EmptyState.vue'
import { hasOpenDialog } from '../composables/dialogStack'

const props = defineProps<{ state: State | null; runbooks: Runbook[]; result: ConsoleResult | null; busy: string; connectionError: string; runbooksError: string }>()
const emit = defineEmits<{
  run: [request: ConsoleRequest]; clear: []; environment: []; reset: []; inspect: [];
  openIncident: [incident: Incident]; export: []; refresh: []; loadRunbooks: []; notify: [message: string, error?: boolean];
}>()
const tasks = [
  { id: 'inspect', label: '检查集群运行状态', detail: '发现 Pod、事件与服务端点中的异常', tag: 'inspect' },
  { id: 'diagnose', label: '分析故障的可能原因', detail: '关联采集证据与可信健康基线', tag: 'diagnose' },
  { id: 'verify', label: '验证工作负载是否恢复', detail: '检查资源就绪与业务探测结果', tag: 'verify' },
  { id: 'inject', label: '复现一个典型故障', detail: '在独立演示环境中注入并巡检', tag: 'inject' },
] as const
const faultNames: Record<string, string> = { ImagePullBackOff: 'checkout-api', ServiceSelector: 'payment-service', ProbeFailure: 'inventory-api', CrashLoopBackOff: 'config-worker', OOMKilled: 'memory-worker', Pending: 'report-worker' }
const actionNames: Record<ConsoleAction, string> = { inspect: '集群巡检', diagnose: '故障诊断', verify: '恢复验证', inject: '故障注入' }
const contextDraft = ref('')
const questionDraft = ref('{\n  "action": "inspect"\n}')
const contextMode = ref('json')
const questionsMode = ref('cards')
const rightMode = ref<'guide' | 'result' | 'closed'>('guide')
const resultMode = ref<'readable' | 'json'>('readable')
const mobilePane = ref<'request' | 'response'>('request')
const allExamples = ref(false)
const settingsOpen = ref(false)
const targetsOpen = ref(false)
const recordsOpen = ref(false)
const targetSearch = ref('')
const fileInput = ref<HTMLInputElement>()
const workspace = ref<HTMLElement>()
const responsePane = ref<HTMLElement>()
const editorWidth = ref(42.35)
const dragging = ref(false)
let initialized = false
let activeResizeId: number | null = null
const demo = computed(() => props.state?.settings.mode === 'demo')
const context = computed(() => {
  try { const value = JSON.parse(contextDraft.value); return value && typeof value === 'object' && !Array.isArray(value) ? value as Record<string, unknown> : null }
  catch { return null }
})
const question = computed(() => {
  try { const value = JSON.parse(questionDraft.value); return value && typeof value === 'object' && !Array.isArray(value) ? value as Record<string, unknown> : null }
  catch { return null }
})
const action = computed(() => question.value?.action as ConsoleAction | undefined)
const selectedFault = computed(() => typeof question.value?.fault === 'string' ? question.value.fault : 'ImagePullBackOff')
const target = computed(() => {
  const matches = [...(props.state?.incidents ?? [])].reverse().filter(i => i.target.namespace === context.value?.namespace && i.target.name === context.value?.workload)
  return matches.find(i => i.status !== 'resolved') || matches[0]
})
const validationError = computed(() => {
  if (!context.value) return 'State 必须是有效的 JSON 对象。'
  if (Object.keys(context.value).some(key => !['namespace', 'workload'].includes(key))) return 'State 仅接受 namespace 和 workload 字段。'
  if (typeof context.value.namespace !== 'string' || typeof context.value.workload !== 'string') return 'namespace 和 workload 必须填写字符串。'
  if (props.state && context.value.namespace !== props.state.settings.namespace) return '命名空间必须与当前连接范围一致：' + props.state.settings.namespace
  if (!question.value) return 'Questions 必须是有效的 JSON 对象。'
  if (Object.keys(question.value).some(key => !['action', 'fault'].includes(key))) return 'Questions 仅接受 action 和 fault 字段。'
  if (!tasks.some(task => task.id === action.value)) return '请选择 inspect、diagnose、verify 或 inject 任务。'
  if (action.value === 'inject' && !demo.value) return '当前环境不支持演示故障注入。'
  if (action.value === 'inject' && !props.runbooks.some(book => book.type === question.value?.fault)) return '请选择一个已支持的故障场景。'
  if (action.value === 'inject' && context.value.workload !== faultNames[String(question.value?.fault)]) return '此演示场景的目标是 ' + faultNames[String(question.value?.fault)] + '，请重新载入场景。'
  if (['diagnose', 'verify'].includes(action.value ?? '') && !target.value) return '请在 State 中填写已有故障的工作负载，或从“＋”选择目标。'
  return ''
})
const targets = computed(() => {
  const byName = new Map<string, Incident>()
  for (const incident of [...(props.state?.incidents ?? [])].reverse()) {
    const key = incident.target.namespace + '/' + incident.target.name
    if (!byName.has(key) || (byName.get(key)?.status === 'resolved' && incident.status !== 'resolved')) byName.set(key, incident)
  }
  return [...byName.values()].filter(i => [i.target.name, i.title, i.target.namespace].join(' ').toLowerCase().includes(targetSearch.value.trim().toLowerCase()))
})
const records = computed(() => [...(props.state?.experiments ?? [])].reverse().slice(0, 100))
const resultJSON = computed(() => JSON.stringify(props.result, null, 2))
const guideExamples = computed(() => allExamples.value ? props.runbooks : props.runbooks.slice(0, 3))
const showResults = computed(() => rightMode.value === 'result')
const engineName = computed(() => props.state?.settings.modelConfigured ? props.state.settings.model : 'rules-engine')
const requestDisabled = computed(() => !!props.busy || !props.state || !!validationError.value)
const formatTime = (value: string) => new Date(value).toLocaleString('zh-CN', { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', second: '2-digit' })
const elapsed = (value?: number) => value === undefined ? '—' : value < 1000 ? value + ' ms' : (value / 1000).toFixed(1) + ' s'
const resultStatus = (value: string) => ({ new: '待诊断', planned: '待执行', manual: '待人工', resolved: '已恢复', detected: '已发现', recovered: '已恢复', injected: '已注入', reset: '已复位' } as Record<string, string>)[value] || value

function defaultContext() { return JSON.stringify({ namespace: props.state?.settings.namespace || 'kube-agent-lab', workload: '' }, null, 2) }
function initialize() {
  if (initialized) return
  initialized = true
  const params = new URLSearchParams(location.search)
  contextDraft.value = JSON.stringify({ namespace: params.get('namespace') || props.state?.settings.namespace || 'kube-agent-lab', workload: params.get('workload') || '' }, null, 2)
  if (tasks.some(task => task.id === params.get('action'))) questionDraft.value = JSON.stringify({ action: params.get('action'), ...(params.has('fault') ? { fault: params.get('fault') } : {}) }, null, 2)
}
initialize()
watch(() => props.state?.settings.namespace, (namespace, previous) => {
  if (!previous && namespace && !new URLSearchParams(location.search).has('namespace') && !context.value?.workload) contextDraft.value = defaultContext()
})
function chooseTask(value: ConsoleAction, fault?: string) {
  questionDraft.value = JSON.stringify({ action: value, ...(value === 'inject' ? { fault: fault || selectedFault.value } : {}) }, null, 2)
  if (value === 'inject') contextDraft.value = JSON.stringify({ namespace: props.state?.settings.namespace || 'kube-agent-lab', workload: faultNames[fault || selectedFault.value] || '' }, null, 2)
}
function chooseExample(book: Runbook) {
  chooseTask('inject', book.type)
  questionsMode.value = 'cards'; mobilePane.value = 'request'
  emit('notify', '已载入“' + book.title + '”，点击运行请求开始实验')
}
function setContextField(field: string, value: string) {
  if (context.value) contextDraft.value = JSON.stringify({ ...context.value, [field]: value }, null, 2)
}
function chooseTarget(incident: Incident) {
  contextDraft.value = JSON.stringify({ namespace: incident.target.namespace, workload: incident.target.name }, null, 2)
  targetsOpen.value = false
}
function clearRequest() {
  if (props.busy) return
  contextDraft.value = defaultContext(); questionDraft.value = '{\n  "action": "inspect"\n}'
  rightMode.value = 'guide'; mobilePane.value = 'request'; emit('clear')
  const url = new URL(location.href)
  for (const key of ['namespace', 'workload', 'action', 'fault']) url.searchParams.delete(key)
  history.replaceState(null, '', url)
  emit('notify', '已清空本次请求')
}
function formatJSON() {
  if (!question.value || !context.value) { emit('notify', 'JSON 格式有误，请先检查输入。', true); return }
  contextDraft.value = JSON.stringify(context.value, null, 2); questionDraft.value = JSON.stringify(question.value, null, 2)
  emit('notify', 'JSON 已格式化')
}
function run() {
  if (requestDisabled.value || !context.value || !action.value) return
  rightMode.value = 'result'; mobilePane.value = 'response'; resultMode.value = 'readable'
  emit('run', {
    action: action.value, namespace: String(context.value.namespace), workload: String(context.value.workload),
    ...(['diagnose', 'verify'].includes(action.value) ? { incidentId: target.value?.id } : {}),
    ...(action.value === 'inject' ? { fault: selectedFault.value } : {}),
  })
}
async function share() {
  if (validationError.value) { emit('notify', '请先修正请求内容，再复制链接。', true); return }
  const url = new URL(location.href); url.search = ''; url.hash = 'experiments'
  url.searchParams.set('namespace', String(context.value?.namespace || ''))
  if (context.value?.workload) url.searchParams.set('workload', String(context.value.workload))
  url.searchParams.set('action', action.value || 'inspect')
  if (action.value === 'inject') url.searchParams.set('fault', selectedFault.value)
  try { await navigator.clipboard.writeText(url.toString()); emit('notify', '请求链接已复制，打开后不会自动执行') }
  catch { emit('notify', '浏览器未允许复制链接。', true) }
}
async function importContext(event: Event) {
  const file = (event.target as HTMLInputElement).files?.[0]
  if (!file) return
  try {
    if (file.size > 1024 * 1024) throw new Error('文件不能超过 1 MB。')
    const value = JSON.parse(await file.text())
    if (!value || typeof value !== 'object' || Array.isArray(value) || typeof value.namespace !== 'string' || typeof value.workload !== 'string' || Object.keys(value).some(key => !['namespace', 'workload'].includes(key))) throw new Error('目标文件仅支持 namespace 和 workload 两个字符串字段。')
    contextDraft.value = JSON.stringify(value, null, 2)
    emit('notify', '排障目标已导入')
  } catch (error) { emit('notify', (error as Error).message, true) }
  finally { if (fileInput.value) fileInput.value.value = '' }
}
async function copyResult() {
  try { await navigator.clipboard.writeText(resultJSON.value); emit('notify', '运行结果已复制') }
  catch { emit('notify', '浏览器未允许复制结果。', true) }
}
function showGuide() { rightMode.value = 'guide'; mobilePane.value = 'response'; nextTick(() => responsePane.value?.scrollTo({ top: 0 })) }
function keydown(event: KeyboardEvent) {
  if ((event.ctrlKey || event.metaKey) && event.key === 'Enter' && !event.repeat && !hasOpenDialog()) { event.preventDefault(); run() }
}
function paneKeydown(event: KeyboardEvent) {
  if (!['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) return
  event.preventDefault()
  mobilePane.value = event.key === 'Home' ? 'request' : event.key === 'End' ? 'response' : mobilePane.value === 'request' ? 'response' : 'request'
  const tabs = (event.currentTarget as HTMLElement).querySelectorAll<HTMLButtonElement>('[role="tab"]')
  tabs[mobilePane.value === 'request' ? 0 : 1]?.focus()
}
function resize(event: PointerEvent) {
  if (!dragging.value || !workspace.value || activeResizeId !== event.pointerId) return
  const bounds = workspace.value.getBoundingClientRect()
  editorWidth.value = Math.min(65, Math.max(30, (event.clientX - bounds.left) / bounds.width * 100))
}
function startResize(event: PointerEvent) {
  if (event.button !== 0) return
  event.preventDefault(); activeResizeId = event.pointerId; dragging.value = true
  ;(event.currentTarget as HTMLElement).setPointerCapture(event.pointerId)
}
function endResize() { dragging.value = false; activeResizeId = null }
function resizeKey(event: KeyboardEvent) {
  if (event.key === 'ArrowLeft' || event.key === 'ArrowRight') { event.preventDefault(); editorWidth.value = Math.max(30, Math.min(65, editorWidth.value + (event.key === 'ArrowLeft' ? -2 : 2))) }
  if (event.key === 'Home') { event.preventDefault(); editorWidth.value = 42.35 }
}
async function fromSettings(action: 'environment' | 'reset' | 'records') {
  settingsOpen.value = false; await nextTick()
  if (action === 'records') recordsOpen.value = true
  else if (action === 'environment') emit('environment')
  else emit('reset')
}
watch(() => props.result, result => { if (result) { rightMode.value = 'result'; nextTick(() => responsePane.value?.scrollTo({ top: 0 })) } })
onMounted(() => window.addEventListener('keydown', keydown))
onUnmounted(() => window.removeEventListener('keydown', keydown))
</script>

<template>
  <section ref="workspace" class="playground-workspace" :class="{ dragging, 'mobile-response': mobilePane === 'response' }" :style="{ '--editor-width': editorWidth + '%' }" aria-label="排障请求工作台">
    <div class="console-mobile-tabs" role="tablist" aria-label="工作台区域" @keydown="paneKeydown">
      <button role="tab" aria-controls="console-request-panel" :tabindex="mobilePane === 'request' ? 0 : -1" :aria-selected="mobilePane === 'request'" @click="mobilePane = 'request'">请求编辑</button><button role="tab" aria-controls="console-response-panel" :tabindex="mobilePane === 'response' ? 0 : -1" :aria-selected="mobilePane === 'response'" @click="mobilePane = 'response'">{{ props.result ? '运行结果' : '操作说明' }}<span v-if="props.result" class="mini-dot"></span></button>
    </div>
    <div id="console-request-panel" class="console-editor">
      <header class="console-toolbar">
        <div class="console-title"><FlaskConical :size="16" /><strong class="console-name">实验工作台</strong><span>Playground</span></div>
        <div class="console-tools"><button :disabled="!!busy" @click="clearRequest">Clear</button><button class="share-request" aria-label="复制请求链接" @click="share"><Link2 :size="14" />Share</button><button class="console-icon" aria-label="工作台设置" @click="settingsOpen = true"><Settings2 :size="16" /></button></div>
      </header>
      <section class="editor-section state-section">
        <div class="editor-section-heading"><h2>State <span class="section-translation">排障目标</span></h2><div class="editor-view-toggle" role="group" aria-label="目标编辑方式"><button :class="{ selected: contextMode === 'json' }" aria-label="JSON 目标编辑" :aria-pressed="contextMode === 'json'" @click="contextMode = 'json'"><AlignLeft :size="16" /></button><button :class="{ selected: contextMode === 'form' }" aria-label="表单目标编辑" :aria-pressed="contextMode === 'form'" @click="contextMode = 'form'"><Code2 :size="17" /></button></div></div>
        <CodeEditor v-if="contextMode === 'json'" v-model="contextDraft" label="排障目标 JSON" :disabled="!!busy" />
        <div v-else class="context-form"><p>选择当前连接范围中的排障目标。</p><label>namespace<input :value="context?.namespace || ''" aria-label="目标命名空间" :disabled="!context || !!busy" @input="setContextField('namespace', ($event.target as HTMLInputElement).value)" /></label><label>workload<input :value="context?.workload || ''" aria-label="目标工作负载" placeholder="例如 checkout-api；巡检时可留空" :disabled="!context || !!busy" @input="setContextField('workload', ($event.target as HTMLInputElement).value)" /></label><button class="text-button" @click="targetsOpen = true">从已发现的故障中选择<ArrowRight :size="14" /></button><p v-if="!context" class="error-text">请切回 JSON 视图修正格式。</p></div>
      </section>
      <section class="editor-section questions-section">
        <div class="editor-section-heading"><h2>Questions <span class="section-translation">执行任务</span></h2><div class="question-tools"><button :aria-label="questionsMode === 'cards' ? '编辑任务 JSON' : '选择任务类型'" @click="questionsMode = questionsMode === 'cards' ? 'json' : 'cards'"><Code2 :size="15" /></button><button @click="formatJSON">Format</button><span :class="{ 'error-text': validationError }" :title="validationError || 'JSON 有效'"><CircleAlert :size="13" />{{ validationError ? 1 : 0 }}</span></div></div>
        <CodeEditor v-if="questionsMode === 'json'" v-model="questionDraft" label="执行任务 JSON" :disabled="!!busy" />
        <div v-else class="task-editor">
          <div class="opening-code"><span>1</span><code>{</code></div>
          <div class="task-chooser"><p>选择一个任务，开始排障 <button @click="showGuide">Docs</button></p><div class="task-grid"><button v-for="task in tasks" :key="task.id" class="task-card" :class="{ selected: action === task.id }" :disabled="!!busy || (task.id === 'inject' && !demo)" :aria-pressed="action === task.id" @click="chooseTask(task.id)"><span class="task-tag">{{ task.tag }}</span><strong>{{ task.label }}</strong><em>{{ task.detail }}</em></button></div>
            <label v-if="action === 'inject'" class="fault-picker">故障场景<select :value="selectedFault" aria-label="故障场景" :disabled="!!busy" @change="chooseTask('inject', ($event.target as HTMLSelectElement).value)"><option v-for="book in runbooks" :key="book.type" :value="book.type">{{ book.title }} · {{ book.type }}</option></select></label>
            <button v-if="['diagnose', 'verify'].includes(action || '')" class="target-picker" @click="targetsOpen = true"><Boxes :size="14" />{{ target ? target.target.name : '选择已发现的故障目标' }}<ChevronRight :size="14" /></button>
          </div><div class="closing-code"><code>}</code></div>
        </div>
      </section>
      <div class="asset-bar"><button :disabled="!!busy" @click="fileInput?.click()"><Upload :size="15" /><span>导入排障目标 <small>JSON</small></span></button><input ref="fileInput" type="file" accept=".json,application/json" aria-label="导入排障目标文件" hidden @change="importContext" /><span class="asset-mode">{{ demo ? '模拟数据' : '集群数据' }}</span></div>
      <footer class="request-footer"><div><button class="console-icon" aria-label="选择已有故障目标" :disabled="!!busy" @click="targetsOpen = true"><Plus :size="17" /></button><button class="engine-badge" aria-label="查看诊断引擎配置" @click="emit('environment')">{{ engineName }}</button></div><button class="run-request" :disabled="requestDisabled" :title="validationError || '运行当前请求'" @click="run"><Loader2 v-if="busy" :size="14" class="spin" /><span>{{ busy ? 'Running…' : '运行请求' }}</span><kbd>Ctrl+↵</kbd></button></footer>
      <p v-if="validationError || connectionError" class="console-validation" role="alert"><CircleAlert :size="14" />{{ connectionError || validationError }}<button v-if="connectionError" @click="emit('refresh')">重新连接</button></p>
    </div>
    <div class="console-resizer" role="separator" aria-label="调整编辑区宽度" aria-orientation="vertical" :aria-valuenow="Math.round(editorWidth)" aria-valuemin="30" aria-valuemax="65" tabindex="0" @pointerdown="startResize" @pointermove="resize" @pointerup="endResize" @pointercancel="endResize" @lostpointercapture="endResize" @keydown="resizeKey" @dblclick="editorWidth = 42.35"></div>
    <aside id="console-response-panel" class="console-response" aria-label="说明与运行结果">
      <div class="response-topline"><span v-if="showResults">Response</span><div v-if="result && showResults"><button :class="{ selected: resultMode === 'readable' }" @click="resultMode = 'readable'">结果</button><button :class="{ selected: resultMode === 'json' }" @click="resultMode = 'json'">JSON</button><button class="console-icon" aria-label="复制运行结果" @click="copyResult"><Copy :size="14" /></button></div><button v-if="rightMode !== 'guide'" class="text-button" @click="showGuide">操作说明</button></div>
      <div ref="responsePane" class="response-scroll">
        <article v-if="rightMode === 'guide'" class="guide-panel">
          <button class="guide-close console-icon" aria-label="关闭操作说明" @click="rightMode = result ? 'result' : 'closed'"><X :size="15" /></button>
          <header class="guide-heading"><p>KUBEPILOT · V{{ state?.settings.version || '0.1.0' }} WORKSPACE</p><h2>让排障有据可循</h2></header>
          <div class="guide-announcement"><div><strong>从发现异常，到验证恢复</strong><p>连接集群状态与故障证据，让每一次处置都有清晰的过程。</p><button @click="emit('environment')">查看当前环境与诊断引擎<ArrowRight :size="13" /></button></div><div class="halftone-art" aria-hidden="true"><Boxes :size="160" :stroke-width=".65" /></div></div>
          <h3 class="guide-section-label">AGENT CAPABILITIES</h3>
          <div class="capability-grid">
            <button class="capability" @click="chooseTask('diagnose'); mobilePane = 'request'"><span class="capability-tag">Evidence</span><div class="capability-art"><FileStack :size="88" :stroke-width=".9" /><Activity :size="32" :stroke-width="1" /></div><strong>从证据出发</strong><p>关联事件、日志与资源状态，定位可能的故障原因</p></button>
            <button class="capability" @click="chooseTask('verify'); mobilePane = 'request'"><span class="capability-tag">Verification</span><div class="capability-art"><ScanLine :size="96" :stroke-width=".8" /><Box :size="44" :stroke-width="1.1" /></div><strong>让恢复可以验证</strong><p>校验处置边界，再检查工作负载与业务是否恢复</p></button>
          </div>
          <div class="guide-section-label examples-heading"><h3>MORE EXAMPLES</h3><button @click="allExamples = !allExamples">{{ allExamples ? '收起' : '全部场景' }}<ChevronRight :size="13" :class="{ rotated: allExamples }" /></button></div>
          <div v-if="runbooksError" class="guide-error"><CircleAlert :size="16" />{{ runbooksError }}<button class="text-button" @click="emit('loadRunbooks')">重试</button></div>
          <div v-else class="guide-examples"><button v-for="book in guideExamples" :key="book.type" :disabled="!demo || !!busy" :aria-label="'载入' + book.title + '场景'" @click="chooseExample(book)"><strong>{{ book.title }}</strong><span>{{ book.checks[0] }}</span><ArrowRight :size="14" /></button></div>
          <div class="guide-bottom"><button @click="recordsOpen = true"><Clock3 :size="14" />查看实验记录<span>{{ state?.experiments.length || 0 }}</span></button><span>{{ demo ? 'DEMO ENVIRONMENT' : 'KUBERNETES' }}</span></div>
        </article>
        <section v-else-if="showResults" class="console-result" aria-label="运行结果">
          <div v-if="busy" class="result-loading" role="status"><Loader2 :size="25" class="spin" /><h2>正在运行{{ actionNames[action || 'inspect'] }}…</h2><p>结果会在服务返回后显示。</p></div>
          <template v-else-if="result">
            <header class="result-heading"><div><p class="eyebrow">{{ result.action.toUpperCase() }} · {{ formatTime(result.at) }}</p><h2>{{ result.outcome === 'error' ? '请求未完成' : result.outcome === 'unconfirmed' ? '结果尚未确认' : actionNames[result.action] + '完成' }}</h2></div><span class="pill" :class="result.outcome === 'success' ? 'success' : result.outcome === 'error' ? 'failed' : 'warning'">{{ result.outcome === 'success' ? '已完成' : result.outcome === 'error' ? '失败' : '未确认' }}</span></header>
            <p class="result-message" :class="{ 'error-text': result.outcome === 'error' }">{{ result.message }}</p>
            <div class="result-meta"><span>namespace <strong>{{ result.request.namespace }}</strong></span><span>耗时 <strong>{{ elapsed(result.elapsedMs) }}</strong></span></div>
            <pre v-if="resultMode === 'json'" class="result-json">{{ resultJSON }}</pre>
            <template v-else>
              <template v-if="result.inspection"><div class="result-stats"><div><span>异常项</span><strong>{{ result.inspection.findings }}</strong></div><div><span>就绪 Pod</span><strong>{{ result.snapshot?.readyPods ?? '—' }}<small> / {{ result.snapshot?.pods ?? '—' }}</small></strong></div><div><span>巡检耗时</span><strong>{{ elapsed(result.inspection.durationMs) }}</strong></div></div><h3 class="result-section-title">发现的故障</h3><div v-if="result.incidents?.length" class="result-incidents"><button v-for="incident in result.incidents" :key="incident.id" @click="emit('openIncident', incident)"><CircleAlert :size="16" /><span><strong>{{ incident.title }}</strong><small>{{ incident.target.name }}</small></span><span class="pill" :class="incident.status">{{ resultStatus(incident.status) }}</span><ChevronRight :size="16" /></button></div><EmptyState v-else title="没有发现待处理故障" description="本次巡检范围内未发现新的异常。" /></template>
              <template v-if="result.incident"><div class="result-target"><Boxes :size="20" /><strong>{{ result.incident.target.name }}</strong><span class="pill" :class="result.incident.status">{{ resultStatus(result.incident.status) }}</span></div><h3 class="result-section-title">{{ result.incident.diagnosis ? '诊断结论' : '关联事件' }}</h3><p class="result-conclusion">{{ result.incident.diagnosis?.conclusion || result.incident.title }}</p><p v-if="result.incident.diagnosis?.limitations" class="result-limit">{{ result.incident.diagnosis.limitations }}</p><button class="button secondary" @click="emit('openIncident', result.incident)">查看证据与处置计划<ArrowRight :size="15" /></button></template>
            </template>
          </template>
          <EmptyState v-else title="等待运行结果" description="填写左侧目标并选择任务，然后点击“运行请求”。" />
        </section>
        <EmptyState v-else class="closed-guide" title="准备好开始了" description="运行一个请求，结果将显示在这里。"><template #icon><FlaskConical :size="27" /></template><button class="text-button" @click="showGuide">重新打开操作说明</button></EmptyState>
      </div>
    </aside>
  </section>

  <ModalLayer :open="settingsOpen" title-id="console-settings-title" @close="settingsOpen = false">
    <div class="modal-heading"><h2 id="console-settings-title">工作台设置</h2><button class="icon-button" aria-label="关闭工作台设置" @click="settingsOpen = false"><X :size="19" /></button></div>
    <p class="modal-description">当前引擎：{{ engineName }} · {{ demo ? '独立演示环境' : '真实集群' }}</p>
    <div class="console-settings-list"><button @click="fromSettings('environment')"><Settings2 :size="17" />环境与连接<ChevronRight :size="16" /></button><button @click="fromSettings('records')"><ClipboardCheck :size="17" />实验记录<ChevronRight :size="16" /></button><button :disabled="!demo || !!busy" @click="fromSettings('reset')"><RefreshCw :size="17" />复位演示环境<ChevronRight :size="16" /></button></div>
  </ModalLayer>
  <ModalLayer :open="targetsOpen" title-id="console-targets-title" @close="targetsOpen = false">
    <div class="modal-heading"><h2 id="console-targets-title">选择排障目标</h2><button class="icon-button" aria-label="关闭目标选择" @click="targetsOpen = false"><X :size="19" /></button></div><label class="search-field full-width"><Search :size="16" /><input v-model="targetSearch" data-autofocus placeholder="搜索工作负载或故障名称" aria-label="搜索排障目标" /></label>
    <div class="target-list"><button v-for="incident in targets" :key="incident.id" @click="chooseTarget(incident)"><Boxes :size="18" /><span><strong>{{ incident.target.name }}</strong><small>{{ incident.title }}</small></span><span class="pill" :class="incident.status">{{ resultStatus(incident.status) }}</span></button></div><EmptyState v-if="!targets.length" title="没有匹配的故障目标" description="可先运行一次集群巡检。" />
  </ModalLayer>
  <ModalLayer :open="recordsOpen" title-id="console-records-title" @close="recordsOpen = false">
    <div class="modal-heading"><h2 id="console-records-title">实验记录</h2><button class="icon-button" aria-label="关闭实验记录" @click="recordsOpen = false"><X :size="19" /></button></div><p class="modal-description">{{ demo ? '以下记录来自模拟环境，不代表真实集群性能。' : '以下记录以实际采集与验证结果为准。' }}</p>
    <div v-if="records.length" class="console-records"><article v-for="record in records" :key="record.id"><div><strong>{{ record.fault }}</strong><span class="pill" :class="record.status">{{ resultStatus(record.status) }}</span></div><time>{{ formatTime(record.at) }}</time><p>发现 {{ elapsed(record.detectionMs) }}<span>恢复 {{ elapsed(record.recoveryMs) }}</span></p></article></div><EmptyState v-else title="还没有实验记录" description="选择右侧故障场景，运行一次注入实验。" />
    <button class="button secondary full-width" :disabled="!records.length" @click="emit('export')"><Download :size="16" />导出全部实验记录</button>
  </ModalLayer>
</template>
