<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { ArrowRight, Check, CircleAlert, Download, FlaskConical, Loader2, Play, Radio } from 'lucide-vue-next'
import { accountAPI } from './account'
const props=defineProps<{accountId:string;workspaceId:string}>()
const emit=defineEmits<{incident:[id:string]}>()
interface DemoStep {id:string;title:string;status:'pending'|'running'|'done';detail:string;at?:string}
interface DemoRun {id:string;status:'running'|'resolved'|'failed';startedAt:string;finishedAt?:string;incidentId?:string;error?:string;before?:string;after?:string;writeCount:number;steps:DemoStep[];modelRequests:{tool:string;at:string}[];payload?:unknown}
const state=ref<{ready:boolean;running:boolean;run:DemoRun|null}|null>(null)
const submitting=ref(false),error=ref(''),connectionError=ref(''),details=ref(false)
let timer:ReturnType<typeof setInterval>|undefined,disposed=false,fetching=false
const run=computed(()=>state.value?.run)
const active=computed(()=>submitting.value||state.value?.running)
const completed=computed(()=>run.value?.steps.filter(s=>s.status==='done').length||0)
const request=(route:string,body?:unknown)=>accountAPI('/demo'+route,body,{accountId:props.accountId,workspaceId:props.workspaceId})
async function refresh(){
 if(fetching||disposed)return;fetching=true
 try{const value=await request('/state');if(!disposed){state.value=value;connectionError.value=''}}catch(e){if(!disposed)connectionError.value=e instanceof Error?e.message:'模拟状态暂时不可用'}finally{fetching=false}
}
async function start(){
 if(active.value)return;submitting.value=true;error.value='';details.value=false
 try{await request('/run',{});await refresh()}catch(e){error.value=e instanceof Error?e.message:'无法开始模拟'}finally{submitting.value=false}
}
async function download(){
 try{const report=await request('/report');const u=URL.createObjectURL(new Blob([JSON.stringify(report,null,2)],{type:'application/json'}));const a=document.createElement('a');a.href=u;a.download=`kubepilot-simulation-${report.id}.json`;a.click();setTimeout(()=>URL.revokeObjectURL(u),1000)}catch(e){error.value=e instanceof Error?e.message:'导出失败'}
}
function time(value?:string){return value?new Date(value).toLocaleTimeString('zh-CN',{hour12:false}):'—'}
watch(()=>run.value?.incidentId,id=>{if(id)emit('incident',id)})
onMounted(()=>{void refresh();timer=setInterval(refresh,1000)})
onUnmounted(()=>{disposed=true;clearInterval(timer)})
</script>

