<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { Plus, Search } from 'lucide-vue-next'
import type { Passage } from '../types'

interface Document { id:string; title:string; content:string; source?:string }
const props = defineProps<{ api:(path:string,body?:unknown)=>Promise<any> }>()
const docs = ref<Document[]>([])
const hits = ref<Passage[]>([])
const query = ref(''), searched = ref(''), title = ref(''), content = ref('')
const editing = ref(false), removal = ref('')
const busy = ref(false), error = ref(''), notice = ref('')
const sizeLimit = 32 * 1024

async function run(work:()=>Promise<void>) {
  if (busy.value) return
  busy.value=true; error.value=''; notice.value=''
  try { await work() } catch (err) { error.value=err instanceof Error ? err.message : '资料操作失败' }
  finally { busy.value=false }
}
async function list() { docs.value=await props.api('/knowledge') }
onMounted(()=>run(list))

async function search() {
  const value=query.value.trim()
  if (!value) { searched.value=''; hits.value=[]; return }
  const result=await props.api('/knowledge/search?q='+encodeURIComponent(value))
  hits.value=result; searched.value=value
}
async function loadFile(event:Event) {
  const input=event.target as HTMLInputElement
  const file=input.files?.[0]; input.value=''
  if (!file) return
  await run(async()=>{
    if (file.size>sizeLimit || !/\.(md|txt)$/i.test(file.name)) throw Error('请选择不超过 32 KiB 的 .md 或 .txt 文件')
    const decoder=new TextDecoder('utf-8',{fatal:true})
    let body:string
    try { body=decoder.decode(await file.arrayBuffer()) } catch { throw Error('请将文件另存为 UTF-8 编码后再导入') }
    title.value=file.name.replace(/\.(md|txt)$/i,''); content.value=body
  })
}
async function refreshAfterChange() { await list(); if (searched.value) await search() }
async function add() {
  await run(async()=>{
    await props.api('/knowledge',{title:title.value,content:content.value})
    title.value=''; content.value=''; editing.value=false
    await refreshAfterChange(); notice.value='资料已加入索引，下次诊断可检索。'
  })
}
async function remove(doc:Document) {
  await run(async()=>{
    await props.api('/knowledge/'+encodeURIComponent(doc.id)+'/remove',{})
    removal.value=''; await refreshAfterChange(); notice.value='资料已从当前索引移除。'
  })
}
</script>

<template>
  <section class="kb">
    <div class="p-page-heading"><div><h1>排障知识库</h1><p>按故障现象、事件名称或日志关键词检索参考资料。</p></div>
      <button class="p-button primary" :disabled="busy" :aria-expanded="editing" @click="editing=!editing"><Plus :size="15"/>{{ editing?'收起导入':'导入资料' }}</button>
    </div>
    <p v-if="error" class="kb-error" role="alert">{{error}} <button class="p-button ghost" :disabled="busy" @click="run(list)">刷新资料列表</button></p>
    <p v-if="notice" class="kb-note" role="status">{{notice}}</p>
    <form v-if="editing" class="p-panel kb-import" @submit.prevent="add">
      <h2>导入工作空间资料</h2>
      <p>可粘贴故障复盘，或选择 UTF-8 的 Markdown、TXT 文件。命中段落会发送给此工作空间配置的模型，请使用已脱敏的资料。</p>
      <label>选择文件<input type="file" accept=".md,.txt" :disabled="busy" @change="loadFile"></label>
      <label>资料名称<input v-model="title" required maxlength="100" placeholder="例如：订单服务启动失败复盘"></label>
      <label>正文<textarea v-model="content" required rows="6" placeholder="可用 Markdown 标题分隔故障现象、核查步骤和恢复条件。"></textarea></label>
      <div class="kb-actions"><small>每份最多 32 KiB，每个工作空间最多 16 份</small><button class="p-button primary" :disabled="busy||!title.trim()||!content.trim()">保存并建立索引</button></div>
    </form>
    <form class="kb-search" @submit.prevent="run(search)">
      <input v-model="query" aria-label="检索关键词" type="search" placeholder="例如：FailedCreate、镜像拉取、配额" maxlength="240">
      <button class="p-button" :disabled="busy||!query.trim()"><Search :size="15"/>检索</button>
      <button v-if="searched" class="p-button ghost" type="button" :disabled="busy" @click="query='';searched='';hits=[]">清除</button>
    </form>
    <section v-if="searched" class="p-panel kb-results" aria-live="polite">
      <h2>“{{searched}}”的匹配段落 <small>{{hits.length}} 条</small></h2>
      <p v-if="!hits.length">没有匹配段落。可尝试事件原文或业务资源名称。</p>
      <article v-for="(p,i) in hits" :key="i"><strong>{{p.title}}<template v-if="p.section"> / {{p.section}}</template></strong><p>{{p.text}}</p></article>
    </section>
    <div v-for="custom in [true,false]" :key="String(custom)" class="p-panel kb-group">
      <header><h2>{{custom?'工作空间资料':'内置排障手册'}}</h2><small>{{docs.filter(d=>d.id.startsWith('doc-')===custom).length}} 份</small></header>
      <p v-if="busy&&!docs.length" role="status">正在读取资料…</p>
      <p v-else-if="custom&&!error&&!docs.some(d=>d.id.startsWith('doc-'))">尚未导入资料。内置手册已可用于诊断。</p>
      <details v-for="doc in docs.filter(d=>d.id.startsWith('doc-')===custom)" :key="doc.id">
        <summary>{{doc.title}}</summary><pre>{{doc.content}}</pre>
        <a v-if="doc.source" :href="doc.source" target="_blank" rel="noopener noreferrer">查看 Kubernetes 原文 ↗</a>
        <template v-if="custom">
          <button v-if="removal!==doc.id" class="p-button ghost" :disabled="busy" @click="removal=doc.id">移除资料</button>
          <div v-else class="kb-actions"><small>移除后，历史诊断仍保留当时的参考段落。</small><button class="p-button" :disabled="busy" @click="remove(doc)">确认移除</button><button class="p-button ghost" @click="removal=''">取消</button></div>
        </template>
      </details>
    </div>
  </section>
