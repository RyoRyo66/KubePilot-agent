<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import type { Inspection } from '../types'
const props = defineProps<{ runs: Inspection[] }>()
const root = ref<HTMLElement>()
const width = ref(500)
const height = ref(200)
const selected = ref<number | null>(null)
let observer: ResizeObserver | undefined
const max = computed(() => Math.max(4, Math.ceil(Math.max(0, ...props.runs.map(run => run.findings)) / 4) * 4))
const points = computed(() => props.runs.map((run, index) => ({
  x: props.runs.length === 1 ? width.value / 2 : 32 + index * (width.value - 68) / (props.runs.length - 1),
  y: height.value - 36 - run.findings / max.value * (height.value - 66),
  value: run.findings, at: run.startedAt, id: run.id,
})))
const line = computed(() => points.value.map((p, index) => (index ? 'L' : 'M') + p.x + ',' + p.y).join(' '))
const area = computed(() => points.value.length > 1 ? line.value + ' L' + points.value.at(-1)!.x + ',' + (height.value - 36) + ' L' + points.value[0]!.x + ',' + (height.value - 36) + ' Z' : '')
const current = computed(() => selected.value === null ? null : points.value[selected.value])
const time = (value: string) => new Date(value).toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit', second: '2-digit' })
function move(event: KeyboardEvent) {
  if (!points.value.length || !['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) return
  event.preventDefault()
  if (event.key === 'Home') selected.value = 0
  else if (event.key === 'End') selected.value = points.value.length - 1
  else selected.value = Math.max(0, Math.min(points.value.length - 1, (selected.value ?? points.value.length - 1) + (event.key === 'ArrowLeft' ? -1 : 1)))
}
watch(() => props.runs.length, () => { selected.value = null })
onMounted(() => {
  observer = new ResizeObserver(entries => {
    width.value = Math.max(240, Math.round(entries[0]!.contentRect.width))
    height.value = Math.round(entries[0]!.contentRect.height)
  })
  if (root.value) observer.observe(root.value)
})
onUnmounted(() => observer?.disconnect())
</script>

<template>
  <div ref="root" class="trend-chart" tabindex="0" role="group" aria-label="异常数量趋势，使用左右方向键查看每次巡检" @keydown="move" @blur="selected = null" @mouseleave="selected = null">
    <svg :viewBox="'0 0 ' + width + ' ' + height" role="img" :aria-label="'最近 ' + runs.length + ' 次成功巡检的异常数量'">
      <defs><linearGradient id="trend-fill" x1="0" y1="0" x2="0" y2="1"><stop offset="0%" stop-color="#0071e3" stop-opacity=".16" /><stop offset="100%" stop-color="#0071e3" stop-opacity=".015" /></linearGradient></defs>
      <g v-for="tick in 5" :key="tick">
        <line x1="32" :x2="width - 36" :y1="height - 36 - (tick - 1) * (height - 66) / 4" :y2="height - 36 - (tick - 1) * (height - 66) / 4" stroke="var(--line)" stroke-dasharray="3 5" />
        <text x="12" :y="height - 32 - (tick - 1) * (height - 66) / 4" class="chart-label" text-anchor="middle">{{ (tick - 1) * max / 4 }}</text>
      </g>
      <path v-if="area" :d="area" fill="url(#trend-fill)" />
      <path :d="line" fill="none" stroke="var(--accent)" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round" />
      <g v-for="(point, index) in points" :key="point.id" @mouseenter="selected = index" @click="selected = index">
        <circle :cx="point.x" :cy="point.y" r="15" fill="transparent" />
        <circle :cx="point.x" :cy="point.y" :r="selected === index ? 5 : 3.5" fill="white" stroke="var(--accent)" stroke-width="2" />
        <text v-if="index === 0 || index === points.length - 1 || (width > 400 && index === Math.floor(points.length / 2))" :x="point.x" :y="height - 11" text-anchor="middle" class="chart-label">{{ time(point.at) }}</text>
      </g>
      <text v-if="!points.length" :x="width / 2" :y="height / 2" text-anchor="middle" class="chart-label">运行巡检后生成趋势</text>
    </svg>
    <div v-if="current" class="chart-tooltip" :style="{ left: Math.max(88, Math.min(width - 88, current.x)) + 'px' }">
      {{ time(current.at) }}<strong>{{ current.value }} 项异常</strong>
    </div>
    <span class="sr-only" aria-live="polite">{{ current ? time(current.at) + '，' + current.value + ' 项异常' : '' }}</span>
  </div>
</template>