<template>
 <section class="sim-panel" aria-labelledby="simulation-title">
  <div class="sim-heading"><div><div class="sim-kicker"><FlaskConical :size="14"/>隔离模拟环境</div><h2 id="simulation-title">告警到修复 · 全链路演示</h2><p>Service 选择器错误 → 无就绪端点 → 恢复健康基线。</p></div><button class="sim-primary" :disabled="!state?.ready||active||run?.status==='failed'" @click="start"><Loader2 v-if="active" class="spin" :size="16"/><Play v-else :size="15"/>{{active?'正在执行模拟':run?.status==='resolved'?'再次注入并发送告警':'注入故障并发送告警'}}</button></div>
  <div class="sim-boundary"><span><Radio :size="14"/>Alertmanager v4 通知格式</span><span>集群：模拟</span><span>模型：模拟响应</span><span>范围：demo-lab / payment-service</span></div>
  <p v-if="error||connectionError" class="sim-error" role="alert"><CircleAlert :size="16"/>{{error||connectionError}}</p>
  <template v-if="run">
   <div class="sim-progress" aria-live="polite"><span><i :class="run.status"/>{{run.status==='resolved'?'全链路验证通过':run.status==='failed'?'流程已停止':'处理中'}}<small>{{completed}} / 8</small></span><span>开始 {{time(run.startedAt)}}<template v-if="run.finishedAt"> · 结束 {{time(run.finishedAt)}}</template></span></div>
   <ol class="sim-steps"><li v-for="(step,index) in run.steps" :key="step.id" :class="step.status"><div class="sim-step-head"><span class="sim-number"><Check v-if="step.status==='done'" :size="14"/><Loader2 v-else-if="step.status==='running'" class="spin" :size="14"/><template v-else>{{String(index+1).padStart(2,'0')}}</template></span><strong>{{step.title}}</strong><time>{{time(step.at)}}</time></div><p>{{step.detail}}</p></li></ol>
   <p v-if="run.error" class="sim-error" role="alert"><CircleAlert :size="16"/>{{run.error}}。本次记录已保留，可导出检查。</p>
   <div v-if="run.before" class="sim-diff"><div><span>修复前</span><code>{{run.before}}</code></div><ArrowRight :size="17"/><div><span>{{run.status==='resolved'?'修复后已验证':'计划恢复为'}}</span><code>{{run.after}}</code></div></div>
   <div class="sim-footer"><p>{{run.status==='resolved'?`实际执行 ${run.writeCount} 次模拟写入；已完成连续两次恢复检查及告警恢复通知。`:'每个事件最多尝试一次写操作；复核和权限检查未通过时停止执行。'}}</p><div><button :aria-expanded="details" @click="details=!details">{{details?'收起告警报文':'查看告警报文'}}</button><button :disabled="active" @click="download"><Download :size="14"/>导出本次记录</button></div></div>
   <pre v-if="details" tabindex="0" class="sim-payload">{{JSON.stringify(run.payload||{},null,2)}}</pre>
  </template>
  <div v-else class="sim-empty"><span>01 注入故障</span><ArrowRight :size="13"/><span>02 接收与采集</span><ArrowRight :size="13"/><span>03 诊断与复核</span><ArrowRight :size="13"/><span>04 修复与验证</span></div>
  <p class="sim-disclaimer">通过真实平台和 Connector 的 HTTP 接口运行；未连接真实 Prometheus、Alertmanager、Kubernetes 或大模型服务。过程证据与修复计划可在下方故障详情中核对。</p>
 </section>
</template>

