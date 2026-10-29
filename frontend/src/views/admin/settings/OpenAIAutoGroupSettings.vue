<template>
  <div class="card" data-testid="openai-auto-group-settings">
    <div class="border-b border-gray-100 px-6 py-4 dark:border-dark-700">
      <h2 class="text-lg font-semibold text-gray-900 dark:text-white">{{ t('admin.settings.openaiAutoGroup.title') }}</h2>
      <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">{{ t('admin.settings.openaiAutoGroup.description') }}</p>
    </div>
    <div class="space-y-5 p-6">
      <div v-if="error" role="alert" class="rounded-lg border border-red-300 bg-red-50 p-3 text-sm text-red-700 dark:border-red-800 dark:bg-red-950 dark:text-red-300">
        {{ error }}
        <button v-if="!loaded" type="button" class="ml-2 underline" @click="load">{{ t('admin.settings.openaiAutoGroup.retry') }}</button>
      </div>
      <div v-if="groupsError" role="alert" class="rounded-lg border border-red-300 bg-red-50 p-3 text-sm text-red-700 dark:border-red-800 dark:bg-red-950 dark:text-red-300">
        {{ groupsError }}
        <button type="button" class="ml-2 underline" :disabled="groupsLoading" @click="loadGroups">{{ t('admin.settings.openaiAutoGroup.retry') }}</button>
      </div>
      <p v-if="loading" class="text-sm text-gray-500">{{ t('common.loading') }}</p>
      <template v-else-if="loaded">
        <div data-testid="auto-group-skip-groups">
          <label for="auto-group-skip-group" class="input-label">{{ t('admin.settings.openaiAutoGroup.skipGroups') }}</label>
          <p class="mb-2 text-sm text-gray-500 dark:text-gray-400">{{ t('admin.settings.openaiAutoGroup.skipGroupsHint') }}</p>
          <Select
            id="auto-group-skip-group"
            :aria-label="t('admin.settings.openaiAutoGroup.skipGroups')"
            :model-value="null"
            :options="skipOptions"
            :placeholder="t('admin.settings.openaiAutoGroup.skipGroupsPlaceholder')"
            :disabled="saving || groupsLoading || !!groupsError"
            searchable
            @update:model-value="addSkipGroup"
          />
          <ul v-if="form.skip_group_ids.length" class="mt-2 flex flex-wrap gap-2">
            <li v-for="id in form.skip_group_ids" :key="id" class="flex items-center gap-2 rounded border border-gray-200 px-2 py-1 text-sm dark:border-dark-600" :data-group-id="id">
              <span>{{ skipGroupLabel(id) }}</span>
              <button type="button" class="text-primary-600 underline" :disabled="saving" @click="form.skip_group_ids = form.skip_group_ids.filter(value => value !== id)">{{ t('admin.settings.openaiAutoGroup.removeSkipGroup') }}</button>
            </li>
          </ul>
        </div>
        <div class="flex items-center justify-between gap-4">
          <div>
            <label class="font-medium text-gray-900 dark:text-white">{{ t('admin.settings.openaiAutoGroup.defaultHighRate') }}</label>
            <p class="text-sm text-gray-500 dark:text-gray-400">{{ t('admin.settings.openaiAutoGroup.defaultHighRateHint') }}</p>
          </div>
          <Toggle v-model="form.default_high_rate" :disabled="saving" data-testid="auto-group-default-high" />
        </div>
        <div class="flex items-center justify-between gap-4">
          <div>
            <label class="font-medium text-gray-900 dark:text-white">{{ t('admin.settings.openaiAutoGroup.enabled') }}</label>
            <p class="text-sm text-gray-500 dark:text-gray-400">{{ t('admin.settings.openaiAutoGroup.enabledHint') }}</p>
          </div>
          <Toggle v-model="form.enabled" :disabled="saving" data-testid="auto-group-enabled" />
        </div>
        <div class="grid gap-4 sm:grid-cols-2">
          <div v-for="field in failureFields" :key="field.key">
            <label :for="field.id" class="input-label">{{ t(`admin.settings.openaiAutoGroup.${field.label}`) }}</label>
            <input :id="field.id" v-model.number="form[field.key]" class="input w-32" type="number" :min="field.min" :max="field.max" step="1" :disabled="saving" />
          </div>
        </div>
        <template v-for="section in sections" :key="section.key">
          <div class="flex items-center justify-between gap-4 border-t border-gray-100 pt-4 dark:border-dark-700">
            <div>
              <label class="font-medium">{{ t(`admin.settings.openaiAutoGroup.${section.key}`) }}</label>
              <p class="text-sm text-gray-500">{{ t(`admin.settings.openaiAutoGroup.${section.key}Hint`) }}</p>
            </div>
            <Toggle v-model="form[section.toggle]" :disabled="saving" :data-testid="`auto-group-${section.key}`" />
          </div>
          <div class="grid gap-4 sm:grid-cols-2">
            <div v-for="field in section.fields" :key="field.key">
              <label :for="field.id" class="input-label">{{ t(`admin.settings.openaiAutoGroup.${field.label}`) }}</label>
              <input :id="field.id" v-model.number="form[field.key]" class="input w-32" type="number" :min="field.min" :max="field.max" step="1" :disabled="saving" />
            </div>
          </div>
        </template>
        <div class="border-t border-gray-100 pt-4 dark:border-dark-700">
          <label for="auto-group-protection" class="input-label">{{ t('admin.settings.openaiAutoGroup.protectionMinutes') }}</label>
          <input id="auto-group-protection" v-model.number="form.protection_minutes" class="input w-32" type="number" min="0" max="1440" step="1" :disabled="saving" />
          <p class="mt-2 text-sm text-gray-500">{{ t('admin.settings.openaiAutoGroup.protectionHint') }}</p>
        </div>
        <div class="flex items-center justify-between gap-4">
          <div>
            <label class="font-medium">{{ t('admin.settings.openaiAutoGroup.observeOnly') }}</label>
            <p class="text-sm text-gray-500">{{ t('admin.settings.openaiAutoGroup.observeOnlyHint') }}</p>
          </div>
          <Toggle v-model="form.observe_only" :disabled="saving" data-testid="auto-group-observe" />
        </div>
        <p class="rounded-lg bg-gray-50 p-3 text-sm text-gray-600 dark:bg-dark-800 dark:text-gray-300">{{ t('admin.settings.openaiAutoGroup.rules') }}</p>
        <p v-if="saved" role="status" class="text-sm text-green-700 dark:text-green-400">{{ t('admin.settings.openaiAutoGroup.saved') }}</p>
        <div class="flex justify-end border-t border-gray-100 pt-4 dark:border-dark-700">
          <button type="button" class="btn btn-primary btn-sm" :disabled="saving" @click="save">{{ saving ? t('common.saving') : t('common.save') }}</button>
        </div>
      </template>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import type { OpenAIAutoGroupSettings } from '@/api/admin/settings'