</template>

<style scoped>
.kb {max-width:1050px}
.kb h1,.kb h2,.kb p {margin:0}
.kb h1 {font-size:22px;font-weight:550;line-height:1.4}
.kb h2 {font-size:15px;font-weight:550}
.kb .p-page-heading {display:flex;align-items:center;justify-content:space-between;gap:16px;margin:0 0 22px}
.kb .p-page-heading p {margin-top:5px;font-size:13px;color:var(--p-muted)}
.kb .p-button {display:inline-flex;align-items:center;justify-content:center;gap:7px;min-height:38px;padding:6px 12px;border:1px solid #595956;border-radius:3px;background:#242424;color:var(--p-text);font-size:13px;white-space:nowrap}
.kb .p-button:hover:not(:disabled) {background:#313130;border-color:#888882}
.kb .p-button.primary {background:#354361;border-color:#718bb8}
.kb .p-button.primary:hover:not(:disabled) {background:#425375;border-color:#9bb5e4}
.kb .p-button.ghost {background:transparent;border-color:var(--p-border)}
.kb input,.kb textarea {background:#181818;color:var(--p-text);border:1px solid var(--p-border);border-radius:2px;padding:8px 10px;min-height:38px}
.kb :focus-visible {outline:2px solid var(--p-accent);outline-offset:2px}
.kb button:disabled {opacity:.5;cursor:not-allowed}
.kb-search,.kb-actions,.kb-group header {display:flex;align-items:center;gap:10px}
.kb-search input {flex:1;min-width:0}
.kb .p-panel {margin:14px 0;padding:16px 20px;background:var(--p-panel);border:1px solid var(--p-border);border-radius:3px}
.kb .kb-import {display:grid;gap:12px}
.kb-import label {display:grid;gap:5px}
.kb-import textarea {resize:vertical;border:1px solid var(--p-border);background:var(--p-bg);color:var(--p-text);font:inherit;padding:8px}
.kb small,.kb .p-panel>p {color:var(--p-muted);font-size:13px}
.kb-group header,.kb-results h2 {margin-bottom:8px}
.kb-group details,.kb-results article {border-top:1px solid var(--p-border);padding:10px 0;overflow-wrap:anywhere}
.kb details summary {cursor:pointer;padding:2px 0}
.kb pre,.kb-results article p {white-space:pre-wrap;overflow-wrap:anywhere;font:inherit;color:var(--p-muted);margin:8px 0}
.kb a,.kb-note {color:var(--p-accent)}
.kb-error {color:#ffb4ac}
.kb-actions {flex-wrap:wrap}
@media(max-width:600px){.kb .p-panel{padding:12px}.kb-search{gap:5px}.kb .p-page-heading{align-items:flex-start;gap:10px}.kb .p-button{min-height:42px}.kb input[type=search]{min-height:42px}}
</style>
