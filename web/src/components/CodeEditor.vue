<script setup lang="ts">
import { computed, ref } from 'vue'
const props = defineProps<{ modelValue: string; label: string; disabled?: boolean }>()
const emit = defineEmits<{ 'update:modelValue': [value: string] }>()
const field = ref<HTMLTextAreaElement>()
const lineCount = computed(() => Math.max(1, props.modelValue.split('\n').length))
const scrollTop = ref(0)
function input(event: Event) { emit('update:modelValue', (event.target as HTMLTextAreaElement).value) }
function insertIndent(event: KeyboardEvent) {
  if (!(event.ctrlKey || event.metaKey) || event.key !== ']') return
  event.preventDefault()
  const element = field.value!
  const start = element.selectionStart
  element.setRangeText('  ', start, element.selectionEnd, 'end')
  emit('update:modelValue', element.value)
}
</script>
<template>
  <div class="code-editor" :style="{ '--code-lines': lineCount }">
    <div class="code-gutter" aria-hidden="true"><div :style="{ transform: 'translateY(-' + scrollTop + 'px)' }"><span v-for="line in lineCount" :key="line">{{ line }}</span></div></div>
    <textarea ref="field" :value="modelValue" :aria-label="label" :disabled="disabled" spellcheck="false" autocapitalize="off" autocomplete="off" wrap="off" @input="input" @keydown="insertIndent" @scroll="scrollTop = ($event.target as HTMLTextAreaElement).scrollTop"></textarea>
  </div>
</template>
