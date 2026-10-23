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
      <p v-if="loading" class="text-sm text-gray-500">{{ t('common.loading') }}</p>
      <template v-else-if="loaded">
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
          <div>
            <label for="auto-group-window" class="input-label">{{ t('admin.settings.openaiAutoGroup.windowMinutes') }}</label>
            <input id="auto-group-window" v-model.number="form.window_minutes" class="input w-32" type="number" min="1" max="60" step="1" :disabled="saving" />
          </div>
          <div>
            <label for="auto-group-threshold" class="input-label">{{ t('admin.settings.openaiAutoGroup.failureThreshold') }}</label>
            <input id="auto-group-threshold" v-model.number="form.failure_threshold" class="input w-32" type="number" min="1" max="1000" step="1" :disabled="saving" />
          </div>
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
import { onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import type { OpenAIAutoGroupSettings } from '@/api/admin/settings'
import Toggle from '@/components/common/Toggle.vue'
import { extractApiErrorMessage } from '@/utils/apiError'

const { t } = useI18n()
const form = reactive<OpenAIAutoGroupSettings>({ default_high_rate: false, enabled: false, window_minutes: 5, failure_threshold: 3 })
const loading = ref(true)
const loaded = ref(false)
const saving = ref(false)
const saved = ref(false)
const error = ref('')

async function load() {
  loading.value = true
  error.value = ''
  try {
    Object.assign(form, await adminAPI.settings.getOpenAIAutoGroupSettings())
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
  if (!Number.isInteger(form.window_minutes) || form.window_minutes < 1 || form.window_minutes > 60 || !Number.isInteger(form.failure_threshold) || form.failure_threshold < 1 || form.failure_threshold > 1000) {
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

onMounted(load)
</script>
