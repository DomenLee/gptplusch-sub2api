<template>
  <div :class="props.embedded ? 'space-y-4' : 'card'">
    <div
      v-if="!props.embedded"
      class="border-b border-gray-100 px-6 py-4 dark:border-dark-700"
    >
      <h2 class="text-lg font-medium text-gray-900 dark:text-white">
        {{ t('profile.changePassword') }}
      </h2>
    </div>
    <div :class="props.embedded ? '' : 'px-6 py-6'">
      <form @submit.prevent="handleChangePassword" class="space-y-4">
        <div v-if="props.embedded">
          <p class="text-sm font-semibold text-gray-900 dark:text-white">
            {{ t('profile.changePassword') }}
          </p>
        </div>
        <div>
          <label for="old_password" class="input-label">
            {{ t('profile.currentPassword') }}
          </label>
          <input
            id="old_password"
            v-model="form.old_password"
            type="password"
            required
            autocomplete="current-password"
            class="input"
          />
        </div>

        <div>
          <label for="new_password" class="input-label">
            {{ t('profile.newPassword') }}
          </label>
          <input
            id="new_password"
            v-model="form.new_password"
            type="password"
            required
            autocomplete="new-password"
            class="input"
          />
          <p class="input-hint">
            {{ props.unifiedAccount ? '新密码至少 12 个字符，两站下次登录均使用新密码。' : t('profile.passwordHint') }}
          </p>
        </div>

        <div>
          <label for="confirm_password" class="input-label">
            {{ t('profile.confirmNewPassword') }}
          </label>
          <input
            id="confirm_password"
            v-model="form.confirm_password"
            type="password"
            required
            autocomplete="new-password"
            class="input"
          />
        </div>

        <div v-if="props.unifiedAccount && props.requiresMfa">
          <label for="password_totp" class="input-label">二次验证码</label>
          <input id="password_totp" v-model="totpCode" class="input" inputmode="numeric" autocomplete="one-time-code" required />
        </div>
        <div class="flex justify-end pt-4">
          <button type="submit" :disabled="loading" class="btn btn-primary">
            {{ loading ? t('profile.changingPassword') : t('profile.changePasswordButton') }}
          </button>
        </div>
      </form>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { useAuthStore } from '@/stores/auth'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import { userAPI } from '@/api'
import { extractApiErrorMessage } from '@/utils/apiError'

const { t } = useI18n()
const appStore = useAppStore()
const props = withDefaults(defineProps<{
  embedded?: boolean
  unifiedAccount?: { id: string; email: string; version: number } | null
  requiresMfa?: boolean
}>(), {
  embedded: false,
})

const router = useRouter()
const totpCode = ref('')
let requestId = ''
const loading = ref(false)
const form = ref({
  old_password: '',
  new_password: '',
  confirm_password: ''
})

watch(() => [form.value.old_password, form.value.new_password], () => { requestId = '' })

const handleChangePassword = async () => {
  if (form.value.new_password !== form.value.confirm_password) {
    appStore.showError(t('profile.passwordsNotMatch'))
    return
  }

  if (Array.from(form.value.new_password).length < (props.unifiedAccount ? 12 : 8) || (props.unifiedAccount && new TextEncoder().encode(form.value.new_password).length > 72)) {
    appStore.showError(props.unifiedAccount ? '新密码至少 12 个字符；中文或特殊字符较多时请适当缩短。' : t('profile.passwordTooShort'))
    return
  }

  loading.value = true
  try {
    if (props.unifiedAccount) requestId ||= crypto.randomUUID()
    const result = props.unifiedAccount
      ? await userAPI.changePassword(form.value.old_password, form.value.new_password, { request_id: requestId, credential_version: props.unifiedAccount.version, totp_code: totpCode.value })
      : await userAPI.changePassword(form.value.old_password, form.value.new_password)
    if (result?.reauthenticate) {
      useAuthStore().clearSession()
      appStore.showSuccess('密码已更新，请重新登录本站。')
      await router.push('/login')
      return
    }
    form.value = { old_password: '', new_password: '', confirm_password: '' }
    appStore.showSuccess(t('profile.passwordChangeSuccess'))
  } catch (error: unknown) {
    appStore.showError(extractApiErrorMessage(error, t('profile.passwordChangeFailed')))
  } finally {
    loading.value = false
  }
}
</script>
