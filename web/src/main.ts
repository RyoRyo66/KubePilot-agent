import { createApp } from 'vue'
import App from './App.vue'
import './style.css'

// The new platform and the preserved lab share assets, but use separate servers/data.
fetch('/api/platform/ping').then(async response => {
  const info = response.ok ? await response.json() : null
  if (info?.product === 'kubepilot-platform') {
    const { default: PlatformApp } = await import('./AccountApp.vue')
    createApp(PlatformApp).mount('#app')
  } else createApp(App).mount('#app')
}).catch(() => createApp(App).mount('#app'))
