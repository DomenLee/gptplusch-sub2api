<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, reactive, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { apiClient as api } from '@/api/client'
import { getAccountState, type AccountState } from '@/api/account'
import { useAuthStore } from '@/stores/auth'

const props = defineProps<{ action: 'link' | 'email' }>()
const emit = defineEmits<{ updated: [] }>()
const auth = useAuthStore()
const router = useRouter()
const state = ref<AccountState | null>(null)
const loading = ref(true)
const busy = ref(false)
const error = ref('')
const notice = ref('')
const complete = ref(false)
const errorElement = ref<HTMLElement>()
const form = reactive({ email: '', password: '', oldPassword: '', code: '', oldCode: '', totpCode: '' })
const differentPassword = ref(false)
const purpose = computed(() => props.action === 'email' ? 'email-new' : 'link')
const codes = reactive<Record<string, { id: string; seconds: number }>>({})
let timer: ReturnType<typeof setInterval> | undefined
let requestId = ''
watch(error, async (value) => {
  if (!value) return
  await nextTick()
  errorElement.value?.scrollIntoView({ block: 'center', behavior: 'smooth' })
})
watch(() => form.email, () => { delete codes['link']; delete codes['email-new']; form.code = '' })
watch(() => [form.email, form.password, form.code, form.oldCode], () => { requestId = '' })
onMounted(async () => {
  try {
    state.value = await getAccountState()
    if (props.action === 'link' && state.value.email && !state.value.email.endsWith('.invalid') && !state.value.email.endsWith('.local')) form.email = state.value.email
  } catch { error.value = '账号信息读取失败，请关闭后重试。' }
  finally { loading.value = false }
  timer = setInterval(() => Object.values(codes).forEach(code => { if (code.seconds > 0) code.seconds-- }), 1000)
})
onUnmounted(() => { if (timer) clearInterval(timer) })
const failure = (err: any) => err.response?.data?.message || err.message || '操作失败，请稍后重试'
async function send(p: string) {
  if (busy.value || codes[p]?.seconds) return
  busy.value = true
  error.value = ''
  notice.value = ''
  const email = p === 'email-old' ? state.value?.account?.email : form.email
  try {
    const { data } = await api.post('/auth/unified/account/code', { email, purpose: p })
    codes[p] = { id: data.challengeId, seconds: data.retryAfter || 60 }
    notice.value = `验证码已发送至 ${email}，5 分钟内有效。`
  } catch (err) { error.value = failure(err) }
  finally { busy.value = false }
}
async function submit() {
  if (busy.value || !state.value) return
  error.value = ''
  notice.value = ''
  if (!codes[purpose.value]?.id) { error.value = '请先获取邮箱验证码'; return }
  if (props.action === 'email' && !codes['email-old']?.id) { error.value = '请先获取原邮箱验证码'; return }
  busy.value = true
  const action = props.action
  try {
    if (action === 'email') requestId ||= crypto.randomUUID()
    const { data } = await api.post(`/auth/unified/account/${action}`, {
      email: form.email, password: action === 'link' && !differentPassword.value ? form.oldPassword : form.password,
      oldPassword: form.oldPassword, totpCode: form.totpCode,
      code: form.code, challengeId: codes[purpose.value]?.id,
      ...(action === 'email' ? { requestId, version: state.value.account?.version, oldCode: form.oldCode, oldChallengeId: codes['email-old']?.id } : {})
    })
    form.password = ''; form.oldPassword = ''; form.totpCode = ''
    if (action === 'link') {
      await auth.refreshUser()
      complete.value = true
      notice.value = '邮箱验证完成，账号已关联。'
      emit('updated')
    } else {
      complete.value = true
      auth.clearSession()
      notice.value = '登录邮箱已更新，请重新登录本站。' + (data?.notice || '')
    }
  } catch (err: any) {
    const code = err.reason || err.code || err.response?.data?.reason || err.response?.data?.code
    if (action === 'link' && ['INVALID_CREDENTIALS', 'PASSWORD_INVALID'].includes(code)) differentPassword.value = true
    error.value = failure(err)
  } finally { busy.value = false }
}
</script>

<template>
  <div class="mt-4 space-y-4" :aria-busy="busy || loading">
    <p v-if="loading" role="status" class="input-hint">正在读取账号信息…</p>
    <p v-if="error" ref="errorElement" role="alert" class="sticky top-2 z-10 rounded-lg bg-red-50 p-3 text-sm text-red-700 dark:bg-red-900/30 dark:text-red-300">{{ error }}</p>
    <p v-if="notice" role="status" class="rounded-lg bg-green-50 p-3 text-sm text-green-700 dark:bg-green-900/30 dark:text-green-300">{{ notice }}</p>
    <button v-if="complete && action === 'email'" class="btn btn-primary" type="button" @click="router.push('/login')">返回登录</button>
    <form v-else-if="!complete && !loading && state" class="space-y-4" @submit.prevent="submit">
      <label class="block"><span class="input-label">{{ action === 'email' ? '新邮箱' : '邮箱' }}</span><input v-model.trim="form.email" type="email" autocomplete="email" class="input" maxlength="120" required :disabled="busy" /></label>
      <label v-if="action === 'link'" class="block"><span class="input-label">本站当前密码</span><input v-model="form.oldPassword" type="password" autocomplete="current-password" class="input" required :disabled="busy" /></label>
      <label v-if="action === 'link'" class="flex items-center gap-2 text-sm text-gray-600 dark:text-dark-300"><input v-model="differentPassword" type="checkbox" :disabled="busy" />需要使用另一密码</label>
      <label v-if="action === 'email' || differentPassword" class="block"><span class="input-label">{{ action === 'link' ? '登录密码（另一站已注册时填写其密码）' : '当前密码' }}</span><input v-model="form.password" type="password" autocomplete="current-password" class="input" required :disabled="busy" /></label>
      <label v-if="action === 'email'" class="block"><span class="input-label">原邮箱验证码</span><div class="grid gap-2 sm:grid-cols-[minmax(0,1fr)_auto]"><input v-model="form.oldCode" class="input" inputmode="numeric" pattern="[0-9]{6}" maxlength="6" autocomplete="one-time-code" required :disabled="busy" /><button class="btn btn-secondary" type="button" :disabled="busy || !!codes['email-old']?.seconds" @click="send('email-old')">{{ codes['email-old']?.seconds ? `${codes['email-old'].seconds} 秒后重发` : '获取原邮箱验证码' }}</button></div></label>
      <label class="block"><span class="input-label">{{ action === 'email' ? '新邮箱验证码' : '邮箱验证码' }}</span><div class="grid gap-2 sm:grid-cols-[minmax(0,1fr)_auto]"><input v-model="form.code" class="input" inputmode="numeric" pattern="[0-9]{6}" maxlength="6" autocomplete="one-time-code" required :disabled="busy" /><button class="btn btn-secondary" type="button" :disabled="busy || !form.email || !!codes[purpose]?.seconds" @click="send(purpose)">{{ codes[purpose]?.seconds ? `${codes[purpose].seconds} 秒后重发` : '获取验证码' }}</button></div></label>
      <label v-if="state.requiresMfa" class="block"><span class="input-label">二次验证码</span><input v-model="form.totpCode" class="input" inputmode="numeric" autocomplete="one-time-code" maxlength="20" required :disabled="busy" /></label>
      <button class="btn btn-primary" type="submit" :disabled="busy">{{ busy ? '处理中…' : action === 'link' ? '验证邮箱并关联' : '保存邮箱' }}</button>
    </form>
  </div>
</template>
