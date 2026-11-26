<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, ref, watch, type Component } from 'vue'
import { Activity, Boxes, LayoutDashboard, ClipboardCheck, Siren, ShieldCheck, ScrollText, FlaskConical, ChevronRight, RefreshCw, Play, Search, ArrowUpRight, Check, CheckCircle2, CircleAlert, Info, X, Clock3, ArrowRight, Download, Server, Network, Settings2, Loader2, LockKeyhole, MoreHorizontal, Copy, Command, ArrowDown, ArrowUp, CornerDownLeft, PanelLeftClose, PanelLeftOpen } from 'lucide-vue-next'
import type { State, Incident, Policy, Runbook, ConsoleRequest, ConsoleResult } from './types'
import ModalLayer from './components/ModalLayer.vue'
import SegmentedControl from './components/SegmentedControl.vue'
import TrendChart from './components/TrendChart.vue'
import EmptyState from './components/EmptyState.vue'
import BrandMark from './components/BrandMark.vue'
import PlaygroundConsole from './components/PlaygroundConsole.vue'
import AgentWorkspace from './components/AgentWorkspace.vue'
import { hasOpenDialog } from './composables/dialogStack'

type Page = 'overview' | 'inspections' | 'incidents' | 'policies' | 'audit' | 'experiments' | 'agent'
const nav = [
  { id: 'overview', name: '集群概览', icon: LayoutDashboard },
  { id: 'inspections', name: '自动巡检', icon: ClipboardCheck },
  { id: 'incidents', name: '故障事件', icon: Siren },
  { id: 'policies', name: '处置策略', icon: ShieldCheck },
  { id: 'audit', name: '操作审计', icon: ScrollText },
  { id: 'experiments', name: '实验工作台', icon: FlaskConical },
  { id: 'agent', name: 'Agent 任务', icon: Activity },
] as const
const desktopNav = [nav[0], nav[5], nav[6], nav[1], nav[2], nav[3], nav[4]]
const mobileNav = [nav[0], nav[5], nav[2]]
const moreNav = [nav[6], nav[1], nav[3], nav[4]]
const descriptions: Record<Page, string> = {
  overview: '掌握运行状态，让每一次排障都有据可循。',
  inspections: '持续观测集群，记录每一次异常发现。',
  incidents: '从异常信号出发，追踪故障处理的完整过程。',
  policies: '为自动处置定义清晰、可验证的执行边界。',
  audit: '每一次操作，都有可追溯的过程与结果。',
  experiments: '复现典型故障，观察从发现到恢复的完整链路。',
  agent: '将采集、知识检索、诊断与受控处置串成可追踪的任务。',
}
const statusLabels: Record<string, string> = { new: '待诊断', diagnosing: '诊断中', planned: '待执行', executing: '执行中', verifying: '验证中', resolved: '已恢复', manual: '待人工处理', completed: '已完成', failed: '失败', success: '成功', started: '已开始', submitted: '已提交', unconfirmed: '未确认恢复', injected: '已注入', detected: '已发现', recovered: '已恢复', reset: '已复位', rejected: '已拒绝' }
const actionLabels: Record<string, string> = { inspection: '运行巡检', diagnose: '诊断故障', execute: '执行修复', verify: '验证恢复', restore_image: '恢复镜像版本', restore_selector: '恢复服务选择器', restore_probe: '恢复健康探针', 'policy.update': '更新策略', 'demo.inject': '注入演示故障', 'demo.reset': '复位演示环境', 'baseline.capture': '记录健康基线', 'request.rejected': '操作被拒绝' }
function pageFromHash(): Page {
  const value = location.hash.replace('#', '')
  return nav.some(item => item.id === value) ? value as Page : 'experiments'
}
const state = ref<State | null>(null)
const page = ref<Page>(pageFromHash())
const query = ref('')
const filter = ref('all')
const recordLimit = ref(50)
const selectedID = ref<string | null>(null)
const drawerTab = ref('evidence')
const busy = ref('')
const refreshing = ref(false)
const connectionError = ref('')
const fetchedAt = ref<Date | null>(null)
const environmentOpen = ref(false)
const moreOpen = ref(false)
const searchOpen = ref(false)
const searchQuery = ref('')
const searchIndex = ref(0)
const confirmedPlan = ref<Incident | null>(null)
const confirmAction = ref<'execute' | 'reset' | null>(null)
const toast = ref<{ text: string; error: boolean } | null>(null)
const runbooks = ref<Runbook[]>([])
const runbooksError = ref('')
const apiToken = ref(sessionStorage.getItem('kubepilot-token') || '')
const loginToken = ref('')
const needsLogin = ref(false)
const loggingIn = ref(false)
const announcementOpen = ref(sessionStorage.getItem('kubepilot-announcement') !== 'hidden')
const sidebarCollapsed = ref(sessionStorage.getItem('kubepilot-sidebar') === 'collapsed')
const consoleResult = ref<ConsoleResult | null>(null)
const isMac = /Mac|iPhone|iPad/.test(navigator.platform)
let poll: ReturnType<typeof setInterval> | undefined
let toastTimer: ReturnType<typeof setTimeout> | undefined
let requestNumber = 0
let appliedRequest = 0
const title = computed(() => nav.find(n => n.id === page.value)!.name)
const demo = computed(() => state.value?.settings.mode === 'demo')
const active = computed(() => state.value?.incidents.filter(i => i.status !== 'resolved') ?? [])
const resolved = computed(() => state.value?.incidents.filter(i => i.status === 'resolved') ?? [])
const selected = computed(() => state.value?.incidents.find(i => i.id === selectedID.value))
const snapshot = computed(() => state.value?.snapshot)
const incidents = computed(() => [...(state.value?.incidents ?? [])].reverse().filter(i => (filter.value === 'all' || i.status === filter.value) && matchesIncident(i, query.value)))
const audits = computed(() => [...(state.value?.audits ?? [])].reverse().filter(a => [a.action, actionLabels[a.action], a.target, a.detail].join(' ').toLowerCase().includes(query.value.trim().toLowerCase())))
const inspections = computed(() => [...(state.value?.inspections ?? [])].reverse())
const latestInspection = computed(() => inspections.value[0])
const trend = computed(() => (state.value?.inspections ?? []).filter(i => i.status === 'completed').slice(-12))
const readyPercent = computed(() => snapshot.value && snapshot.value.pods > 0 ? Math.round(snapshot.value.readyPods / snapshot.value.pods * 100) : null)
const incidentFilters = computed(() => [
  { id: 'all', label: '全部', count: state.value?.incidents.length ?? 0 },
  { id: 'new', label: '待诊断' }, { id: 'planned', label: '待执行' },
  { id: 'manual', label: '待人工' }, { id: 'resolved', label: '已恢复' },
])
const drawerTabs = [{ id: 'evidence', label: '故障证据' }, { id: 'diagnosis', label: '诊断与处置' }, { id: 'timeline', label: '处理时间线' }]
const executeBlocked = computed(() => {
  if (selected.value?.plan && (!selected.value.planHash || Date.parse(selected.value.reviewExpires || '') <= Date.now())) return '计划确认已到期，请重新诊断。'
  if (!state.value?.settings.writesEnabled) return '当前为只读模式，暂不能执行处置。'
  if (selected.value?.plan && !state.value?.policies.find(p => p.action === selected.value?.plan?.action)?.enabled) return '对应处置策略已关闭，请先启用策略。'
  if ((selected.value?.executionCount ?? 0) > 0) return '此事件已执行过一次处置，请人工确认后续方案。'
  return ''
})
type SearchResult = { id: string; label: string; description: string; icon: Component; page?: Page; incident?: Incident }
const searchResults = computed<SearchResult[]>(() => {
  const term = searchQuery.value.trim().toLowerCase()
  const pages = nav.filter(n => (n.name + ' ' + n.id).toLowerCase().includes(term)).map(n => ({ id: n.id, label: n.name, description: '前往页面', icon: n.icon, page: n.id }))
  const events = term ? [...(state.value?.incidents ?? [])].reverse().filter(i => matchesIncident(i, term)).slice(0, 6).map(i => ({ id: i.id, label: i.target.name, description: i.title + ' · ' + status(i.status), icon: Siren, incident: i })) : []
  return [...pages, ...events]
})
function matchesIncident(i: Incident, term: string) { return [i.title, i.type, i.target.name, i.target.namespace, i.id].join(' ').toLowerCase().includes(term.trim().toLowerCase()) }
function time(value?: string | Date, seconds = false) { return value ? new Date(value).toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit', ...(seconds ? { second: '2-digit' } : {}) }) : '—' }
function date(value: string) { return new Date(value).toLocaleString('zh-CN', { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', second: '2-digit' }) }
function duration(ms?: number) { return ms === undefined ? '—' : ms < 1000 ? ms + ' ms' : (ms / 1000).toFixed(1) + ' s' }
function status(value: string) { return statusLabels[value] ?? value }
function notify(text: string, error = false) {
  toast.value = { text, error }; clearTimeout(toastTimer)
  toastTimer = setTimeout(() => { toast.value = null }, error ? 9000 : 4500)
}
function setPage(value: Page) {
  selectedID.value = null; moreOpen.value = false; searchOpen.value = false
  if (page.value !== value) { page.value = value; history.pushState(null, '', '#' + value) }
}
function syncRoute() { page.value = pageFromHash(); selectedID.value = null; moreOpen.value = false; searchOpen.value = false }
function focusMain() { document.getElementById('main')?.focus() }
function openIncident(incident: Incident) { selectedID.value = incident.id; drawerTab.value = 'evidence' }
function openSearch() { if (hasOpenDialog()) return; searchQuery.value = ''; searchIndex.value = 0; searchOpen.value = true }
async function selectSearch(result?: SearchResult) {
  if (!result) return
  searchOpen.value = false
  if (result.page) setPage(result.page)
  if (result.incident) { await nextTick(); openIncident(result.incident) }
}
function searchKey(event: KeyboardEvent) {
  if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
    event.preventDefault()
    const length = searchResults.value.length
    if (length) searchIndex.value = (searchIndex.value + (event.key === 'ArrowDown' ? 1 : -1) + length) % length
    nextTick(() => document.getElementById('command-' + searchIndex.value)?.scrollIntoView({ block: 'nearest' }))
  } else if (event.key === 'Enter') { event.preventDefault(); void selectSearch(searchResults.value[searchIndex.value]) }
}
async function api(path: string, body?: unknown) {
  const headers: Record<string, string> = apiToken.value ? { Authorization: 'Bearer ' + apiToken.value } : {}
  const response = await fetch('/api' + path, {
    ...(body === undefined ? { headers } : { method: 'POST', headers: { ...headers, 'Content-Type': 'application/json', 'X-Requested-With': 'KubePilot' }, body: JSON.stringify(body) }),
    signal: AbortSignal.timeout(body === undefined ? 12000 : 90000),
  })
  if (response.status === 401) needsLogin.value = true
  const result = await response.json()
  if (!response.ok) throw new Error(result.error || '请求失败 (' + response.status + ')')
  return result
}
async function loadRunbooks() {
  try { runbooks.value = await api('/runbooks'); runbooksError.value = '' }
  catch { runbooksError.value = '故障场景暂未加载，请重试。' }
}
async function refresh(manual = false) {
  const id = ++requestNumber
  if (manual) refreshing.value = true
  try {
    const next: State = await api('/state')
    if (id >= appliedRequest) { state.value = next; appliedRequest = id; connectionError.value = ''; fetchedAt.value = new Date(); needsLogin.value = false }
    if (manual) notify('已同步最新状态')
    return true
  } catch (error) {
    if (id >= appliedRequest) connectionError.value = (error as Error).name === 'TimeoutError' ? '连接超时，请检查后端服务' : (error as Error).message
    if (manual) notify(connectionError.value, true)
    return false
  } finally { if (manual) refreshing.value = false }
}
async function runAction(key: string, action: () => Promise<unknown>, success: string) {
  if (busy.value) return false
  busy.value = key
  try {
    await action()
    const synced = await refresh()
    notify(synced ? success : '操作已提交，暂未获取最新状态。请刷新确认结果。', !synced)
    return true
  } catch (error) {
    const message = (error as Error).name === 'TimeoutError' ? '操作响应超时，请刷新并检查审计记录后再试。' : (error as Error).message
    await refresh(); notify(message, true); return false
  } finally { busy.value = '' }
}
async function inspect() { await runAction('inspect', () => api('/inspections', {}), '巡检完成，已更新异常与证据') }
async function diagnose() {
  const id = selectedID.value
  if (id && await runAction('diagnose', () => api('/incidents/' + id + '/diagnose', {}), '诊断完成，已生成分析结果')) {
    if (selectedID.value === id) drawerTab.value = 'diagnosis'
  }
}
async function execute() {
  const item = confirmedPlan.value
  confirmAction.value = null
  if (!item?.planHash || executeBlocked.value) return
  const id = item.id, planHash = item.planHash
  if (await runAction('execute', async () => { await api('/incidents/' + id + '/execute', {planHash}); await api('/inspections', {}) }, '处置流程结束，请查看实际恢复结果')) {
    if (selectedID.value === id) drawerTab.value = 'timeline'
  }
}
async function verify() {
  const id = selectedID.value
  if (id && await runAction('verify', () => api('/incidents/' + id + '/verify', {}), '恢复验证已完成')) {
    if (selectedID.value === id) drawerTab.value = 'timeline'
  }
}
async function updatePolicy(policy: Policy, field: 'enabled' | 'automatic') {
  const next = { enabled: policy.enabled, automatic: policy.automatic }
  next[field] = !next[field]; if (!next.enabled) next.automatic = false
  await runAction(policy.action, () => api('/policies/' + policy.action, next), '策略已更新')
}
async function runConsole(request: ConsoleRequest) {
  if (busy.value || !state.value) return
  const started = performance.now()
  consoleResult.value = null
  let failure = ''
  const messages = { inspect: '巡检完成，已更新异常与证据', diagnose: '诊断完成，已生成分析结果', verify: '恢复验证已完成', inject: '故障已注入并完成巡检，可查看关联事件' }
  const ok = await runAction('console-' + request.action, async () => {
    try {
      if (request.namespace !== state.value?.settings.namespace) throw new Error('目标命名空间不在当前连接范围内')
      if (request.action === 'inspect') await api('/inspections', {})
      else if (request.action === 'inject') {
        if (!demo.value || !runbooks.value.some(book => book.type === request.fault)) throw new Error('当前请求不支持故障注入')
        await api('/demo/inject', { fault: request.fault }); await api('/inspections', {})
      } else {
        const incident = state.value.incidents.find(item => item.id === request.incidentId && item.target.name === request.workload && item.target.namespace === request.namespace)
        if (!incident) throw new Error('目标事件不存在，请重新选择工作负载')
        await api('/incidents/' + incident.id + '/' + request.action, {})
      }
    } catch (error) { failure = (error as Error).message; throw error }
  }, messages[request.action])
  const incident = request.action === 'inject'
    ? [...state.value.incidents].reverse().find(item => item.type === request.fault && item.target.namespace === request.namespace)
    : state.value.incidents.find(item => item.id === request.incidentId)
  const inspected = request.action === 'inspect' || request.action === 'inject'
  const failedInspection = inspected && !connectionError.value && latestInspection.value?.status === 'failed'
  const unconfirmed = request.action === 'verify' && incident?.status !== 'resolved'
  consoleResult.value = {
    action: request.action, outcome: !ok || failedInspection ? 'error' : connectionError.value || unconfirmed ? 'unconfirmed' : 'success',
    message: failure || (failedInspection ? latestInspection.value?.error || '巡检未能完成' : connectionError.value ? '操作已提交，最新状态尚未同步。请重新连接后确认结果。' : unconfirmed ? '尚未确认恢复，请查看事件证据和处理时间线。' : messages[request.action]),
    at: new Date().toISOString(), elapsedMs: Math.round(performance.now() - started), request,
    ...(!connectionError.value && ok ? {
      incident, snapshot: state.value.snapshot,
      ...(inspected ? { inspection: latestInspection.value, incidents: state.value.incidents.filter(item => item.status !== 'resolved') } : {}),
    } : {}),
  }
}
async function reset() {
  confirmAction.value = null
  await runAction('reset', async () => { await api('/demo/reset', {}); await api('/inspections', {}) }, '演示资源已复位，历史记录已保留')
}
async function copyEvidence() {
  if (!selected.value) return
  try { await navigator.clipboard.writeText(selected.value.evidence.map(e => '[' + e.source + '] ' + e.summary + '\n' + e.detail).join('\n\n')); notify('故障证据已复制') }
  catch { notify('浏览器未允许复制，请选中证据文本手动复制。', true) }
}
function download(kind: 'inspections' | 'audit' | 'experiments') {
  if (!state.value) return
  const source = kind === 'inspections' ? state.value.inspections : kind === 'audit' ? state.value.audits : state.value.experiments
  if (!source.length) { notify('当前还没有可导出的记录'); return }
  const keys = Array.from(new Set(source.flatMap(v => Object.keys(v))))
  const cell = (v: unknown) => { let text = String(v ?? ''); if (/^[=+@-]/.test(text)) text = "'" + text; return '"' + text.replaceAll('"', '""') + '"' }
  const csv = '\uFEFF' + [keys, ...source.map(v => keys.map(k => (v as unknown as Record<string, unknown>)[k]))].map(r => r.map(cell).join(',')).join('\r\n')
  const link = document.createElement('a'); const url = URL.createObjectURL(new Blob([csv], { type: 'text/csv;charset=utf-8;' }))
  link.href = url; link.download = 'kubepilot-' + kind + '-' + state.value.settings.mode + '.csv'; link.click()
  setTimeout(() => URL.revokeObjectURL(url), 1000)
  notify('CSV 记录已导出')
}
function keydown(event: KeyboardEvent) {
  if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === 'k') {
    event.preventDefault()
    if (searchOpen.value) searchOpen.value = false
    else openSearch()
  }
}
async function login() {
  if (!loginToken.value.trim() || loggingIn.value) return
  loggingIn.value = true; apiToken.value = loginToken.value.trim()
  if (await refresh()) { sessionStorage.setItem('kubepilot-token', apiToken.value); loginToken.value = ''; await loadRunbooks() }
  loggingIn.value = false
}
watch(page, () => { query.value = ''; filter.value = 'all'; recordLimit.value = 50; nextTick(() => document.getElementById('main')?.scrollTo({ top: 0 })) })
watch([query, filter], () => { recordLimit.value = 50 })
watch(searchQuery, () => { searchIndex.value = 0 })
watch(announcementOpen, value => sessionStorage.setItem('kubepilot-announcement', value ? 'visible' : 'hidden'))
watch(sidebarCollapsed, value => sessionStorage.setItem('kubepilot-sidebar', value ? 'collapsed' : 'expanded'))
let disposed = false
onMounted(async () => {
  window.addEventListener('keydown', keydown)
  window.addEventListener('popstate', syncRoute)
  window.addEventListener('hashchange', syncRoute)
  await refresh(); if (disposed) return
  await loadRunbooks(); if (disposed) return
  poll = setInterval(() => { if (!busy.value && !needsLogin.value && document.visibilityState === 'visible') void refresh() }, 5000)
})
onUnmounted(() => {
  disposed = true
  clearInterval(poll); clearTimeout(toastTimer)
  window.removeEventListener('keydown', keydown)
  window.removeEventListener('popstate', syncRoute)
  window.removeEventListener('hashchange', syncRoute)
})
</script>

<template>
  <div class="app-shell" :class="{ 'console-page': page === 'experiments', 'banner-hidden': !announcementOpen, 'sidebar-collapsed': sidebarCollapsed }">
    <a href="#main" class="skip-link" @click.prevent="focusMain">跳至主要内容</a>
    <div v-if="announcementOpen" class="announcement-bar"><p><strong>{{ demo ? '当前为独立演示环境' : state ? 'Kubernetes 工作空间已连接' : '正在连接工作空间' }}</strong><span>{{ demo ? '在工作台体验巡检、诊断与恢复验证' : '从故障证据出发，追踪每一次受控处置' }}</span><button @click="environmentOpen = true">查看环境</button></p><button class="announcement-close" aria-label="关闭顶部提示" @click="announcementOpen = false"><X :size="14" /></button></div>
    <aside class="sidebar">
      <div class="brand-row"><a class="brand" href="#experiments" aria-label="KubePilot 实验工作台" @click.prevent="setPage('experiments')"><BrandMark /><span>KUBEPILOT</span></a><button class="sidebar-toggle" :aria-label="sidebarCollapsed ? '展开导航' : '收起导航'" @click="sidebarCollapsed = !sidebarCollapsed"><PanelLeftOpen v-if="sidebarCollapsed" :size="16" /><PanelLeftClose v-else :size="16" /></button></div>
      <nav class="desktop-nav" aria-label="主导航">
        <button v-for="item in desktopNav" :key="item.id" class="nav-item" :class="{ active: page === item.id }" :aria-label="item.name" :title="item.name" :aria-current="page === item.id ? 'page' : undefined" @click="setPage(item.id)"><component :is="item.icon" :size="16" :stroke-width="1.7" /><span>{{ item.name }}</span></button>
      </nav>
      <div class="sidebar-bottom">
        <button class="workspace-balance" @click="setPage('incidents')"><span>待处理事件</span><strong>{{ active.length }}<small> 项</small></strong><span class="balance-mode">{{ demo ? 'Demo' : 'Cluster' }}</span></button>
        <button class="organization-switch" aria-label="环境与连接" @click="environmentOpen = true"><span class="org-avatar">示</span><span><strong>演示工作空间</strong><small>{{ connectionError ? '连接中断' : demo ? '本地演示 · Admin' : 'Kubernetes · Admin' }}</small></span><ChevronRight :size="15" /></button>
      </div>
    </aside>

    <div class="main-shell">
      <header class="topbar">
        <div class="breadcrumb"><span>工作空间</span><ChevronRight :size="13" /><strong>{{ title }}</strong></div>
        <a class="mobile-brand" href="#overview" @click.prevent="setPage('overview')"><BrandMark />KubePilot</a>
        <div class="topbar-right">
          <button class="search-trigger" aria-label="搜索页面或故障，快捷键 Control K" @click="openSearch"><Search :size="17" /><span>快速查找</span><kbd>{{ isMac ? '⌘' : 'Ctrl' }} K</kbd></button>
          <button class="environment-chip" @click="environmentOpen = true"><span class="status-dot" :class="{ offline: connectionError }"></span>{{ demo ? '演示环境' : state ? '真实集群' : '连接中' }}<ChevronRight :size="13" /></button>
          <span class="avatar" aria-label="示例用户">示</span>
        </div>
      </header>
      <main id="main" class="main-content" tabindex="-1">
        <h1 v-if="page === 'experiments'" class="sr-only">实验工作台</h1>
        <PlaygroundConsole v-if="page === 'experiments'" :state="state" :runbooks="runbooks" :result="consoleResult" :busy="busy" :connection-error="connectionError" :runbooks-error="runbooksError" @run="runConsole" @clear="consoleResult = null" @environment="environmentOpen = true" @reset="confirmAction = 'reset'" @open-incident="openIncident" @export="download('experiments')" @notify="notify" @refresh="refresh(true)" @load-runbooks="loadRunbooks" />
        <AgentWorkspace v-else-if="page === 'agent'" :state="state" :api="api" :refresh="refresh" :connection-error="connectionError" @open-incident="openIncident" @policies="setPage('policies')" />
        <template v-else>
        <div class="page-heading">
          <div><p class="eyebrow">KUBERNETES · {{ demo ? 'DEMO WORKSPACE' : 'OPERATIONS' }}</p><h1>{{ title }}</h1><p class="page-description">{{ descriptions[page] }}</p></div>
          <div class="heading-actions">
            <button class="button secondary refresh-button" :disabled="refreshing || !!busy" aria-label="刷新状态" @click="refresh(true)"><RefreshCw :size="17" :class="{ spin: refreshing }" /><span>刷新</span></button>
            <button v-if="page !== 'audit' && page !== 'policies'" class="button primary" :disabled="!!busy || !state" @click="inspect"><Loader2 v-if="busy === 'inspect'" class="spin" :size="17" /><Play v-else :size="15" fill="currentColor" />{{ busy === 'inspect' ? '正在巡检…' : '运行巡检' }}</button>
            <button v-else-if="page === 'audit'" class="button primary" :disabled="!state?.audits.length" @click="download('audit')"><Download :size="17" />导出记录</button>
          </div>
        </div>

        <div v-if="connectionError" class="notice error" role="alert"><CircleAlert :size="18" /><span><strong>连接暂时中断</strong>{{ state ? ' · 当前显示上次同步的数据。' : ' · 请确认后端服务已启动。' }}<small>{{ connectionError }}</small></span><button @click="refresh(true)">重新连接<RefreshCw :size="14" /></button></div>
        <div v-else-if="demo" class="notice demo"><Info :size="17" /><span><strong>演示模式</strong><span class="notice-separator">·</span>数据来自独立模拟环境，操作仅作用于演示资源。</span><button @click="environmentOpen = true">环境详情<ArrowUpRight :size="15" /></button></div>
        <div v-else-if="state && !state.settings.writesEnabled" class="notice"><LockKeyhole :size="17" /><span>只读模式 · 可巡检与诊断，尚未启用集群写操作。</span></div>
        <div v-if="snapshot?.warnings.length && !demo" class="notice warning"><Info :size="17" /><span>{{ snapshot.warnings.join(' ') }}</span></div>
        <div v-if="latestInspection?.status === 'failed'" class="notice error"><CircleAlert :size="17" /><span>最近一次巡检失败，指标保留上次成功采集值。{{ latestInspection.error }}</span></div>
        <div v-if="busy && !selected" class="operation-progress" role="status"><Loader2 :size="15" class="spin" />正在处理操作，请稍候…</div>

        <div v-if="!state && !connectionError" class="skeleton-layout" role="status" aria-label="正在加载工作空间">
          <span class="sr-only">正在加载工作空间</span><div class="skeleton skeleton-strip"></div><div class="skeleton skeleton-stats"></div><div class="skeleton-columns"><div class="skeleton"></div><div class="skeleton"></div></div>
        </div>
        <EmptyState v-else-if="!state" title="暂时无法连接工作空间" description="启动本地后端服务后，重新连接即可继续。"><template #icon><Server :size="27" /></template><button class="button primary" @click="refresh(true)">重新连接</button></EmptyState>

        <div v-else :key="page" class="page-view">
          <template v-if="page === 'overview'">
            <section class="cluster-strip" aria-label="当前集群">
              <span class="cluster-icon"><Network :size="24" :stroke-width="1.5" /></span>
              <div class="cluster-name"><strong>{{ snapshot?.cluster || '尚未采集集群信息' }}</strong><span><span class="mono">{{ state.settings.namespace }}</span><i>·</i>{{ demo ? '本地模拟集群' : '限定命名空间' }}</span></div>
              <span class="pill" :class="active.length ? 'warning' : 'resolved'"><span class="mini-dot"></span>{{ active.length ? '有异常待处理' : '暂无未关闭事件' }}</span>
              <span class="last-updated"><Clock3 :size="14" />采集于 {{ time(snapshot?.at, true) }}</span>
            </section>
            <section class="metrics-panel" aria-label="集群关键指标">
              <article class="metric"><div class="metric-label">就绪 Pod<Boxes :size="19" /></div><div class="metric-value">{{ snapshot?.readyPods ?? '—' }}<span>/ {{ snapshot?.pods ?? '—' }}</span></div><p><span class="small-dot blue-dot"></span>{{ readyPercent === null ? '等待采集资源状态' : '资源就绪率 ' + readyPercent + '%' }}</p></article>
              <article class="metric"><div class="metric-label">待处理事件<Siren :size="19" /></div><div class="metric-value">{{ active.length }}<span>项</span></div><p><span class="small-dot orange-dot"></span>{{ active.filter(i => i.severity === 'critical').length }} 项高优先级异常</p></article>
              <article class="metric"><div class="metric-label">已恢复事件<ShieldCheck :size="19" /></div><div class="metric-value">{{ resolved.length }}<span>项</span></div><p><CheckCircle2 :size="14" class="success-text" />已通过恢复验证</p></article>
              <article class="metric"><div class="metric-label">累计巡检<ClipboardCheck :size="19" /></div><div class="metric-value">{{ state.inspections.length }}<span>次</span></div><p><Clock3 :size="14" />每 {{ state.settings.intervalSeconds }} 秒自动执行</p></article>
            </section>
            <div class="overview-middle">
              <section class="panel trend-panel">
                <div class="panel-heading"><div><h2>异常发现趋势</h2><p>最近 {{ trend.length }} 次成功巡检</p></div><span class="chart-key"><i></i>异常项</span></div>
                <TrendChart :runs="trend" />
                <div class="chart-bottom"><span><span class="status-dot" :class="{ offline: !!connectionError }"></span>{{ connectionError ? '等待恢复连接' : '定时巡检已启用' }}</span><span>最近耗时 <strong>{{ duration(latestInspection?.durationMs) }}</strong></span></div>
              </section>
              <section class="panel health-panel">
                <div class="panel-heading"><div><h2>资源健康度</h2><p>当前采集范围内的运行状态</p></div><Server :size="19" class="muted" /></div>
                <div class="health-ring-row">
                  <div class="health-ring" :style="{ '--health': (readyPercent ?? 0) + '%' }" :aria-label="'Pod 就绪率 ' + (readyPercent === null ? '暂无数据' : readyPercent + '%')"><div><strong>{{ readyPercent ?? '—' }}<small v-if="readyPercent !== null">%</small></strong><span>Pod 就绪率</span></div></div>
                  <div class="health-legend"><div><i class="green-dot"></i><span>就绪</span><strong>{{ snapshot?.readyPods ?? '—' }}</strong></div><div><i class="orange-dot"></i><span>未就绪</span><strong>{{ snapshot ? snapshot.pods - snapshot.readyPods : '—' }}</strong></div></div>
                </div>
                <div class="resource-summary"><span><Server :size="15" />节点<strong>{{ snapshot && snapshot.readyNodes >= 0 ? snapshot.readyNodes : '—' }} / {{ snapshot && snapshot.nodes >= 0 ? snapshot.nodes : '—' }}</strong></span><span><Network :size="15" />Service<strong>{{ snapshot?.services ?? '—' }}</strong></span></div>
              </section>
            </div>
            <section class="panel incident-panel">
              <div class="panel-heading"><div class="heading-inline"><h2>待处理事件</h2><span class="count-label">{{ active.length }}</span></div><button class="text-button" @click="setPage('incidents')">查看全部<ArrowRight :size="16" /></button></div>
              <div v-if="active.length" class="table-scroll" tabindex="0" role="region" aria-label="待处理事件表，可横向滚动">
                <table><thead><tr><th>故障 / 工作负载</th><th>命名空间</th><th>优先级</th><th>处理状态</th><th>发现时间</th><th><span class="sr-only">操作</span></th></tr></thead>
                  <tbody><tr v-for="incident in [...active].reverse().slice(0, 5)" :key="incident.id" class="clickable-row" @click="openIncident(incident)">
                    <td><div class="incident-name"><span class="event-icon" :class="incident.severity"><CircleAlert :size="19" /></span><div><strong>{{ incident.title }}</strong><small class="mono">{{ incident.target.name }}</small></div></div></td>
                    <td><span class="namespace-tag">{{ incident.target.namespace }}</span></td><td><span class="priority" :class="incident.severity"><i></i>{{ incident.severity === 'critical' ? '高优先级' : '一般' }}</span></td><td><span class="pill" :class="incident.status">{{ status(incident.status) }}</span></td><td class="muted tabular">{{ date(incident.firstSeen) }}</td>
                    <td><button class="row-link" :aria-label="'查看 ' + incident.target.name" @click.stop="openIncident(incident)"><ChevronRight :size="18" /></button></td>
                  </tr></tbody>
                </table>
              </div>
              <EmptyState v-else title="所有事件已处理" description="集群当前没有待处理事件，定时巡检会持续发现新的异常。"><template #icon><CheckCircle2 :size="27" /></template><button class="text-button" @click="setPage('experiments')">前往实验工作台<ArrowRight :size="16" /></button></EmptyState>
            </section>
            <div class="workflow-strip"><span><ShieldCheck :size="17" />每一步，有迹可循</span><div><span>异常发现</span><ChevronRight :size="13" /><span>证据诊断</span><ChevronRight :size="13" /><span>策略校验</span><ChevronRight :size="13" /><span>受控执行</span><ChevronRight :size="13" /><span>恢复验证</span></div></div>
          </template>

          <template v-else-if="page === 'incidents'">
            <section class="panel">
              <div class="toolbar incident-toolbar">
                <SegmentedControl v-model="filter" :options="incidentFilters" label="筛选故障状态" />
                <label class="search-field"><Search :size="17" /><input v-model="query" placeholder="搜索事件或工作负载" aria-label="搜索事件" /><button v-if="query" class="clear-search" aria-label="清除搜索" @click="query = ''"><X :size="15" /></button></label>
              </div>
              <div v-if="incidents.length" class="table-scroll" tabindex="0" role="region" aria-label="故障事件表，可横向滚动">
                <table><thead><tr><th>故障事件</th><th>目标资源</th><th>优先级</th><th>状态</th><th>观测次数</th><th>首次发现</th><th><span class="sr-only">操作</span></th></tr></thead><tbody>
                  <tr v-for="i in incidents.slice(0, recordLimit)" :key="i.id" class="clickable-row" @click="openIncident(i)">
                    <td><strong>{{ i.title }}</strong><small class="mono cell-sub">{{ i.id }}</small></td><td><span class="mono">{{ i.target.name }}</span><small class="cell-sub">{{ i.target.namespace }} / {{ i.target.kind }}</small></td><td><span class="priority" :class="i.severity"><i></i>{{ i.severity === 'critical' ? '高' : '一般' }}</span></td><td><span class="pill" :class="i.status">{{ status(i.status) }}</span></td><td class="tabular">{{ i.occurrences }} 次</td><td class="muted tabular">{{ date(i.firstSeen) }}</td><td><button class="row-link" :aria-label="'查看 ' + i.target.name" @click.stop="openIncident(i)"><ChevronRight :size="18" /></button></td>
                  </tr>
                </tbody></table>
              </div>
              <EmptyState v-else :title="query || filter !== 'all' ? '没有匹配的事件' : '还没有故障事件'" :description="query || filter !== 'all' ? '试试其他关键词，或清除当前筛选条件。' : '运行一次巡检，开始发现集群中的异常。'">
                <template #icon><Search :size="26" /></template><button v-if="query || filter !== 'all'" class="button secondary" @click="query = ''; filter = 'all'">清除筛选</button>
              </EmptyState>
              <div class="table-footer"><span>共 {{ incidents.length }} 条事件 · 相同故障自动聚合</span><button v-if="incidents.length > recordLimit" class="text-button" @click="recordLimit += 50">再显示 50 条<ArrowDown :size="15" /></button></div>
            </section>
          </template>

          <template v-else-if="page === 'inspections'">
            <div class="schedule-card"><span class="feature-icon"><Clock3 :size="25" /></span><div><h2>让巡检持续发生</h2><p>每 {{ state.settings.intervalSeconds }} 秒检查一次 <span class="mono">{{ state.settings.namespace }}</span>，聚合资源状态与异常事件。</p></div><span class="pill" :class="connectionError ? 'warning' : 'resolved'"><span class="mini-dot"></span>{{ connectionError ? '连接中断' : '定时任务已启用' }}</span></div>
            <section class="panel">
              <div class="panel-heading"><div class="heading-inline"><h2>巡检历史</h2><span class="count-label">{{ inspections.length }}</span></div><button class="button secondary compact" :disabled="!inspections.length" @click="download('inspections')"><Download :size="16" />导出 CSV</button></div>
              <div v-if="inspections.length" class="table-scroll" tabindex="0" role="region" aria-label="巡检历史表，可横向滚动"><table><thead><tr><th>任务编号</th><th>触发方式</th><th>开始时间</th><th>耗时</th><th>异常项</th><th>执行结果</th></tr></thead><tbody>
                <tr v-for="run in inspections.slice(0, recordLimit)" :key="run.id"><td class="mono">{{ run.id }}</td><td>{{ run.trigger === 'scheduler' ? '定时巡检' : run.trigger === 'startup' ? '启动巡检' : '手动巡检' }}</td><td class="tabular">{{ date(run.startedAt) }}</td><td class="tabular">{{ duration(run.durationMs) }}</td><td><span class="finding-count" :class="{ 'has-findings': run.findings > 0 }">{{ run.findings }}</span></td><td><span class="pill" :class="run.status">{{ status(run.status) }}</span><small v-if="run.error" class="cell-sub error-text">{{ run.error }}</small></td></tr>
              </tbody></table></div>
              <EmptyState v-else title="还没有巡检记录" description="点击右上角“运行巡检”开始采集。"><template #icon><ClipboardCheck :size="27" /></template></EmptyState>
              <div class="table-footer"><span>显示 {{ Math.min(recordLimit, inspections.length) }} / {{ inspections.length }} 条记录</span><button v-if="inspections.length > recordLimit" class="text-button" @click="recordLimit += 50">再显示 50 条<ArrowDown :size="15" /></button></div>
            </section>
          </template>

          <template v-else-if="page === 'policies'">
            <div class="policy-intro"><ShieldCheck :size="21" /><p>自动处置默认关闭。开启后，定时巡检会对符合条件的新事件执行一次修复，未知故障转人工处理。</p></div>
            <div class="policy-grid">
              <article v-for="policy in state.policies" :key="policy.action" class="panel policy-card" :class="{ 'policy-disabled': !policy.enabled }">
                <div class="policy-top"><span class="feature-icon"><component :is="policy.action === 'restore_selector' ? Network : policy.action === 'restore_image' ? Boxes : Activity" :size="24" /></span><button class="switch" role="switch" :aria-checked="policy.enabled" :aria-label="policy.name + '策略'" :class="{ on: policy.enabled }" :disabled="!!busy" @click="updatePolicy(policy, 'enabled')"><span></span></button></div>
                <h2>{{ policy.name }}</h2><p class="policy-description">{{ policy.description }}</p>
                <dl class="policy-details"><div><dt>适用范围</dt><dd class="mono">{{ state.settings.namespace }}</dd></div><div><dt>风险等级</dt><dd class="warning-text">{{ policy.risk }} · 配置变更</dd></div><div><dt>单事件上限</dt><dd>执行 1 次</dd></div></dl>
                <button class="auto-setting" role="checkbox" :aria-checked="policy.automatic" :disabled="!!busy || !policy.enabled || !state.settings.writesEnabled" @click="updatePolicy(policy, 'automatic')"><span class="checkbox" :class="{ checked: policy.automatic }"><Check v-if="policy.automatic" :size="14" /></span><span>允许自动处置<small>{{ !policy.enabled ? '请先启用此策略' : policy.automatic ? '定时巡检可自动执行' : '由操作人员确认后执行' }}</small></span></button>
              </article>
            </div>
            <section class="panel boundary">
              <div class="panel-heading"><div><h2>每次执行前，完成六项校验</h2><p>固定约束始终生效，模型建议也需要经过校验。</p></div><ShieldCheck :size="22" class="success-text" /></div>
              <div class="boundary-grid"><div v-for="(item, index) in ['目标命名空间与动作白名单', '可信健康基线及对象身份', 'resourceVersion 并发校验', '服务端 dry-run 预检', '同一工作负载执行互斥', '就绪状态与业务恢复验证']" :key="item"><span class="step-number">{{ String(index + 1).padStart(2, '0') }}</span><strong>{{ item }}</strong><Check :size="16" /></div></div>
              <p class="boundary-note">基线缺失、对象状态变化或权限不足时，系统停止自动处置并保留记录。</p>
            </section>
          </template>

          <template v-else-if="page === 'audit'">
            <section class="panel">
              <div class="toolbar"><div class="heading-inline"><h2>操作记录</h2><span class="count-label">{{ state.audits.length }}</span></div><label class="search-field"><Search :size="17" /><input v-model="query" placeholder="搜索动作、对象或内容" aria-label="搜索审计记录" /><button v-if="query" class="clear-search" aria-label="清除搜索" @click="query = ''"><X :size="15" /></button></label></div>
              <div v-if="audits.length" class="table-scroll" tabindex="0" role="region" aria-label="操作审计表，可横向滚动"><table class="audit-table"><thead><tr><th>时间 / 操作者</th><th>操作</th><th>目标</th><th>结果</th><th>记录详情</th></tr></thead><tbody>
                <tr v-for="entry in audits.slice(0, recordLimit)" :key="entry.id"><td class="tabular">{{ date(entry.at) }}<small class="cell-sub">{{ ({ operator: '操作人员', scheduler: '定时任务', verifier: '验证器', startup: '启动任务' } as Record<string, string>)[entry.actor] || entry.actor }}</small></td><td>{{ actionLabels[entry.action] || entry.action }}</td><td class="mono">{{ entry.target }}</td><td><span class="pill" :class="entry.result">{{ status(entry.result) }}</span></td><td class="audit-detail">{{ entry.detail }}</td></tr>
              </tbody></table></div>
              <EmptyState v-else :title="query ? '没有匹配的操作记录' : '还没有操作记录'" description="巡检、诊断和处置发生后，将在此留下记录。"><template #icon><ScrollText :size="26" /></template><button v-if="query" class="text-button" @click="query = ''">清除搜索</button></EmptyState>
              <div class="table-footer"><span>显示 {{ Math.min(recordLimit, audits.length) }} / {{ audits.length }} 条 · 最多保留 2,000 条记录</span><button v-if="audits.length > recordLimit" class="text-button" @click="recordLimit += 50">再显示 50 条<ArrowDown :size="15" /></button></div>
            </section>
          </template>


        </div>
        <footer v-if="state" class="page-footer"><span><span class="status-dot" :class="{ offline: !!connectionError }"></span>{{ connectionError ? '等待连接恢复' : '状态自动同步' }}</span><span>最近同步 {{ time(fetchedAt || undefined, true) }}</span></footer>
        </template>
      </main>
    </div>

    <nav class="mobile-dock" aria-label="移动主导航">
      <button v-for="item in mobileNav" :key="item.id" :class="{ active: page === item.id }" :aria-current="page === item.id ? 'page' : undefined" :aria-label="item.name" @click="setPage(item.id)"><span><component :is="item.icon" :size="22" :stroke-width="1.7" /><i v-if="item.id === 'incidents' && active.length"></i></span>{{ item.name }}</button>
      <button :class="{ active: ['policies', 'audit', 'inspections'].includes(page) }" aria-label="更多功能" :aria-expanded="moreOpen" @click="moreOpen = true"><MoreHorizontal :size="23" /><span>更多</span></button>
    </nav>
  </div>

  <ModalLayer :open="!!selected" kind="drawer" title-id="drawer-title" @close="selectedID = null">
    <template v-if="selected">
      <header class="drawer-heading"><div><span class="eyebrow">INCIDENT DETAILS</span><h2 id="drawer-title">{{ selected.title }}</h2></div><button class="icon-button" aria-label="关闭详情" @click="selectedID = null"><X :size="21" /></button></header>
      <div class="drawer-summary"><div class="drawer-target"><span class="feature-icon"><Boxes :size="22" /></span><div><strong class="mono">{{ selected.target.name }}</strong><span>{{ selected.target.namespace }}<i> / </i>{{ selected.target.kind }}</span></div><span class="pill" :class="selected.status">{{ status(selected.status) }}</span></div><div class="drawer-meta"><span><Clock3 :size="14" />{{ date(selected.firstSeen) }}</span><span>重复观测 {{ selected.occurrences }} 次</span></div></div>
      <div class="drawer-tabs"><SegmentedControl v-model="drawerTab" :options="drawerTabs" label="事件详情" tabs /></div>
      <div :id="'panel-' + drawerTab" :key="drawerTab" class="drawer-content" role="tabpanel" :aria-labelledby="'tab-' + drawerTab" tabindex="0">
        <template v-if="drawerTab === 'evidence'">
          <div class="section-heading"><h3>已采集的证据<span class="count-label">{{ selected.evidence.length }}</span></h3><button class="text-button" :disabled="!selected.evidence.length" @click="copyEvidence"><Copy :size="15" />复制</button></div>
          <article v-for="evidence in selected.evidence" :key="evidence.id" class="evidence-card"><div class="evidence-top"><span class="evidence-source">{{ evidence.source }}</span><time>{{ time(evidence.at, true) }}</time></div><h4>{{ evidence.summary }}</h4><pre>{{ evidence.detail }}</pre></article>
          <EmptyState v-if="!selected.evidence.length" title="暂未采集到证据" description="运行巡检后，相关事件和资源状态会出现在这里。" />
          <p class="subtle-note"><Info :size="15" />诊断基于已采集的证据，保留来源与采集时间。</p>
        </template>
        <template v-else-if="drawerTab === 'diagnosis'">
          <template v-if="selected.diagnosis">
            <div class="section-heading"><h3>根因分析</h3><span class="pill neutral">{{ selected.diagnosis.engine === 'rules' ? '规则引擎' : selected.diagnosis.engine }}</span></div>
            <p class="diagnosis-conclusion">{{ selected.diagnosis.conclusion }}</p>
            <h3 class="section-title">建议检查</h3><ol class="recommendations"><li v-for="item in selected.diagnosis.recommendations" :key="item">{{ item }}</li></ol>
            <div v-if="selected.diagnosis.limitations" class="limitation"><Info :size="17" /><span>{{ selected.diagnosis.limitations }}</span></div>
            <template v-if="selected.plan">
              <div class="section-heading plan-heading"><h3>处置计划</h3><span class="pill planned">{{ selected.executionCount > 0 ? '已尝试执行' : '待执行前校验' }}</span></div>
              <p class="plan-summary">{{ selected.plan.summary }}</p>
              <div class="diff"><div><span>当前配置</span><pre>- {{ selected.plan.before }}</pre></div><div><span>目标配置 · 可信基线</span><pre>+ {{ selected.plan.after }}</pre></div></div>
              <ul class="checks"><li v-for="check in selected.plan.checks" :key="check"><ShieldCheck :size="16" />{{ check }}</li></ul>
            </template>
          </template>
          <EmptyState v-else title="尚未生成诊断" description="诊断将关联当前证据与排障知识，生成根因分析和建议。"><template #icon><Activity :size="28" /></template></EmptyState>
        </template>
        <div v-else class="timeline">
          <div v-for="(item, index) in selected.timeline" :key="index" class="timeline-item"><span class="timeline-marker" :class="item.status"><Check v-if="item.status === 'resolved'" :size="13" /><span v-else></span></span><div><div class="timeline-title"><strong>{{ item.title }}</strong><time>{{ time(item.at, true) }}</time></div><p>{{ item.detail }}</p></div></div>
        </div>
      </div>
      <footer class="drawer-footer">
        <p v-if="selected.status === 'planned' && executeBlocked" class="execution-blocked"><LockKeyhole :size="15" /><span>{{ executeBlocked }}</span><button v-if="executeBlocked.includes('策略')" class="text-button" @click="setPage('policies')">管理策略<ArrowUpRight :size="14" /></button></p>
        <p v-if="selected.status === 'resolved'" class="recovery-note"><CheckCircle2 :size="16" />已通过恢复验证，完整过程已记录。</p>
        <div class="drawer-actions"><button class="button secondary" :disabled="!!busy || selected.status === 'resolved'" @click="verify"><Loader2 v-if="busy === 'verify'" class="spin" :size="17" /><ShieldCheck v-else :size="17" />验证恢复</button>
          <button v-if="selected.status === 'planned' && selected.plan" class="button primary" :disabled="!!busy || !!executeBlocked" @click="confirmedPlan = selected; confirmAction = 'execute'"><Loader2 v-if="busy === 'execute'" class="spin" :size="16" /><Play v-else :size="15" />{{ busy === 'execute' ? '正在处置…' : demo ? '执行演示修复' : '确认并执行' }}</button>
          <button v-else class="button primary" :disabled="!!busy || selected.status === 'resolved'" @click="diagnose"><Loader2 v-if="busy === 'diagnose'" class="spin" :size="17" /><Activity v-else :size="17" />{{ busy === 'diagnose' ? '正在诊断…' : selected.diagnosis ? '重新诊断' : '诊断故障' }}</button>
        </div>
      </footer>
    </template>
  </ModalLayer>

  <ModalLayer :open="moreOpen" kind="sheet" title-id="more-title" @close="moreOpen = false">
    <div class="modal-heading"><div><span class="eyebrow">WORKSPACE</span><h2 id="more-title">更多功能</h2></div><button class="icon-button" aria-label="关闭更多功能" @click="moreOpen = false"><X :size="21" /></button></div>
    <nav class="more-nav" aria-label="更多功能"><button v-for="item in moreNav" :key="item.id" :class="{ active: page === item.id }" @click="setPage(item.id)"><component :is="item.icon" :size="22" /><span>{{ item.name }}</span><ChevronRight :size="17" /></button><button @click="moreOpen = false; environmentOpen = true"><Settings2 :size="22" /><span>环境与连接</span><ChevronRight :size="17" /></button></nav>
    <p class="sheet-footnote">示例用户 · 毕业设计工作空间</p>
  </ModalLayer>

  <ModalLayer :open="searchOpen" kind="command" title-id="search-title" @close="searchOpen = false">
    <h2 id="search-title" class="sr-only">快速查找</h2>
    <div class="command-input"><Search :size="21" /><input v-model="searchQuery" data-autofocus placeholder="搜索页面、故障或工作负载…" aria-label="搜索页面或故障" role="combobox" aria-autocomplete="list" aria-expanded="true" aria-controls="command-results" :aria-activedescendant="searchResults.length ? 'command-' + searchIndex : undefined" @keydown="searchKey" /><button class="icon-button" aria-label="关闭搜索" @click="searchOpen = false"><X :size="19" /></button></div>
    <p class="command-label">{{ searchQuery ? '搜索结果' : '快速前往' }}</p>
    <div id="command-results" class="command-results" role="listbox" aria-label="搜索结果"><button v-for="(result, index) in searchResults" :id="'command-' + index" :key="result.id" class="command-result" :class="{ selected: searchIndex === index }" role="option" :aria-selected="searchIndex === index" @click="selectSearch(result)" @pointermove="searchIndex = index"><component :is="result.icon" :size="20" /><span><strong>{{ result.label }}</strong><small>{{ result.description }}</small></span><CornerDownLeft :size="16" /></button></div>
    <EmptyState v-if="!searchResults.length" title="没有找到相关内容" description="试试“巡检”、故障名称或工作负载名称。" />
    <div class="command-footer"><span><kbd><ArrowUp :size="11" /></kbd><kbd><ArrowDown :size="11" /></kbd>选择</span><span><kbd>Enter</kbd>打开</span><span><Command :size="13" />随时找到下一步</span></div>
  </ModalLayer>

  <ModalLayer :open="environmentOpen" title-id="environment-title" @close="environmentOpen = false">
    <div class="modal-heading"><span class="feature-icon"><Server :size="25" /></span><button class="icon-button" aria-label="关闭环境信息" @click="environmentOpen = false"><X :size="21" /></button></div>
    <h2 id="environment-title">环境与连接</h2><p class="modal-description">当前工作空间的连接状态与运行配置。</p>
    <dl class="environment-details"><div><dt>数据来源</dt><dd>{{ demo ? '本地独立演示环境' : state ? 'Kubernetes API' : '尚未连接' }}</dd></div><div><dt>命名空间</dt><dd class="mono">{{ state?.settings.namespace || '—' }}</dd></div><div><dt>诊断引擎</dt><dd>{{ state?.settings.modelConfigured ? 'Ollama / ' + state.settings.model : state ? '规则引擎 · 未配置大模型' : '—' }}</dd></div><div><dt>写操作</dt><dd>{{ demo ? '仅允许演示操作' : state?.settings.writesEnabled ? '已启用受控写操作' : '只读' }}</dd></div><div><dt>数据保存</dt><dd>{{ state?.settings.storage || '—' }}</dd></div><div><dt>应用版本</dt><dd>v{{ state?.settings.version || '0.1.0' }}</dd></div></dl>
    <div class="notice"><Info :size="17" /><span>真实集群接入与大模型配置步骤见项目 README。接入时需指定 kubeconfig、context 和实验命名空间。</span></div>
    <button class="button primary full-width" @click="environmentOpen = false">知道了</button>
  </ModalLayer>

  <ModalLayer :open="!!confirmAction" title-id="confirm-title" @close="confirmAction = null">
    <div class="modal-heading"><span class="feature-icon"><component :is="confirmAction === 'execute' ? ShieldCheck : RefreshCw" :size="25" /></span><button class="icon-button" aria-label="关闭确认" @click="confirmAction = null"><X :size="21" /></button></div>
    <h2 id="confirm-title">{{ confirmAction === 'execute' ? '确认执行处置计划' : '复位演示资源' }}</h2>
    <p class="modal-description">{{ confirmAction === 'execute' ? (demo ? '仅修改演示环境。提交变更后，系统会自动验证模拟恢复结果。' : '系统将重新校验策略与对象版本，提交限定字段的变更，并验证业务恢复。') : '清除模拟故障并保留历史记录。复位不会被统计为自动修复成功。' }}</p>
    <div v-if="confirmAction === 'execute' && confirmedPlan?.plan" class="diff"><div><span>目标：{{ confirmedPlan.target.name }}</span><pre>- {{ confirmedPlan.plan.before }}</pre></div><div><span>恢复为健康基线</span><pre>+ {{ confirmedPlan.plan.after }}</pre></div></div>
    <div class="modal-actions"><button class="button secondary" data-autofocus @click="confirmAction = null">取消</button><button class="button primary" :disabled="!!busy || (confirmAction === 'execute' && !!executeBlocked)" @click="confirmAction === 'execute' ? execute() : reset()">{{ confirmAction === 'execute' ? '确认执行' : '确认复位' }}<ArrowRight :size="16" /></button></div>
  </ModalLayer>

  <ModalLayer :open="needsLogin" title-id="login-title" :dismissable="false">
    <span class="feature-icon"><LockKeyhole :size="26" /></span><h2 id="login-title" class="login-title">连接你的工作空间</h2><p class="modal-description">服务已启用访问令牌，请输入令牌继续。令牌只保存在当前浏览器会话中。</p>
    <form @submit.prevent="login"><label class="form-label" for="access-token">访问令牌</label><input id="access-token" v-model="loginToken" class="form-input" data-autofocus type="password" autocomplete="current-password" :disabled="loggingIn" /><p v-if="connectionError" class="error-text login-error" role="alert">{{ connectionError }}</p><button class="button primary full-width" :disabled="!loginToken.trim() || loggingIn"><Loader2 v-if="loggingIn" :size="16" class="spin" />{{ loggingIn ? '正在连接…' : '连接工作空间' }}</button></form>
  </ModalLayer>

  <Teleport to="body"><Transition name="toast"><div v-if="toast" class="toast-message" :class="{ 'toast-error': toast.error, 'toast-raised': !!selected || !!confirmAction || environmentOpen || moreOpen || searchOpen }" :role="toast.error ? 'alert' : 'status'"><CircleAlert v-if="toast.error" :size="20" /><CheckCircle2 v-else :size="20" /><span>{{ toast.text }}</span><button class="icon-button" aria-label="关闭提示" @click="toast = null"><X :size="16" /></button></div></Transition></Teleport>
</template>