import Toggle from '@/components/common/Toggle.vue'
import Select from '@/components/common/Select.vue'
import type { AdminGroup } from '@/types'
import { extractApiErrorMessage } from '@/utils/apiError'

const { t } = useI18n()
const form = reactive<OpenAIAutoGroupSettings>({
  skip_group_ids: [], default_high_rate: false, enabled: false, failure_window: 20, failure_threshold: 3,
  latency_enabled: false, latency_window: 10, slow_threshold: 3, first_token_seconds: 10,
  recovery_enabled: false, recovery_samples: 30, recovery_fast_percent: 90,
  recovery_median_seconds: 8, recovery_fast_streak: 10, protection_minutes: 30, observe_only: false,
})
type NumericKey = { [K in keyof OpenAIAutoGroupSettings]-?: OpenAIAutoGroupSettings[K] extends number ? K : never }[keyof OpenAIAutoGroupSettings]
type Field = { key: NumericKey; id: string; label: string; min: number; max: number }
const failureFields: Field[] = [
  { key: 'failure_window', id: 'auto-group-window', label: 'failureWindow', min: 1, max: 1000 },
  { key: 'failure_threshold', id: 'auto-group-threshold', label: 'failureThreshold', min: 1, max: 1000 },
]
const sections: { key: string; toggle: 'latency_enabled' | 'recovery_enabled'; fields: Field[] }[] = [
  { key: 'latencyEnabled', toggle: 'latency_enabled', fields: [
    { key: 'latency_window', id: 'auto-group-latency-window', label: 'latencyWindow', min: 1, max: 1000 },
    { key: 'slow_threshold', id: 'auto-group-slow-threshold', label: 'slowThreshold', min: 1, max: 1000 },
    { key: 'first_token_seconds', id: 'auto-group-first-token', label: 'firstTokenSeconds', min: 1, max: 300 },
  ] },
  { key: 'recoveryEnabled', toggle: 'recovery_enabled', fields: [
    { key: 'recovery_samples', id: 'auto-group-recovery-samples', label: 'recoverySamples', min: 1, max: 1000 },
    { key: 'recovery_fast_percent', id: 'auto-group-fast-percent', label: 'recoveryFastPercent', min: 1, max: 100 },
    { key: 'recovery_median_seconds', id: 'auto-group-median', label: 'recoveryMedianSeconds', min: 1, max: 300 },
    { key: 'recovery_fast_streak', id: 'auto-group-fast-streak', label: 'recoveryFastStreak', min: 1, max: 1000 },
  ] },
]
const loading = ref(true)
const loaded = ref(false)
const saving = ref(false)
const saved = ref(false)
const error = ref('')

