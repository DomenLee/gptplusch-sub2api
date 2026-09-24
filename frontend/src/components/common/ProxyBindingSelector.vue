<template>
  <div class="space-y-3">
    <select v-model="bindingMode" class="input" :disabled="disabled">
      <option value="none">{{ t('admin.proxyGroups.binding.none') }}</option>
      <option value="proxy">{{ t('admin.proxyGroups.binding.proxy') }}</option>
      <option value="group" :disabled="proxyGroups.length === 0">
        {{ t('admin.proxyGroups.binding.group') }}
      </option>
    </select>

    <select v-if="bindingMode === 'proxy'" v-model="selectedProxyId" class="input" :disabled="disabled">
      <option :value="null">{{ t('admin.proxyGroups.binding.chooseProxy') }}</option>
      <option v-for="proxy in proxies" :key="proxy.id" :value="proxy.id">
        {{ proxy.name }} ({{ proxy.protocol }}://{{ proxy.host }}:{{ proxy.port }})
      </option>
    </select>

    <select v-if="bindingMode === 'group'" v-model="selectedGroupId" class="input" :disabled="disabled">
      <option :value="null">{{ t('admin.proxyGroups.binding.chooseGroup') }}</option>
      <option v-for="group in proxyGroups" :key="group.id" :value="group.id">
        {{ group.name }} · {{ group.available_member_count }}/{{ group.member_count }}
      </option>
    </select>

    <p v-if="bindingMode === 'group' && selectedGroup" class="input-hint">
      {{ t('admin.proxyGroups.binding.groupHint', {
        available: selectedGroup.available_member_count,
        total: selectedGroup.member_count
      }) }}
    </p>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { Proxy, ProxyGroup } from '@/types'

const { t } = useI18n()

interface Props {
  proxyId: number | null
  proxyGroupId: number | null
  proxies: Proxy[]
  proxyGroups?: ProxyGroup[]
  disabled?: boolean
}

const props = withDefaults(defineProps<Props>(), {
  proxyGroups: () => [],
  disabled: false
})

const emit = defineEmits<{
  'update:proxyId': [value: number | null]
  'update:proxyGroupId': [value: number | null]
}>()

type BindingMode = 'none' | 'proxy' | 'group'

const bindingMode = computed<BindingMode>({
  get: () => {
    if (props.proxyGroupId !== null) return 'group'
    if (props.proxyId !== null) return 'proxy'
    return 'none'
  },
  set: (mode) => {
    if (mode === 'proxy') {
      emit('update:proxyGroupId', null)
      return
    }
    if (mode === 'group') {
      emit('update:proxyId', null)
      return
    }
    emit('update:proxyId', null)
    emit('update:proxyGroupId', null)
  }
})

const selectedProxyId = computed({
  get: () => props.proxyId,
  set: (value: number | null) => {
    emit('update:proxyId', value)
    if (value !== null) emit('update:proxyGroupId', null)
  }
})

const selectedGroupId = computed({
  get: () => props.proxyGroupId,
  set: (value: number | null) => {
    emit('update:proxyGroupId', value)
    if (value !== null) emit('update:proxyId', null)
  }
})

const selectedGroup = computed(() => props.proxyGroups.find((group) => group.id === props.proxyGroupId))
</script>
