<script setup lang="ts">
import type { Passage } from '../types'
defineProps<{items?: Passage[]}>()
</script>

<template>
  <details v-if="items?.length" class="kb-references">
    <summary>本轮送入模型的参考段落 · {{ items.length }}</summary>
    <article v-for="(p,i) in items" :key="i">
      <strong>{{ p.title }}<template v-if="p.section"> / {{ p.section }}</template></strong>
      <small>{{ p.documentId.startsWith('doc-') ? '工作空间资料' : '内置手册' }} · {{ p.version }}</small>
      <p>{{ p.text }}</p>
    </article>
  </details>
</template>

<style scoped>
.kb-references { margin: 10px 0; font-size: 13px; }
summary { color: var(--p-accent); cursor: pointer; }
article { padding: 10px 0; border-bottom: 1px solid var(--p-border); overflow-wrap: anywhere; }
small { display: block; color: var(--p-muted); }
p { white-space: pre-wrap; }
</style>
