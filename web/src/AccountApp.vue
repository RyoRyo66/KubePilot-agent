<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, reactive, ref } from 'vue'
import { ArrowRight, Check, CircleAlert, Eye, EyeOff, Loader2, LockKeyhole, LogOut, UserRound, FolderOpen } from 'lucide-vue-next'
import PlatformApp from './PlatformApp.vue'
import BrandMark from './components/BrandMark.vue'
import { accountAPI, APIError, type Session, type ActiveSession, type Workspace } from './account'

const session = ref<Session>({ authenticated: false, registrationOpen: true })
const restoring = ref(true), restoreError = ref(''), error = ref(''), notice = ref(''), busy = ref(false)
const mode = ref<'login'|'register'>('login'), reveal = ref(false), help = ref(false)
const credentials = reactive({ username: '', password: '', confirmation: '' })
const workspace = reactive({ name: '', description: '' })
const active = computed(() => session.value.authenticated && session.value.workspace && session.value.account ? session.value as ActiveSession : null)
const creating = computed(() => session.value.authenticated && !session.value.workspace)
let channel: BroadcastChannel | undefined
let requestVersion = 0
function announce() { channel?.postMessage('session-changed') }
function clearPasswords() { credentials.password = ''; credentials.confirmation = ''; reveal.value = false }
function accept(value: Session) {
  session.value = value; clearPasswords()
  if (value.authenticated && !value.workspace) { workspace.name = `${value.account!.username}的工作空间`; workspace.description = '' }
}
let refreshSequence = 0
async function restore(background = false) {
  const sequence = ++refreshSequence, version = requestVersion
  const foreground = !background || restoring.value
  if (foreground) { restoring.value = true; restoreError.value = '' }
  try {
    const next: Session = await accountAPI('/auth/session')
    if (sequence !== refreshSequence || version !== requestVersion) return
    const sameIdentity = session.value.authenticated === next.authenticated &&
      session.value.account?.id === next.account?.id && session.value.workspace?.id === next.workspace?.id
    restoreError.value = ''
    if (background && sameIdentity) session.value = next
    else { ++requestVersion; accept(next) }
  } catch {
    if (sequence === refreshSequence && version === requestVersion) restoreError.value = '无法同步登录状态，请检查连接后重试。'
  } finally {
    if (sequence === refreshSequence) restoring.value = false
  }
}
async function switchMode(next: 'login'|'register') {
  mode.value = next; error.value = ''; notice.value = ''; help.value = false; clearPasswords()
  await nextTick(); document.querySelector<HTMLInputElement>('#account-username')?.focus()
}
async function authenticate() {
  if (busy.value) return
  error.value = ''; notice.value = ''
  if (mode.value === 'register' && credentials.password !== credentials.confirmation) { error.value = '两次输入的密码不一致'; return }
  busy.value = true; const version = ++requestVersion
  try { const value=await accountAPI('/auth/' + mode.value, mode.value === 'register' ? credentials : { username: credentials.username, password: credentials.password }); if(version===requestVersion){++requestVersion;accept(value);announce()} }
  catch (e) { if(version===requestVersion)error.value = e instanceof Error ? e.message : '操作失败，请重试' }
  finally { busy.value = false }
}
async function createWorkspace() {
  if (busy.value) return
  busy.value = true; error.value = ''; const version = ++requestVersion
  try { const result = await accountAPI('/workspace/create', workspace,{accountId:session.value.account!.id}); if(version===requestVersion){++requestVersion; session.value = { ...session.value, workspace: result.workspace }; announce()} }
  catch (e) { if(version===requestVersion){if (e instanceof APIError && (e.status === 401||e.code==='session_changed')) expired('登录已失效，请重新登录'); else error.value = e instanceof Error ? e.message : '创建失败，请重试'} }
  finally { busy.value = false }
}
function expired(message = '登录已失效，请重新登录') {
  restoreError.value = ''; restoring.value = false
  requestVersion++; session.value = { authenticated: false, registrationOpen: session.value.registrationOpen }
  mode.value = 'login'; clearPasswords(); workspace.name = ''; workspace.description = ''; error.value = ''; notice.value = message; announce()
}
async function signOut() {
  busy.value = true; error.value = ''
  try { await accountAPI('/auth/logout', {},{accountId:session.value.account!.id}); expired('已退出登录') }
  catch (e) { if (e instanceof APIError && e.status === 401) expired(); else error.value = e instanceof Error ? e.message : '退出失败，请重试' }
  finally { busy.value = false }
}
function updateWorkspace(value: Workspace) { session.value = { ...session.value, workspace: value } }
onMounted(() => {
  sessionStorage.removeItem('kubepilot-platform-token')
  if ('BroadcastChannel' in window) { channel = new BroadcastChannel('kubepilot-account'); channel.onmessage = event => { if (event.data === 'session-changed') void restore(true) } }
  void restore()
})
onUnmounted(() => { requestVersion++; channel?.close() })
</script>

