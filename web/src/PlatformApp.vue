<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, reactive, ref } from 'vue'
import { Radio, BrainCircuit, ShieldCheck, ArrowRight, Check, Copy, Download, Plus, X, RefreshCw, Activity, Clock3, CircleCheck, CircleAlert, Loader2, LockKeyhole, ChevronRight, Pause, Terminal, FileCheck2, FlaskConical, PanelLeftClose, PanelLeftOpen, FileStack, ScanLine, BookOpen, Settings2, LogOut } from 'lucide-vue-next'
import type { PlatformState, Scope, PlatformCase } from './platform'
import { accountAPI, APIError, type ActiveSession, type Workspace } from './account'
import KnowledgeLibrary from './components/KnowledgeLibrary.vue'
import KnowledgePassages from './components/KnowledgePassages.vue'
import BrandMark from './components/BrandMark.vue'
import SplitPane from './components/SplitPane.vue'

const props = defineProps<{ session: ActiveSession }>()
const emit = defineEmits<{ expired: [message?: string]; workspaceChange: [workspace: Workspace] }>()

const data = ref<PlatformState | null>(null)
const page = ref<'setup'|'cases'|'events'|'knowledge'>(props.session.workspace.onboardingComplete ? 'cases' : 'setup')
const step = ref(1)
const sidebarCollapsed = ref(false)
const checkNames:Record<string,string>={service_backends:'Service 后端与端口',rollout_status:'发布进度与副本集',container_exits:'容器退出与重启',target_events:'目标资源事件'}
const bannerOpen = ref(true)
const mobilePane = ref<'config'|'guide'>('config')
const workspaceDialog = ref<HTMLDialogElement | null>(null)
const workspaceOpen = ref(false)
const accessURL = location.origin
const workspaceName = computed(() => props.session.workspace.name)
const avatar = computed(() => Array.from(props.session.account.username)[0]?.toUpperCase())
const workspaceTab = ref<'connection'|'workspace'|'password'>('connection')
const workspaceForm = reactive({ name: '', description: '' })
const passwordForm = reactive({ currentPassword: '', password: '', confirmation: '' })
const accountBusy = ref(false), accountError = ref(''), accountNotice = ref('')
function openWorkspace(){
 if(!workspaceDialog.value || workspaceDialog.value.open)return
 workspaceTab.value='connection';accountError.value='';accountNotice.value='';Object.assign(workspaceForm,{name:props.session.workspace.name,description:props.session.workspace.description});Object.assign(passwordForm,{currentPassword:'',password:'',confirmation:''})
 workspaceDialog.value.showModal();workspaceOpen.value=true
}
function workspaceKeydown(event:KeyboardEvent){
 if(event.key!=='Tab'||event.ctrlKey||event.metaKey)return
 const controls=workspaceDialog.value?.querySelectorAll<HTMLElement>('button:not(:disabled):not([tabindex="-1"]), input:not(:disabled), textarea:not(:disabled)')
 const first=controls?.[0],last=controls?.[controls.length-1]
 if(!first||!last)return
 if(event.shiftKey&&(document.activeElement===first||document.activeElement===workspaceDialog.value)){event.preventDefault();last.focus()}
 else if(!event.shiftKey&&document.activeElement===last){event.preventDefault();first.focus()}
}
async function configureWorkspace(targetStep:number){
 workspaceDialog.value?.close();page.value='setup';step.value=targetStep;mobilePane.value='config'
 await nextTick()
 document.querySelector<HTMLElement>('.p-steps button[aria-current="step"]')?.focus()
 document.querySelector('.p-form-panel')?.scrollTo({top:0})
}
const guideCopy = computed(() => [
 {title:'告警接入要求',description:'Alertmanager 发送 Webhook 通知；Connector 采集资源状态与日志，作为诊断依据。',note:'生成配置后可继续设置模型；告警是否到达以接收记录为准。'},
 {title:'模型接口要求',description:'使用支持工具调用的 OpenAI 兼容接口。诊断与复核分别使用独立请求。',note:'密钥在服务端保存；连接测试通过后保存配置。'},
 {title:'执行端部署要求',description:'Connector 部署在业务命名空间内，按指定资源和动作授权，并持久化执行记录。',note:'初次安装需要部署权限；执行端须能访问平台及业务健康检查地址。'},
 ][step.value-1]!)