const groups = ref<AdminGroup[]>([])
const groupsLoading = ref(false)
const groupsError = ref('')
const skipOptions = computed(() => groups.value
  .filter(group => group.platform === 'openai' && !group.auto_route_enabled && !form.skip_group_ids.includes(group.id))
  .map(group => ({ value: group.id, label: skipGroupLabel(group.id) })))

function skipGroupLabel(id: number) {
  const group = groups.value.find(item => item.id === id)
  if (!group) return t('admin.settings.openaiAutoGroup.unavailableSkipGroup', { id })
  const inactive = group.status === 'active' ? '' : ` · ${t('admin.settings.openaiAutoGroup.inactiveGroup')}`
  return `${group.name} (#${id}, ${group.rate_multiplier}x)${inactive}`
}

function addSkipGroup(value: string | number | boolean | null) {
  if (saving.value || typeof value !== 'number' || !Number.isSafeInteger(value) || value <= 0 || form.skip_group_ids.includes(value)) return
  form.skip_group_ids = [...form.skip_group_ids, value]
}

async function loadGroups() {
  groupsLoading.value = true
  groupsError.value = ''
  try {
    groups.value = await adminAPI.groups.getAllIncludingInactive()
  } catch (cause) {
    groupsError.value = `${t('admin.settings.openaiAutoGroup.skipGroupsLoadFailed')} ${extractApiErrorMessage(cause, t('admin.settings.openaiAutoGroup.retryHint'))}`
  } finally { groupsLoading.value = false }
}

async function load() {
  loading.value = true
  error.value = ''
  try {
    Object.assign(form, await adminAPI.settings.getOpenAIAutoGroupSettings())
    form.skip_group_ids = form.skip_group_ids ?? []
    loaded.value = true
  } catch (cause) {
    error.value = `${t('admin.settings.openaiAutoGroup.loadFailed')} ${extractApiErrorMessage(cause, t('admin.settings.openaiAutoGroup.retryHint'))}`
  } finally {
    loading.value = false
  }
}

async function save() {
  error.value = ''
  saved.value = false
  const fields = [...failureFields, ...sections.flatMap(section => section.fields)]
  if (fields.some(field => !Number.isInteger(form[field.key]) || form[field.key] < field.min || form[field.key] > field.max)
    || form.failure_threshold > form.failure_window || form.slow_threshold > form.latency_window
    || form.recovery_samples < form.latency_window || form.recovery_fast_streak > form.recovery_samples
    || form.recovery_median_seconds > form.first_token_seconds
    || !Number.isInteger(form.protection_minutes) || form.protection_minutes < 0 || form.protection_minutes > 1440) {
    error.value = t('admin.settings.openaiAutoGroup.invalid')
    return
  }
  saving.value = true
  try {
    Object.assign(form, await adminAPI.settings.updateOpenAIAutoGroupSettings({ ...form }))
    saved.value = true
  } catch (cause) {
    error.value = `${t('admin.settings.openaiAutoGroup.saveFailed')} ${extractApiErrorMessage(cause, t('admin.settings.openaiAutoGroup.retryHint'))}`
  } finally {
    saving.value = false
  }
}

onMounted(() => { void load(); void loadGroups() })
</script>
