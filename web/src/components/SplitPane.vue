<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'

const props = defineProps<{
  primaryId: string
  label: string
  initialRatio: number
  minLeft: number
  minRight: number
}>()

const container = ref<HTMLDivElement | null>(null)
const separator = ref<HTMLDivElement | null>(null)
const storageKey = `kubepilot-split-${props.primaryId}`
const ratio = ref(props.initialRatio)
const width = ref(0)
const drag = ref<{ id: number; offset: number; initialRatio: number } | null>(null)
let observer: ResizeObserver | undefined

try {
  const saved = Number(localStorage.getItem(storageKey))
  if (saved > 0 && saved < 1) ratio.value = saved
} catch { /* The pane remains adjustable when browser storage is unavailable. */ }

const minRatio = computed(() => width.value ? Math.min(1, props.minLeft / width.value) : 0)
const maxRatio = computed(() => width.value ? Math.max(minRatio.value, 1 - props.minRight / width.value) : 1)
const clamp = (value: number) => Math.min(maxRatio.value, Math.max(minRatio.value, value))
const leftPercent = computed(() => Math.round(clamp(ratio.value) * 100))

function measureWidth() {
  const gutter = separator.value?.offsetWidth ?? 0
  width.value = container.value && gutter ? container.value.clientWidth - gutter : 0
}

function save() {
  try { localStorage.setItem(storageKey, String(ratio.value)) } catch { /* In-memory preference remains usable. */ }
}

function reset() {
  ratio.value = props.initialRatio
  save()
}

function startDrag(event: PointerEvent) {
  if (event.button !== 0 || !event.isPrimary || drag.value) return
  measureWidth()
  if (!width.value || minRatio.value >= maxRatio.value) return
  const handle = event.currentTarget as HTMLElement
  handle.setPointerCapture(event.pointerId)
  drag.value = { id: event.pointerId, offset: event.clientX - handle.getBoundingClientRect().left, initialRatio: ratio.value }
  event.preventDefault()
}

function moveDrag(event: PointerEvent) {
  if (drag.value?.id !== event.pointerId || !container.value || !width.value) return
  const left = container.value.getBoundingClientRect().left
  ratio.value = clamp((event.clientX - left - drag.value.offset) / width.value)
}

function finishDrag(pointerId: number) {
  if (drag.value?.id !== pointerId) return
  if (ratio.value !== drag.value.initialRatio) save()
  drag.value = null
}

function resizeWithKeys(event: KeyboardEvent) {
  if (drag.value || event.altKey || event.ctrlKey || event.metaKey) return
  measureWidth()
  if (!width.value) return
  const step = 24 / width.value
  switch (event.key) {
    case 'ArrowLeft': ratio.value = clamp(clamp(ratio.value) - step); break
    case 'ArrowRight': ratio.value = clamp(clamp(ratio.value) + step); break
    case 'Home': ratio.value = minRatio.value; break
    case 'End': ratio.value = maxRatio.value; break
    case 'Enter': reset(); event.preventDefault(); return
    default: return
  }
  event.preventDefault()
  save()
}

onMounted(() => {
  const layout = container.value!
  const handle = separator.value!
  function measure() {
    measureWidth()
    if (!width.value && drag.value) {
      const { id } = drag.value
      if (handle.hasPointerCapture(id)) handle.releasePointerCapture(id)
      finishDrag(id)
    }
  }
  measure()
  observer = new ResizeObserver(measure)
  observer.observe(layout)
  observer.observe(handle)
})
onUnmounted(() => observer?.disconnect())
</script>

<template>
  <div
    ref="container"
    class="split-pane"
    :class="{ 'split-dragging': drag }"
    :style="{ '--split-ratio': ratio, '--split-min-left': minLeft + 'px', '--split-min-right': minRight + 'px' }"
  >
    <slot name="left" />
    <div
      ref="separator"
      class="split-separator"
      role="separator"
      aria-orientation="vertical"
      :aria-label="label"
      :aria-controls="primaryId"
      :aria-valuemin="Math.round(minRatio * 100)"
      :aria-valuemax="Math.round(maxRatio * 100)"
      :aria-valuenow="leftPercent"
      :aria-valuetext="`左栏 ${leftPercent}%，右栏 ${100 - leftPercent}%`"
      tabindex="0"
      title="拖动调整宽度，双击恢复默认"
      @pointerdown="startDrag"
      @pointermove="moveDrag"
      @pointerup="finishDrag($event.pointerId)"
      @pointercancel="finishDrag($event.pointerId)"
      @lostpointercapture="finishDrag($event.pointerId)"
      @keydown="resizeWithKeys"
      @dblclick="reset"
    ><span class="split-grip" aria-hidden="true"></span></div>
    <slot name="right" />
  </div>
</template>

<style scoped>
.split-pane {
  --split-available: calc(100% - var(--split-gutter));
  display: var(--split-display, grid);
  grid-template-columns:
    clamp(min(var(--split-min-left), var(--split-available)), calc(var(--split-available) * var(--split-ratio)), calc(var(--split-available) - var(--split-min-right)))
    var(--split-gutter) minmax(0, 1fr);
  min-width: 0;
}
.split-separator {
  position: relative;
  align-self: stretch;
  display: var(--split-handle-display, grid);
  justify-items: center;
  align-content: var(--split-grip-align, center);
  padding-top: var(--split-grip-inset, 0px);
  color: #a09f98;
  cursor: col-resize;
  touch-action: none;
  transition: background-color 150ms, color 150ms;
}
.split-separator::before {
  content: '';
  position: absolute;
  inset: 0 auto 0 calc(50% - 0.5px);
  width: 1px;
  background: var(--split-line, transparent);
}
.split-grip {
  position: relative;
  display: grid;
  place-content: center;
  width: 10px;
  height: 24px;
  border-radius: 5px;
  background: var(--p-bg);
}
.split-grip::before {
  content: '';
  width: 2px;
  height: 2px;
  border-radius: 50%;
  background: currentColor;
  box-shadow: 0 -4px currentColor, 0 4px currentColor;
}
.split-separator:hover, .split-separator:focus-visible, .split-dragging .split-separator {
  background: #92b0ff24;
  color: var(--p-accent);
}
.split-pane .split-separator:focus-visible { outline: none; }
.split-separator:focus-visible .split-grip { outline: 2px solid var(--p-accent); }
.split-dragging { cursor: col-resize; user-select: none; }
@media (prefers-reduced-motion: reduce) {
  .split-separator { transition: none; }
}
</style>