const busy = ref('')
const error = ref('')
const connectionError = ref('')
const toast = ref('')
const source = reactive({ publicUrl: location.origin, allowHttp: location.protocol === 'http:' })
const model = reactive({ baseUrl: 'https://api.deepseek.com', model: '', apiKey: '' })
const scope = reactive<Scope>({ namespace: 'kube-agent-lab', image: '', targets: [{ kind:'Deployment', name:'', probeUrl:'' }], actions:['restore_image','restore_selector','restore_probe'], automatic: false })
const sourceConfig = ref('')
const manifest = ref('')
const chosen = ref('')
const detailTab = ref('timeline')
const detailTabs = [{id:'timeline',label:'处理过程'},{id:'evidence',label:'原始证据'},{id:'plan',label:'修复计划'}]
function moveTab(event:KeyboardEvent){
 const index=detailTabs.findIndex(t=>t.id===detailTab.value)
 const next=event.key==='ArrowRight'?(index+1)%3:event.key==='ArrowLeft'?(index+2)%3:event.key==='Home'?0:event.key==='End'?2:-1
 if(next<0)return
 event.preventDefault();detailTab.value=detailTabs[next]!.id
 const group=event.currentTarget as HTMLElement
 ;(group.querySelectorAll('button')[next] as HTMLButtonElement)?.focus()
}
const confirm = ref<HTMLDialogElement | null>(null)
const confirmCase = ref<PlatformCase | null>(null)
let timer: ReturnType<typeof setInterval> | undefined
let toastTimer: ReturnType<typeof setTimeout> | undefined
let hydrated = false
let requestEpoch = 0
let fetching = false
let disposed = false
const actions = [{id:'restore_image',label:'恢复健康镜像'},{id:'restore_selector',label:'恢复服务选择器'},{id:'restore_probe',label:'恢复健康检查'}]
const names: Record<string,string> = {new:'待诊断',diagnosing:'诊断中',reviewing:'自动复核中',planned:'等待确认',manual:'需人工核查',executing:'修复中',verifying:'验证中',resolved:'已恢复',rejected:'已阻断'}
const selected = computed(() => data.value?.cases.find(c => c.incident.id === chosen.value) || [...(data.value?.cases || [])].at(-1))
const cases = computed(() => [...(data.value?.cases || [])].reverse())
const openCases = computed(() => cases.value.filter(c => c.incident.status !== 'resolved').length)
const steps = computed(() => [
 { n:1, title:'接入告警', caption:data.value?.alerts.length ? '已收到告警通知' : data.value?.config.publicUrl ? '已配置，等待告警' : 'Alertmanager Webhook', done:!!data.value?.config.publicUrl, icon:Radio },
 { n:2, title:'连接模型', caption:data.value?.config.modelTested ? data.value.config.model : 'OpenAI 兼容接口', done:!!data.value?.config.modelTested, icon:BrainCircuit },
 { n:3, title:'连接执行端', caption:data.value?.online ? `${data.value.baselineReady} 个健康基线已就绪` : '集群内 Connector', done:!!data.value?.ready, icon:ShieldCheck },
])
const time = (v?:string) => v && !v.startsWith('0001') ? new Date(v).toLocaleString('zh-CN',{month:'2-digit',day:'2-digit',hour:'2-digit',minute:'2-digit',second:'2-digit',hour12:false}) : '尚无记录'
function message(text:string) { toast.value=text; clearTimeout(toastTimer); toastTimer=setTimeout(()=>toast.value='',4500) }
async function api(path:string, body?:unknown) {
 return await scopedAccountAPI('/platform'+path,body)
}
async function scopedAccountAPI(path:string,body?:unknown){
 try {const result=await accountAPI(path,body,{accountId:props.session.account.id,workspaceId:props.session.workspace.id});if(disposed)throw new Error('页面状态已更新');return result}
 catch(e){if(!disposed&&e instanceof APIError&&(e.status===401||e.code==='session_changed'))emit('expired');throw e}
}
async function refresh(force = false) {
 if(disposed || (!force && (fetching || busy.value)))return;
 const epoch=++requestEpoch; fetching=true
 try { const next=await api('/state') as PlatformState;if(disposed || epoch!==requestEpoch)return;data.value=next;connectionError.value=''
  if(!hydrated){ if(next.config.publicUrl){source.publicUrl=next.config.publicUrl;source.allowHttp=next.config.allowHttp} if(next.config.baseUrl)model.baseUrl=next.config.baseUrl;model.model=next.config.model
   if(next.config.generation>0)Object.assign(scope,JSON.parse(JSON.stringify(next.config.scope)));step.value=!next.config.publicUrl?1:!next.config.modelTested?2:3;hydrated=true }
 }catch(e){if(!disposed && epoch===requestEpoch)connectionError.value=e instanceof Error?e.message:'无法连接平台'}finally{if(epoch===requestEpoch)fetching=false}
}
async function action(key:string,fn:()=>Promise<void>){if(busy.value)return;busy.value=key;++requestEpoch;fetching=false;error.value='';try{await fn();await refresh(true)}catch(e){error.value=e instanceof Error?e.message:'操作失败'}finally{busy.value=''}}
async function saveSource(){await action('source',async()=>{const result=await api('/setup/source',source);sourceConfig.value=result.config;message('告警接收配置已生成，请加入 Alertmanager')})}
async function revealSource(){await action('source-config',async()=>{sourceConfig.value=(await api('/setup/source-config',{})).config})}
async function saveModel(){await action('model',async()=>{await api('/setup/model',model);model.apiKey='';message('工具调用测试通过，模型配置已保存')})}
async function saveConnector(){await action('connector',async()=>{manifest.value=(await api('/setup/connector',scope)).manifest;message('部署清单已生成，请在集群应用')})}
async function revealManifest(){await action('manifest',async()=>{manifest.value=(await api('/setup/manifest',{})).manifest})}
async function copy(value:string){try{await navigator.clipboard.writeText(value);message('已复制')}catch{message('浏览器未允许复制，请手动选中内容复制')}}
function download(value:string,name:string,type='text/yaml'){const u=URL.createObjectURL(new Blob([value],{type}));const a=document.createElement('a');a.href=u;a.download=name;a.click();setTimeout(()=>URL.revokeObjectURL(u),1000)}
function openConfirm(c:PlatformCase){confirmCase.value=c;confirm.value?.showModal()}
async function approve(){const c=confirmCase.value;if(!c?.review)return;confirm.value?.close();await action('approve',async()=>{await api('/cases/'+c.incident.id+'/approve',{planHash:c.review!.planHash});message('已提交具体计划，正在重新校验')})}
function canApprove(c:PlatformCase){return !!c.review && c.review.decision!=='reject' && !c.incident.executionCount && ['planned','manual'].includes(c.incident.status) && !!c.reviewExpires && new Date(c.reviewExpires).getTime()>Date.now()}
async function accountAction(fn:()=>Promise<void>) {
 if(accountBusy.value)return;accountBusy.value=true;accountError.value='';accountNotice.value=''
 try { await fn() } catch(e) { if(e instanceof APIError&&e.status===401)emit('expired');else accountError.value=e instanceof Error?e.message:'操作失败，请重试' } finally {accountBusy.value=false}
}
async function saveWorkspace(){await accountAction(async()=>{const result=await scopedAccountAPI('/workspace/update',workspaceForm);emit('workspaceChange',result.workspace);accountNotice.value='工作空间信息已保存'})}
async function changePassword(){await accountAction(async()=>{if(passwordForm.password!==passwordForm.confirmation)throw new Error('两次输入的密码不一致');await scopedAccountAPI('/auth/password',passwordForm);emit('expired','密码已修改，请使用新密码重新登录')})}
async function logout(){await accountAction(async()=>{await scopedAccountAPI('/auth/logout',{});emit('expired','已退出登录')})}
async function enterCases(){await action('complete',async()=>{if(data.value?.ready&&!props.session.workspace.onboardingComplete){const result=await scopedAccountAPI('/workspace/complete',{});emit('workspaceChange',result.workspace)}page.value='cases'})}
function accountTabKeydown(event:KeyboardEvent){
 const tabs=['connection','workspace','password'] as const;const i=tabs.indexOf(workspaceTab.value)
 const n=event.key==='ArrowRight'?(i+1)%3:event.key==='ArrowLeft'?(i+2)%3:event.key==='Home'?0:event.key==='End'?2:-1
 if(n<0)return;event.preventDefault();workspaceTab.value=tabs[n]!;nextTick(()=>document.querySelector<HTMLElement>('#workspace-tab-'+tabs[n])?.focus())
}
onMounted(()=>{document.body.classList.add('platform-mode');refresh();timer=setInterval(refresh,3000)})
onUnmounted(()=>{document.body.classList.remove('platform-mode');disposed=true;clearInterval(timer);clearTimeout(toastTimer)})
</script>

