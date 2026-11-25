<script setup lang="ts">
import { nextTick, onMounted, onUnmounted, ref, watch } from 'vue'
const props = withDefaults(defineProps<{ modelValue: string; options: { id: string; label: string; count?: number }[]; label: string; tabs?: boolean }>(), { tabs: false })
const emit = defineEmits<{ 'update:modelValue': [value: string] }>()
const root = ref<HTMLElement>()
const indicator = ref({ width: '0px', transform: 'translateX(0px)' })
let observer: ResizeObserver | undefined
function measure() {
  const button = root.value?.querySelector<HTMLElement>('[data-value="' + props.modelValue + '"]')
  if (button) indicator.value = { width: button.offsetWidth + 'px', transform: 'translateX(' + button.offsetLeft + 'px)' }
}
function navigate(event: KeyboardEvent, index: number) {
  let next = index
  if (event.key === 'ArrowRight') next = (index + 1) % props.options.length
  else if (event.key === 'ArrowLeft') next = (index - 1 + props.options.length) % props.options.length
  else if (event.key === 'Home') next = 0
  else if (event.key === 'End') next = props.options.length - 1
  else return
  event.preventDefault()
  const option = props.options[next]
  if (!option) return
  emit('update:modelValue', option.id)
  root.value?.querySelector<HTMLButtonElement>('[data-value="' + option.id + '"]')?.focus()
}
watch(() => [props.modelValue, props.options], async () => { await nextTick(); measure() }, { deep: true })
onMounted(() => { measure(); observer = new ResizeObserver(measure); if (root.value) observer.observe(root.value) })
onUnmounted(() => observer?.disconnect())
</script>

<template>
  <div class="segments-scroll">
    <div ref="root" class="segments" :role="tabs ? 'tablist' : 'group'" :aria-label="label">
      <span class="segment-indicator" :style="indicator" aria-hidden="true"></span>
      <button v-for="(option, index) in options" :id="tabs ? 'tab-' + option.id : undefined" :key="option.id" :data-value="option.id"
        :class="{ selected: modelValue === option.id }" :role="tabs ? 'tab' : undefined"
        :aria-selected="tabs ? modelValue === option.id : undefined" :aria-pressed="tabs ? undefined : modelValue === option.id"
        :aria-controls="tabs ? 'panel-' + option.id : undefined" :tabindex="tabs && modelValue !== option.id ? -1 : 0"
        @click="emit('update:modelValue', option.id)" @keydown="navigate($event, index)">
        {{ option.label }}<span v-if="option.count !== undefined" class="segment-count">{{ option.count }}</span>
      </button>
    </div>
  </div>
</template>