<template>
  <div v-if="active && restoreError" class="session-warning" role="status">{{ restoreError }} <button type="button" @click="restore(true)">重试</button></div>
  <PlatformApp v-if="active" :key="active.account.id + ':' + active.workspace.id" :session="active" @expired="expired" @workspace-change="updateWorkspace"/>
  <div v-else class="a-shell">
    <header class="a-header"><a href="/" class="a-brand"><BrandMark/><span>KUBEPILOT</span></a><span class="a-header-caption">Kubernetes 故障诊断与修复</span></header>
    <main class="a-main">
      <section v-if="restoring || restoreError" class="a-card a-loading" aria-live="polite">
        <Loader2 v-if="restoring" class="spin" :size="24"/><CircleAlert v-else :size="24"/>
        <h1>{{restoring ? '正在连接平台' : '连接失败'}}</h1><p>{{restoreError || '正在恢复登录状态。'}}</p>
        <button v-if="restoreError" class="a-primary" @click="restore()">重新连接</button>
      </section>
      <section v-else class="a-card" :key="creating ? 'workspace' : mode" :aria-labelledby="creating ? 'workspace-create-title' : 'account-title'">
        <div class="a-card-kicker"><span>{{creating ? '02' : '01'}}</span><span>{{creating ? 'PERSONAL WORKSPACE' : 'ACCOUNT ACCESS'}}</span><span class="a-kicker-line"/></div>
        <template v-if="creating">
          <div class="a-title-icon"><FolderOpen :size="23" :stroke-width="1.5"/></div>
          <h1 id="workspace-create-title">创建个人工作空间</h1>
          <p class="a-intro">工作空间用于保存接入配置、故障事件与执行记录。</p>
          <div class="a-signed-in"><span class="a-avatar">{{Array.from(session.account!.username)[0]?.toUpperCase()}}</span><span>当前账号 <strong>{{session.account!.username}}</strong></span><Check :size="15"/></div>
          <form @submit.prevent="createWorkspace" :aria-busy="busy">
            <label for="workspace-name">工作空间名称</label><input id="workspace-name" v-model="workspace.name" required maxlength="48" autocomplete="off" :disabled="busy" aria-describedby="workspace-name-note"/>
            <p id="workspace-name-note" class="a-field-note">可随时修改，不影响集群命名空间与接入地址。</p>
            <label for="workspace-description">描述 <span>选填</span></label><textarea id="workspace-description" v-model="workspace.description" maxlength="200" rows="3" placeholder="例如：毕业设计实验集群" :disabled="busy"/>
            <p v-if="error" class="a-error" role="alert"><CircleAlert :size="16"/>{{error}}</p>
            <button class="a-primary" :disabled="busy"><Loader2 v-if="busy" class="spin" :size="16"/>{{busy ? '正在创建…' : '创建工作空间'}}<ArrowRight v-if="!busy" :size="16"/></button>
          </form>
          <div class="a-next"><span>后续配置</span><ol><li>接入告警</li><li>连接模型</li><li>部署执行端</li></ol></div>
          <button class="a-text a-signout" :disabled="busy" @click="signOut"><LogOut :size="14"/>退出登录</button>
        </template>
        <template v-else>
          <div class="a-title-icon"><UserRound v-if="mode==='register'" :size="23" :stroke-width="1.5"/><LockKeyhole v-else :size="23" :stroke-width="1.5"/></div>
          <h1 id="account-title">{{mode==='register' ? '注册账号' : '登录 KubePilot'}}</h1>
          <p class="a-intro">{{mode==='register' ? '注册后创建个人工作空间，并完成集群接入。' : '使用账号访问你的个人工作空间。'}}</p>
          <p v-if="notice" class="a-notice" role="status"><Check :size="16"/>{{notice}}</p>
          <form @submit.prevent="authenticate" :aria-busy="busy">
            <label for="account-username">用户名</label><input id="account-username" v-model.trim="credentials.username" required minlength="3" maxlength="32" autocomplete="username" autocapitalize="none" spellcheck="false" :disabled="busy" :aria-describedby="mode==='register'?'username-note':undefined"/>
            <p v-if="mode==='register'" id="username-note" class="a-field-note">3–32 个字符，支持中英文、数字、_ 和 -。<br/>英文字母不区分大小写。</p>
            <label for="account-password">密码</label><div class="a-password"><input id="account-password" v-model="credentials.password" :type="reveal?'text':'password'" required :minlength="mode==='register'?12:undefined" maxlength="128" :autocomplete="mode==='register'?'new-password':'current-password'" :disabled="busy" :aria-describedby="mode==='register'?'password-note':undefined"/><button type="button" :aria-label="reveal?'隐藏密码':'显示密码'" :aria-pressed="reveal" @click="reveal=!reveal"><EyeOff v-if="reveal" :size="17"/><Eye v-else :size="17"/></button></div>
            <p v-if="mode==='register'" id="password-note" class="a-field-note">12–128 个字符，支持空格与符号。</p>
            <template v-if="mode==='register'"><label for="account-confirmation">确认密码</label><input id="account-confirmation" v-model="credentials.confirmation" :type="reveal?'text':'password'" required minlength="12" maxlength="128" autocomplete="new-password" :disabled="busy"/></template>
            <div v-else class="a-forgot"><button type="button" class="a-text" :aria-expanded="help" @click="help=!help">忘记密码？</button></div>
            <p v-if="help" class="a-help" role="status">请联系此平台的部署管理员重置密码。重置后，原有工作空间和接入配置会保留。</p>
            <p v-if="error" class="a-error" role="alert"><CircleAlert :size="16"/>{{error}}</p>
            <button class="a-primary" :disabled="busy"><Loader2 v-if="busy" class="spin" :size="16"/>{{busy ? (mode==='register'?'正在注册…':'正在登录…') : (mode==='register'?'注册并继续':'登录')}}<ArrowRight v-if="!busy" :size="16"/></button>
          </form>
          <div class="a-switch"><template v-if="mode==='register'">已有账号？<button class="a-text" :disabled="busy" @click="switchMode('login')">返回登录</button></template><template v-else-if="session.registrationOpen">还没有账号？<button class="a-text" :disabled="busy" @click="switchMode('register')">注册账号</button></template><span v-else>当前部署已关闭新账号注册。</span></div>
        </template>
      </section>
    </main>
    <footer class="a-footer"><span>KubePilot · 个人工作空间</span><span>告警采集 / 自动复核 / 受控修复</span></footer>
  </div>
</template>

<style scoped src="./account.css"></style>

<style scoped>
.session-warning { position: fixed; bottom: 12px; left: 50%; transform: translateX(-50%); z-index: 20; padding: 8px 12px; border: 1px solid #68615a; background: #302d29; color: #faf5ee; border-radius: 6px; font-size: 14px }
.session-warning button { color: inherit; cursor: pointer; text-decoration: underline; background: transparent; border: 0 }
</style>
