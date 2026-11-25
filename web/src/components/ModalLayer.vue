<script setup lang="ts">
import { nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { registerDialog } from '../composables/dialogStack'
const props = withDefaults(defineProps<{ open: boolean; titleId: string; kind?: 'dialog' | 'drawer' | 'sheet' | 'command'; dismissable?: boolean }>(), { kind: 'dialog', dismissable: true })
const emit = defineEmits<{ close: [] }>()
const surface = ref<HTMLElement>()
let release: (() => void) | undefined
let generation = 0
function close() { if (props.dismissable) emit('close') }
watch(() => props.open, async value => {
  const current = ++generation
  release?.(); release = undefined
  if (value) {
    await nextTick()
    if (props.open && current === generation && surface.value) release = registerDialog(surface.value, close, () => props.dismissable)
  }
}, { immediate: true })
onBeforeUnmount(() => { generation++; release?.() })
</script>

<template>
  <Teleport to="body">
    <Transition name="overlay">
      <div v-if="open" class="modal-overlay" :class="'overlay-' + kind" @click.self="close">
        <section ref="surface" class="modal-surface" :class="kind" role="dialog" aria-modal="true" :aria-labelledby="titleId" tabindex="-1">
          <slot />
        </section>
      </div>
    </Transition>
  </Teleport>
</template>
