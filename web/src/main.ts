import { createApp, h } from 'vue'
import './style.css'
import './account.css'

async function start() {
  try {
    const response = await fetch('/api/platform/ping', { cache: 'no-store', signal: AbortSignal.timeout(10000) })
    const info = await response.json()
    if (response.ok && info?.product === 'kubepilot-platform') {
      const { default: AccountApp } = await import('./AccountApp.vue')
      createApp(AccountApp).mount('#app')
    } else if (response.status === 404 && info?.mode === 'standalone') {
      const { default: LabApp } = await import('./App.vue')
      createApp(LabApp).mount('#app')
    } else throw new Error('服务类型未确认')
  } catch {
    createApp({ render: () => h('main', {class:'a-shell'}, [
      h('section',{class:'a-card a-loading',role:'alert'},[
        h('h1','无法连接 KubePilot'),h('p','服务暂时不可用，请检查连接后重试。'),
        h('button',{type:'button',class:'a-primary',onClick:()=>window.location.reload()},'重新连接'),
      ]),
    ]) }).mount('#app')
  }
}
void start()
