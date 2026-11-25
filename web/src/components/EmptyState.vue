<script setup lang="ts">
import { Inbox } from 'lucide-vue-next'
defineProps<{ title: string; description?: string }>()
</script>
<template>
  <div class="empty-state">
    <span class="empty-icon"><slot name="icon"><Inbox :size="24" :stroke-width="1.5" /></slot></span>
    <h3>{{ title }}</h3>
    <p v-if="description">{{ description }}</p>
    <slot />
  </div>
</template>