<style scoped>
.sim-panel{border:1px solid #53504f;background:#1e1e1e;margin-bottom:24px;color:#e4e4de;overflow:hidden}
.sim-heading{display:flex;justify-content:space-between;align-items:center;gap:24px;padding:22px 24px}
.sim-kicker{display:flex;align-items:center;gap:6px;color:#cdb8cf;font-size:11px;margin-bottom:7px}.sim-heading h2{font-size:18px;font-weight:500}.sim-heading p{font-size:12px;color:#b1b1a9;margin-top:6px}
.sim-primary{display:inline-flex;justify-content:center;align-items:center;gap:8px;min-height:40px;padding:9px 16px;white-space:nowrap;background:#92b0ff;border:1px solid #a8bfff;color:#17213a;border-radius:4px;font-size:13px;font-weight:600;transition:background 160ms}.sim-primary:hover:not(:disabled){background:#b2c6fc}
.sim-boundary{display:flex;align-items:center;gap:18px;flex-wrap:wrap;border-top:1px solid #41413c;border-bottom:1px solid #41413c;background:#242323;padding:10px 24px;font-size:11px;color:#bfb7bf}.sim-boundary span{display:inline-flex;align-items:center;gap:6px}
.sim-progress{display:flex;align-items:center;justify-content:space-between;gap:12px;padding:18px 24px;font-size:12px}.sim-progress>span:first-child{display:flex;align-items:center;gap:8px}.sim-progress small{color:#a9aaa3;font:11px Consolas,monospace;margin-left:4px}.sim-progress>span:last-child{color:#a8a8a0;font-size:11px}.sim-progress i{height:6px;width:6px;background:#92b0ff;border-radius:50%}.sim-progress i.resolved{background:#b0ceaf}.sim-progress i.failed{background:#e7a49a}
.sim-steps{list-style:none;padding:0;margin:0 24px 20px;border:1px solid #41413c;display:grid;grid-template-columns:repeat(4,minmax(0,1fr))}.sim-steps li{padding:15px 13px;min-height:112px;border-right:1px solid #41413c;border-bottom:1px solid #41413c}.sim-steps li:nth-child(4n){border-right:0}.sim-steps li:nth-last-child(-n+4){border-bottom:0}
.sim-step-head{display:flex;align-items:center;gap:7px}.sim-number{width:21px;height:21px;display:grid;place-items:center;border:1px solid #54544e;font:10px Consolas,monospace;color:#a5a59e}.sim-step-head strong{font-size:12px;font-weight:500;color:#aaa9a2}.sim-step-head time{font:10px Consolas,monospace;color:#96968d;margin-left:auto}.sim-steps p{font-size:11px;line-height:1.7;color:#96988f;margin-top:10px}.sim-steps .done .sim-number{border-color:#55664d;color:#b1ccaa;background:#2b3328}.sim-steps .done strong{color:#dbdfd7}.sim-steps .done p{color:#b6b7ad}.sim-steps .running{background:#252d3d}.sim-steps .running .sim-number{color:#abc3ff;border-color:#7487ac}.sim-steps .running strong{color:#dce5fc}.sim-steps .running p{color:#bbc5da}
.sim-diff{display:grid;grid-template-columns:1fr auto 1fr;align-items:center;gap:18px;margin:0 24px 20px;padding:16px;background:#171717;border:1px solid #41413c}.sim-diff span{display:block;font-size:11px;color:#a9a9a0;margin-bottom:5px}.sim-diff code{font:12px/1.6 Consolas,monospace;color:#d7d9d0;overflow-wrap:anywhere}.sim-diff>svg{color:#96988f}
.sim-footer{display:flex;justify-content:space-between;align-items:center;gap:15px;margin:0 24px 18px}.sim-footer p{font-size:11px;color:#b6b7ad;max-width:550px}.sim-footer>div{display:flex;gap:16px;flex-shrink:0}.sim-footer button{font-size:11px;color:#b2c6f9;display:flex;gap:5px;align-items:center;padding:6px 0;min-height:32px}
.sim-disclaimer{padding:13px 24px;border-top:1px solid #41413c;color:#a7a49f;font-size:11px;line-height:1.75}.sim-empty{display:flex;gap:16px;align-items:center;flex-wrap:wrap;padding:23px 24px;font-size:12px;color:#c1c1b9}.sim-empty svg{color:#7f8278}.sim-error{display:flex;gap:8px;align-items:flex-start;margin:14px 24px;padding:10px 12px;background:#352826;color:#f0b8af;border:1px solid #6e4943;font-size:12px}.sim-error svg{flex-shrink:0;margin-top:2px}.sim-payload{margin:0 24px 20px;max-height:300px;overflow:auto;background:#141414;border:1px solid #444;padding:14px;font:11px/1.7 Consolas,monospace;color:#bdc5b9}
@media(max-width:1250px){.sim-steps{grid-template-columns:repeat(2,minmax(0,1fr))}.sim-steps li:nth-child(2n){border-right:0}.sim-steps li:nth-last-child(-n+4){border-bottom:1px solid #41413c}.sim-steps li:nth-last-child(-n+2){border-bottom:0}.sim-footer{align-items:flex-start;flex-direction:column;gap:8px}}
@media(max-width:640px){.sim-heading{padding:17px 15px;flex-direction:column;align-items:flex-start;gap:15px}.sim-heading h2{font-size:16px}.sim-primary{width:100%;min-height:44px}.sim-boundary{padding:10px 15px;gap:7px 14px}.sim-boundary span:last-child{width:100%}.sim-progress{padding:15px;flex-wrap:wrap;gap:8px}.sim-steps{margin:0 15px 15px;grid-template-columns:1fr}.sim-steps li{border-right:0;min-height:85px;padding:13px}.sim-steps li:nth-last-child(-n+2){border-bottom:1px solid #41413c}.sim-steps li:last-child{border-bottom:0}.sim-diff{grid-template-columns:1fr;margin:0 15px 15px;gap:12px}.sim-diff>svg{transform:rotate(90deg)}.sim-footer{margin:0 15px 15px}.sim-footer>div{width:100%;justify-content:space-between}.sim-footer button{min-height:40px}.sim-disclaimer{padding:12px 15px}.sim-error{margin:12px 15px}.sim-empty{padding:18px 15px;gap:10px;font-size:11px}.sim-empty svg{display:none}.sim-empty span{width:calc(50% - 5px)}.sim-payload{margin:0 15px 15px}}
@media(prefers-reduced-motion:reduce){*{animation:none!important;transition:none!important}}
</style>