<template>
 <div class="p-shell" :class="{'p-collapsed':sidebarCollapsed,'p-banner-hidden':!bannerOpen}">
  <div v-if="bannerOpen" class="p-announcement"><p><strong>KubePilot</strong><span>Kubernetes 应用故障诊断与修复</span><button @click="page='setup';mobilePane='config'">接入设置</button></p><button class="p-announcement-close" aria-label="关闭顶部提示" @click="bannerOpen=false"><X :size="14"/></button></div>
  <aside class="p-side">
   <div class="p-brand-row"><a class="p-brand" href="#" aria-label="KubePilot 接入工作台" @click.prevent="page='setup'"><BrandMark/><span>KUBEPILOT</span></a><button class="p-icon p-collapse" :aria-label="sidebarCollapsed?'展开导航':'收起导航'" @click="sidebarCollapsed=!sidebarCollapsed"><PanelLeftOpen v-if="sidebarCollapsed" :size="16"/><PanelLeftClose v-else :size="16"/></button></div>
   <nav aria-label="主要导航">
    <button aria-label="接入设置" title="接入设置" :class="{active:page==='setup'}" @click="page='setup';mobilePane='config'"><FlaskConical :size="17"/><span>接入设置</span><span class="p-count">{{steps.filter(s=>s.done).length}}/3</span></button>
    <button aria-label="故障中心" title="故障中心" :class="{active:page==='cases'}" @click="page='cases'"><Activity :size="17"/><span>故障中心</span><span v-if="openCases" class="p-count">{{openCases}}</span></button>
    <button aria-label="知识库" title="知识库" :class="{active:page==='knowledge'}" @click="page='knowledge'"><BookOpen :size="17"/><span>知识库</span></button>
    <button aria-label="操作记录" title="操作记录" :class="{active:page==='events'}" @click="page='events'"><Clock3 :size="17"/><span>操作记录</span></button>
   </nav>
   <div class="p-side-bottom"><button class="p-balance" @click="page='cases'"><span>待处理事件</span><strong>{{openCases}}<small> 项</small></strong><span class="p-balance-mode">{{data?.connection.provider==='demo'?'Demo':data?.online?'Connected':'未连接'}}</span></button><button class="p-organization" aria-label="工作空间与连接" title="工作空间与连接" aria-haspopup="dialog" aria-controls="workspace-dialog" :aria-expanded="workspaceOpen" @click="openWorkspace"><span class="p-avatar">{{avatar}}</span><span><strong>{{workspaceName}}</strong><small>个人空间 · {{session.account.username}}</small></span><ChevronRight :size="16"/></button></div>
  </aside>
  <div class="p-body">
   <header class="p-top"><div class="p-breadcrumb"><FlaskConical v-if="page==='setup'" :size="17"/><Activity v-else-if="page==='cases'" :size="17"/><BookOpen v-else-if="page==='knowledge'" :size="17"/><Clock3 v-else :size="17"/><strong>{{page==='setup'?'接入工作台':page==='cases'?'故障中心':page==='knowledge'?'知识库':'操作记录'}}</strong><small>{{page==='setup'?'Workspace':page==='cases'?'Incidents':page==='knowledge'?'Knowledge':'Activity'}}</small></div><div class="p-top-right"><span class="p-live" :class="{online:data?.online}"><i/>{{data?.online?'Connector 在线':'Connector 未连接'}}</span><button class="p-icon" aria-label="刷新状态" @click="refresh()"><RefreshCw :size="16" :class="{spin:busy}"/></button><button class="p-icon" aria-label="查看工作空间信息" title="工作空间与连接" aria-haspopup="dialog" aria-controls="workspace-dialog" :aria-expanded="workspaceOpen" @click="openWorkspace"><Settings2 :size="16"/></button></div></header>
   <main class="p-main" :class="{'p-setup-main':page==='setup'}">
     <div v-if="error||connectionError" class="p-error" role="alert"><CircleAlert :size="18"/><span>{{error||connectionError}}</span><button class="p-icon" aria-label="关闭提示" @click="error='';connectionError=''"><X :size="15"/></button></div>
     <div v-if="data?.connection.provider==='demo'" class="p-demo"><FlaskConical :size="17"/><span>当前执行端使用模拟集群，用于协议与界面演示，尚未连接真实 Kubernetes。</span></div>
     <template v-if="page==='setup'">
      <div class="p-mobile-panes" role="group" aria-label="工作台视图"><button :aria-pressed="mobilePane==='config'" :class="{active:mobilePane==='config'}" @click="mobilePane='config'"><Settings2 :size="15"/>接入配置</button><button :aria-pressed="mobilePane==='guide'" :class="{active:mobilePane==='guide'}" @click="mobilePane='guide'"><BookOpen :size="15"/>接入指南</button></div>
      <SplitPane class="p-setup-grid" :class="{'p-show-guide':mobilePane==='guide'}" primary-id="connection-config" label="调整接入配置与指南的宽度" :initial-ratio="0.4235" :min-left="390" :min-right="350">
       <template #left>
       <section id="connection-config" class="p-editor">
        <div class="p-editor-title"><span>Configuration <small>接入配置</small></span><span>{{steps.filter(s=>s.done).length}} / 3 已就绪</span></div>
        <div class="p-steps" aria-label="接入步骤"><button v-for="s in steps" :key="s.n" :class="{selected:step===s.n,done:s.done}" @click="step=s.n" :aria-current="step===s.n?'step':undefined"><span class="p-step-number"><Check v-if="s.done" :size="13"/><template v-else>0{{s.n}}</template></span>{{s.title}}</button></div>
        <div class="p-form-panel" :key="step">
        <template v-if="step===1">
         <div class="p-section-title"><span class="p-feature-icon"><Radio :size="22"/></span><div><h2>Alertmanager 告警接入</h2><p>配置 Webhook 接收地址与身份凭证。</p></div></div>
         <form @submit.prevent="saveSource">
          <label>平台对集群可达的地址<input v-model="source.publicUrl" type="url" required placeholder="https://kubepilot.example.com"/></label>
          <p class="p-field-note">填写平台的域名或服务器 IP。集群中的 localhost 指向容器自身，不能使用它连接你的电脑。</p>
          <label class="p-check"><input v-model="source.allowHttp" type="checkbox"/><span>仅本机回环地址允许 HTTP<small>通过网络使用时，建议由管理员配置 HTTPS。</small></span></label>
          <div class="p-form-actions"><button class="p-button primary" :disabled="!!busy"><Loader2 v-if="busy==='source'" class="spin" :size="16"/>生成接收配置<ArrowRight :size="16"/></button><button v-if="data?.config.publicUrl" class="p-button ghost" type="button" @click="revealSource" :disabled="!!busy">查看已保存配置</button></div>
         </form>
         <div v-if="sourceConfig" class="p-config"><div><strong>Alertmanager receiver</strong><button class="p-icon" aria-label="复制告警接入配置" @click="copy(sourceConfig)"><Copy :size="15"/></button></div><pre tabindex="0">{{sourceConfig}}</pre><p>加入现有配置，将目标告警路由到 kubepilot，然后重新加载 Alertmanager。专用令牌仅允许发送告警。</p></div>
         <div class="p-arrival"><span class="p-status-dot" :class="{good:data?.alerts.length}"/><div><strong>{{data?.alerts.length?'已收到告警转发':'等待第一条告警'}}</strong><p>{{data?.alerts.length?`最近接收于 ${time(data.alerts.at(-1)?.receivedAt)}`:'配置生成后可以继续下一步，首次通知到达时会在这里更新。'}}</p></div></div>
         <div class="p-bottom-action"><button class="p-button" @click="step=2" :disabled="!data?.config.publicUrl">下一步：连接模型<ArrowRight :size="16"/></button></div>
        </template>
        <template v-else-if="step===2">
         <div class="p-section-title"><span class="p-feature-icon"><BrainCircuit :size="22"/></span><div><h2>模型接口配置</h2><p>支持 DeepSeek 等提供工具调用的 OpenAI 兼容接口。</p></div></div>
         <form @submit.prevent="saveModel">
          <label>API 基地址<input v-model="model.baseUrl" type="url" required placeholder="https://api.deepseek.com"/></label>
          <label>模型名称<input v-model="model.model" required autocomplete="off" placeholder="填写服务商提供的模型名称"/></label>
          <label>API Key<input v-model="model.apiKey" type="password" :required="!data?.config.apiKeySet" autocomplete="new-password" :placeholder="data?.config.apiKeySet?'已保存；留空保留当前密钥':'输入模型服务的 API Key'"/></label>
          <p class="p-field-note">密钥在平台服务端加密保存，不写入浏览器存储。连接测试会实际调用一次模型工具。</p>
          <button class="p-button primary" :disabled="!!busy"><Loader2 v-if="busy==='model'" class="spin" :size="16"/>{{busy==='model'?'正在测试工具调用…':'测试连接并保存'}}<ArrowRight v-if="busy!=='model'" :size="16"/></button>
         </form>
         <div class="p-review-explain"><FileCheck2 :size="20"/><div><strong>独立复核机制</strong><p>复核重新读取原始证据与修复计划。模型只能提出审查意见，最终权限由程序规则控制。</p></div></div>
         <div v-if="data?.config.modelTested" class="p-arrival"><CircleCheck :size="18" class="good-text"/><div><strong>工具调用测试通过</strong><p>{{data.config.model}} · 配置修改后需重新测试</p></div></div>
         <div class="p-bottom-action"><button class="p-button" @click="step=3" :disabled="!data?.config.modelTested">下一步：连接执行端<ArrowRight :size="16"/></button></div>
        </template>
        <template v-else>
         <div class="p-section-title"><span class="p-feature-icon"><ShieldCheck :size="22"/></span><div><h2>Connector 部署与授权</h2><p>设置执行端、资源范围和允许的修复动作。</p></div></div>
         <form @submit.prevent="saveConnector">
          <div class="p-two-fields"><label>授权命名空间<input v-model="scope.namespace" required placeholder="kube-agent-lab"/></label><label>Connector 镜像<input v-model="scope.image" required placeholder="registry.example.com/team/connector@sha256:…"/></label></div>
          <p class="p-field-note">填写管理员发布的镜像地址和完整 SHA-256 摘要。下方对象必须已存在于这个命名空间。</p>
          <div class="p-label-row"><strong>授权资源与业务检查</strong><button class="p-text-button" type="button" @click="scope.targets.push({kind:'Service',name:'',probeUrl:''})" :disabled="scope.targets.length>=20"><Plus :size="14"/>添加资源</button></div>
          <div v-for="(target,index) in scope.targets" :key="index" class="p-target">
           <div class="p-target-row">
            <label class="p-kind-label">类型<select v-model="target.kind" :aria-label="`资源 ${index+1} 类型`"><option>Deployment</option><option>Service</option></select></label>
            <label>资源名称<input v-model="target.name" required :aria-label="`资源 ${index+1} 名称`" placeholder="checkout-api"/></label>
           </div>
           <label>业务健康检查地址<input v-model="target.probeUrl" required type="url" :aria-label="`资源 ${index+1} 健康检查地址`" placeholder="http://checkout-api.kube-agent-lab.svc/health"/></label>
           <button class="p-icon p-target-remove" type="button" :aria-label="`移除资源 ${index+1}`" :title="scope.targets.length===1?'至少保留一个授权资源':'移除这项授权资源'" :disabled="scope.targets.length===1" @click="scope.targets.splice(index,1)"><X :size="16"/></button>
          </div>
          <div class="p-label-row"><strong>允许的恢复动作</strong><span class="p-muted">仅恢复管理员已批准的基线</span></div>
          <div class="p-action-checks"><label v-for="a in actions" :key="a.id" class="p-check"><input v-model="scope.actions" type="checkbox" :value="a.id"/><span>{{a.label}}</span></label></div>
          <label class="p-check p-auto-consent"><input v-model="scope.automatic" type="checkbox"/><span><strong>允许在上述范围内自动修复</strong><small>规则与自动复核均通过后执行；每个事件最多尝试一次写操作。</small></span></label>
          <button class="p-button primary" :disabled="!!busy || !data?.config.publicUrl || !scope.actions.length"><Loader2 v-if="busy==='connector'" class="spin" :size="16"/>生成授权与部署清单<ArrowRight :size="16"/></button>
          <p v-if="data?.config.generation" class="p-field-note">重新生成会更新授权并使旧接入令牌失效，需要重新应用清单。</p>
         </form>
         <div v-if="manifest || data?.config.generation" class="p-deploy"><div class="p-label-row"><strong>部署执行端</strong><button v-if="!manifest" class="p-text-button" @click="revealManifest" :disabled="!!busy">读取已生成清单</button><button v-else class="p-text-button" @click="download(manifest,'kubepilot-connector.yaml')"><Download :size="15"/>下载 YAML</button></div><div class="p-command"><Terminal :size="16"/><code>kubectl apply -f kubepilot-connector.yaml</code><button class="p-icon" aria-label="复制安装命令" @click="copy('kubectl apply -f kubepilot-connector.yaml')"><Copy :size="15"/></button></div><p>Connector 自动连接平台，在应用健康且业务探测通过时记录初始基线。已有故障不会被当作健康基线。</p></div>
         <div class="p-arrival"><span class="p-status-dot" :class="{good:data?.online}"/><div><strong>{{data?.online?'执行端已连接':'等待 Connector 上线'}}</strong><p>{{data?.online?`${data.baselineReady} / ${data.config.scope.targets.length} 个资源已建立健康基线`:'需要集群能访问平台地址，并能为 Connector 分配持久化存储。'}}</p></div></div>
         <div v-if="data?.online" class="p-baselines"><div v-for="(value,key) in data.connection.baselines" :key="key"><span>{{key}}</span><small :class="{'good-text':value==='ready'}">{{value==='ready'?'已批准的健康基线就绪':value==='pending'?'等待健康采样或管理员批准':value}}</small></div></div>
         <div class="p-bottom-action"><button class="p-button" :disabled="!!busy" @click="enterCases">{{data?.ready&&!session.workspace.onboardingComplete?'完成接入并进入故障中心':'前往故障中心'}}<ArrowRight :size="16"/></button></div>
        </template>
        </div>
        <footer class="p-editor-footer"><span class="p-footer-engine"><Plus :size="15"/><code>{{data?.config.model || '等待连接模型'}}</code></span><span :class="{'good-text':data?.ready}"><CircleCheck v-if="data?.ready" :size="13"/>{{data?.ready?'接入已就绪':`第 ${step} 步 · ${steps[step-1]?.title}`}}</span></footer>
       </section>
       </template>
       <template #right>
       <aside class="p-guide">
        <div class="p-guide-document">
         <div class="p-guide-heading"><div class="p-guide-title-row"><h1>接入说明</h1><span>v{{data?.version || '0.3.1'}}</span></div><p>配置告警源、模型服务和集群执行端。</p></div>
         <div class="p-guide-context"><strong>{{guideCopy.title}}</strong><p>{{guideCopy.description}}</p><span>{{guideCopy.note}}</span></div>
         <div class="p-guide-label">诊断与验证</div>
         <div class="p-capabilities"><article><div><FileStack :size="18"/><h2>诊断证据</h2></div><p>根据告警、资源状态、事件与日志生成诊断结论。</p></article><article><div><ScanLine :size="18"/><h2>恢复验证</h2></div><p>执行后检查资源就绪状态、服务端点和业务响应。</p></article></div>
         <div class="p-guide-label">接入状态 <span>选择项目查看配置</span></div>
         <div class="p-guide-rows"><button v-for="s in steps" :key="s.n" @click="step=s.n;mobilePane='config'"><span class="p-guide-index">0{{s.n}}</span><strong>{{s.title}}</strong><span>{{s.caption}}</span><CircleCheck v-if="s.done" :size="16" class="good-text"/><ChevronRight v-else :size="16"/></button></div>
         <div class="p-guide-label">执行约束</div>
         <dl class="p-constraints"><div><dt>授权范围</dt><dd>限定命名空间、资源名称与恢复动作</dd></div><div><dt>执行前检查</dt><dd>规则校验、自动复核、资源版本核对</dd></div><div><dt>写入限制</dt><dd>每个故障事件最多尝试一次写操作</dd></div><div><dt>异常处理</dt><dd>证据不足、状态变化或验证失败时转人工核查</dd></div></dl>
         <footer class="p-guide-footer"><LockKeyhole :size="14"/><span>集群凭证保存在 Connector 所在集群内</span></footer>
        </div>
       </aside>
       </template>
      </SplitPane>
     </template>
     <KnowledgeLibrary v-else-if="page==='knowledge'" :api="api"/>
     <template v-else-if="page==='cases'">
      <div class="p-page-heading"><div><h1>故障中心</h1><p>查看故障证据、复核结论、执行状态与验证结果。</p></div><div class="p-heading-actions"><button v-if="data?.config.scope.automatic" class="p-button ghost" :disabled="!!busy" @click="action('pause',async()=>{await api('/pause',{});scope.automatic=false;message('已暂停后续自动修复，已提交操作仍需验证')})"><Pause :size="15"/>暂停自动修复</button><button class="p-button primary" :disabled="!!busy||data?.busy||!data?.online" @click="action('scan',async()=>{await api('/scan',{});message('已开始实时巡检')})"><Loader2 v-if="data?.busy" class="spin" :size="16"/><RefreshCw v-else :size="16"/>{{data?.busy?'任务运行中':'立即巡检'}}</button></div></div>
      <div class="p-metrics"><div><small>待处理事件</small><strong>{{openCases}}</strong></div><div><small>已验证恢复</small><strong>{{cases.filter(c=>c.incident.status==='resolved').length}}</strong></div><div><small>收到告警</small><strong>{{data?.alerts.length || 0}}</strong></div><div><small>执行策略</small><strong class="p-metric-text">{{data?.config.scope.automatic?'受控自动修复':'人工确认执行'}}</strong></div></div>
      <div v-if="data?.lastError" class="p-notice"><CircleAlert :size="18"/><span>{{data.lastError}}</span></div>
      <div v-if="!cases.length" class="p-panel p-empty"><Activity :size="36" :stroke-width="1"/><h2>{{data?.online?'当前没有发现故障':'连接集群后开始巡检'}}</h2><p>{{data?.online?'新的实时巡检发现会出现在这里，告警通知单独记录在下方。':'完成三步接入，平台会自动采集证据并按授权范围处理。'}}</p><button v-if="!data?.online" class="p-button" @click="page='setup';step=3">连接执行端<ArrowRight :size="16"/></button></div>
      <SplitPane v-else class="p-case-grid" primary-id="case-list" label="调整故障列表与详情的宽度" :initial-ratio="0.3023" :min-left="210" :min-right="285">
       <template #left>
       <div id="case-list" class="p-panel p-case-list"><button v-for="c in cases" :key="c.incident.id" @click="chosen=c.incident.id;detailTab='timeline'" :class="{selected:selected?.incident.id===c.incident.id}"><div><span class="p-status" :class="c.incident.status">{{names[c.incident.status]||c.incident.status}}</span><small>{{time(c.incident.firstSeen)}}</small></div><strong>{{c.incident.title}}</strong><p>{{c.incident.target.kind}} / {{c.incident.target.name}}</p></button></div>
       </template>
       <template #right>
       <section v-if="selected" class="p-panel p-case-detail"><div class="p-case-head"><div><span class="p-eyebrow">{{selected.incident.target.namespace}}</span><h2>{{selected.incident.target.name}}</h2><p>{{selected.incident.title}}</p></div><span class="p-status" :class="selected.incident.status">{{names[selected.incident.status]}}</span></div>
        <div v-if="selected.review" class="p-review-result" :class="selected.review.decision"><FileCheck2 :size="20"/><div><strong>自动复核 · {{selected.review.decision==='approve'?'通过':selected.review.decision==='reject'?'拒绝':'需人工核查'}}</strong><p>{{selected.review.reason}}</p><KnowledgePassages :items="selected.review.knowledge"/></div></div>
        <details v-if="selected.investigation" class="p-diagnosis p-investigation">
          <summary>{{selected.investigation.assessment?.disposition==='consider_plan'?'初诊 · 依据充分':selected.investigation.assessment?.disposition==='need_evidence'?'初诊 · 待补充证据':'初诊 · 候选原因与核查项'}} <small>{{selected.investigation.engine}}</small></summary>
          <p v-if="selected.investigation.assessment?.disposition==='inspect'">{{selected.investigation.assessment.conclusion}}</p>
          <KnowledgePassages :items="selected.investigation.knowledge"/>
          <p v-if="selected.investigation.checks?.length">模型选择的检查 · {{selected.investigation.checks.map(c=>checkNames[c]||c).join('、')}}</p>
          <article v-for="(h,i) in selected.investigation.hypotheses" :key="i"><strong>{{h.cause}}</strong><p>{{h.check}}</p><small>首次证据 · {{h.evidenceIds.join('、')}}</small></article>
          <details><summary>查看首次采集证据</summary><article v-for="e in selected.investigation.initialEvidence" :key="e.id"><strong>{{e.summary}}</strong><p>{{e.detail}}</p><small>{{e.source}} · {{e.id}}</small></article></details>
        </details>
        <details v-if="selected.investigation?.followUps?.length" class="p-diagnosis p-investigation">
          <summary>补查记录 · {{selected.investigation.followUps.length}} 轮</summary>
          <article v-for="(round,n) in selected.investigation.followUps" :key="n">
            <strong>第 {{n+1}} 轮 · {{round.disposition==='inspect'?'继续核查':round.disposition==='need_evidence'?'证据不足':'提交诊断'}}</strong>
            <p>{{round.conclusion}}</p><small>引用证据 · {{round.evidenceIds.join('、')}}</small>
            <p v-if="round.checks?.length">下一步检查 · {{round.checks.map(c=>checkNames[c]||c).join('、')}}</p>
            <KnowledgePassages :items="round.knowledge"/>
          </article>
        </details>
        <div v-if="selected.incident.diagnosis" class="p-diagnosis"><strong>诊断结论</strong><small> {{selected.incident.diagnosis.engine}}</small><p>{{selected.incident.diagnosis.conclusion}}</p>
          <KnowledgePassages :items="selected.incident.diagnosis.knowledge"/>
          <small v-if="selected.incident.diagnosis.evidenceIds?.length">引用证据 · {{selected.incident.diagnosis.evidenceIds.join('、')}}</small>
          <p v-if="selected.incident.diagnosis.limitations">{{selected.incident.diagnosis.limitations}}</p>
          <ul v-if="selected.incident.diagnosis.recommendations?.length"><li v-for="(advice,i) in selected.incident.diagnosis.recommendations" :key="i">{{advice}}</li></ul>
        </div>
        <div class="p-tabs" role="tablist" aria-label="故障详情" @keydown="moveTab"><button v-for="t in detailTabs" :key="t.id" :id="'detail-tab-'+t.id" aria-controls="detail-panel" role="tab" :tabindex="detailTab===t.id?0:-1" :aria-selected="detailTab===t.id" :class="{active:detailTab===t.id}" @click="detailTab=t.id">{{t.label}}</button></div>
        <div id="detail-panel" role="tabpanel" :aria-labelledby="'detail-tab-'+detailTab">
         <ol v-if="detailTab==='timeline'" class="p-timeline"><li v-for="(item,i) in selected.incident.timeline" :key="i"><span class="p-timeline-dot"/><div><strong>{{item.title}}</strong><time>{{time(item.at)}}</time><p>{{item.detail}}</p></div></li></ol>
         <div v-else-if="detailTab==='evidence'" class="p-evidence"><article v-for="e in selected.incident.evidence" :key="e.id"><small>{{e.source}} · {{e.id}}</small><strong>{{e.summary}}</strong><pre>{{e.detail}}</pre></article></div>
         <div v-else class="p-plan"><template v-if="selected.incident.plan"><p>{{selected.incident.plan.summary}}</p><label>变更前<pre>{{selected.incident.plan.before}}</pre></label><label>变更后<pre>{{selected.incident.plan.after}}</pre></label><small>健康基线 {{selected.incident.plan.baselineId}}</small></template><p v-else>尚未生成满足条件的修复计划。</p></div>
        </div>
        <div class="p-case-actions"><button v-if="canApprove(selected)" class="p-button primary" :disabled="!!busy||data?.busy" @click="openConfirm(selected)">核对并人工确认</button><button v-if="!selected.incident.executionCount && ['manual','rejected','new','planned'].includes(selected.incident.status)" class="p-button" :disabled="!!busy||data?.busy||!data?.online" @click="action('retry',async()=>{await api('/cases/'+selected!.incident.id+'/retry',{});message('正在重新采集证据，旧复核已作废')})">重新诊断与复核</button><button class="p-button ghost" @click="download(JSON.stringify(selected,null,2),'kubepilot-case-'+selected.incident.id+'.json','application/json')"><Download :size="15"/>导出记录</button></div>
       </section>
       </template>
      </SplitPane>
      <section class="p-panel p-alerts"><div class="p-label-row"><h3>最近告警通知</h3><small>通知状态与业务恢复分开记录</small></div><p v-if="!data?.alerts.length" class="p-muted">还没有收到 Alertmanager 通知。</p><div v-for="a in [...(data?.alerts || [])].reverse().slice(0,8)" :key="a.id" class="p-alert-row"><span class="p-status" :class="a.status==='resolved'?'resolved':'manual'">{{a.status==='resolved'?'告警已恢复':'告警触发'}}</span><div><strong>{{a.name}}</strong><p>{{a.summary || a.namespace || '未提供摘要'}}</p></div><small>{{time(a.receivedAt)}} · {{a.count}} 次</small></div></section>
     </template>
     <template v-else><div class="p-page-heading"><div><h1>操作记录</h1><p>查看接入配置变更、复核结果与执行记录。</p></div><button class="p-button" :disabled="!data?.events.length" @click="download(JSON.stringify(data?.events,null,2),'kubepilot-events.json','application/json')"><Download :size="16"/>导出记录</button></div><section class="p-panel p-events"><div v-for="e in [...(data?.events||[])].reverse()" :key="e.id"><span class="p-event-icon"><Clock3 :size="16"/></span><div><strong>{{e.detail}}</strong><small>{{e.action}}</small></div><time>{{time(e.at)}}</time></div><div v-if="!data?.events.length" class="p-empty"><p>暂无操作记录。</p></div></section></template>
   </main>
  </div>
  <dialog id="workspace-dialog" ref="workspaceDialog" class="p-confirm p-workspace-dialog" aria-labelledby="workspace-title" @close="workspaceOpen=false" @click.self="workspaceDialog?.close()" @keydown="workspaceKeydown">
   <div>
    <div class="p-label-row"><h2 id="workspace-title">工作空间与连接</h2><button class="p-icon" aria-label="关闭工作空间信息" autofocus @click="workspaceDialog?.close()"><X :size="18"/></button></div>
    <p class="p-workspace-caption">{{workspaceName}} · v{{data?.version || '0.3.1'}}</p>
    <div class="p-tabs p-account-tabs" role="tablist" aria-label="账号与空间设置" @keydown="accountTabKeydown">
     <button v-for="t in [{id:'connection',label:'连接状态'},{id:'workspace',label:'工作空间'},{id:'password',label:'修改密码'}]" :key="t.id" :id="'workspace-tab-'+t.id" role="tab" aria-controls="workspace-settings-panel" :tabindex="workspaceTab===t.id?0:-1" :aria-selected="workspaceTab===t.id" :class="{active:workspaceTab===t.id}" @click="workspaceTab=t.id as typeof workspaceTab;accountError='';accountNotice=''">{{t.label}}</button>
    </div>
    <div id="workspace-settings-panel" role="tabpanel" :aria-labelledby="'workspace-tab-'+workspaceTab">
     <template v-if="workspaceTab==='connection'">
      <p v-if="!data" role="status">正在获取连接状态。</p>
      <template v-else>
       <p v-if="data.connection.provider==='demo'" class="p-workspace-demo">当前执行端为模拟集群。</p>
       <dl class="p-workspace-details">
        <div><dt>访问地址</dt><dd>{{accessURL}}</dd></div>
        <div><dt>平台接收地址</dt><dd>{{data.config.publicUrl || '未配置'}}</dd></div>
        <div><dt>告警接入</dt><dd>{{!data.config.publicUrl?'未配置':data.alerts.length?`已收到 ${data.alerts.length} 条告警记录`:'已配置，尚未收到告警'}}</dd></div>
        <div><dt>模型接口</dt><dd>{{data.config.modelTested?`${data.config.model} · 已通过连接测试`:'未配置'}}</dd></div>
        <div><dt>集群执行端</dt><dd :class="{'good-text':data.online}">{{data.online?'在线':data.connection.id?'离线':'未连接'}}</dd></div>
        <div><dt>授权范围</dt><dd>{{data.config.generation?`${data.config.scope.namespace} · ${data.config.scope.targets.length} 个资源`:'未配置'}}</dd></div>
        <div><dt>执行策略</dt><dd>{{!data.config.generation?'未配置':data.config.scope.automatic?'受控自动修复':'人工确认执行'}}</dd></div>
       </dl>
      </template>
      <div class="p-form-actions p-workspace-actions"><button class="p-button" :disabled="!data" @click="configureWorkspace(1)"><Radio :size="15"/>告警配置</button><button class="p-button" :disabled="!data" @click="configureWorkspace(2)"><BrainCircuit :size="15"/>模型配置</button><button class="p-button" :disabled="!data" @click="configureWorkspace(3)"><ShieldCheck :size="15"/>执行端配置</button></div>
     </template>
     <form v-else-if="workspaceTab==='workspace'" class="p-account-form" @submit.prevent="saveWorkspace">
      <label>工作空间名称<input v-model="workspaceForm.name" maxlength="48" required :disabled="accountBusy"/></label>
      <label>描述 <small>选填</small><textarea v-model="workspaceForm.description" maxlength="200" rows="3" :disabled="accountBusy"/></label>
      <p class="p-field-note">名称修改不会改变接入地址和 Kubernetes 命名空间。</p>
      <div class="p-form-actions"><button class="p-button primary" :disabled="accountBusy"><Loader2 v-if="accountBusy" class="spin" :size="15"/>保存工作空间</button></div>
     </form>
     <form v-else class="p-account-form" @submit.prevent="changePassword">
      <label>当前密码<input v-model="passwordForm.currentPassword" type="password" autocomplete="current-password" required :disabled="accountBusy"/></label>
      <label>新密码<input v-model="passwordForm.password" type="password" autocomplete="new-password" minlength="12" maxlength="128" required :disabled="accountBusy"/></label>
      <label>确认新密码<input v-model="passwordForm.confirmation" type="password" autocomplete="new-password" minlength="12" maxlength="128" required :disabled="accountBusy"/></label>
      <p class="p-field-note">密码需要 12–128 个字符。修改后，所有设备需要重新登录。</p>
      <div class="p-form-actions"><button class="p-button primary" :disabled="accountBusy"><Loader2 v-if="accountBusy" class="spin" :size="15"/>修改密码并重新登录</button></div>
     </form>
    </div>
    <p v-if="accountError" class="p-account-error" role="alert">{{accountError}}</p><p v-if="accountNotice" class="p-account-notice" role="status">{{accountNotice}}</p>
    <div class="p-account-footer"><span>{{session.account.username}}<small>个人账号</small></span><button class="p-button ghost" :disabled="accountBusy" @click="logout"><LogOut :size="15"/>退出登录</button></div>
    <p class="p-signout-note">退出后，已授权的后台任务继续运行。可在故障中心单独暂停自动修复。</p>
   </div>
  </dialog>
  <dialog ref="confirm" class="p-confirm"><div v-if="confirmCase"><div class="p-label-row"><h2>确认执行具体修复计划</h2><button class="p-icon" aria-label="关闭确认" @click="confirm?.close()"><X :size="18"/></button></div><p>{{confirmCase.incident.target.namespace}} / {{confirmCase.incident.target.kind}} / {{confirmCase.incident.target.name}}</p><div class="p-plan"><label>变更前<pre>{{confirmCase.incident.plan?.before}}</pre></label><label>变更后<pre>{{confirmCase.incident.plan?.after}}</pre></label></div><p>执行前仍会重新核对版本和基线；每个事件最多尝试一次写操作。</p><div class="p-form-actions"><button class="p-button" @click="confirm?.close()">取消</button><button class="p-button primary" @click="approve">确认执行当前计划</button></div></div></dialog>
  <div v-if="toast" class="p-toast" role="status"><CircleCheck :size="17"/>{{toast}}</div>
 </div>
</template>

<style scoped src="./platform.css"></style>
