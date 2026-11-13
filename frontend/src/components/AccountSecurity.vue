<script setup lang="ts">
import { useAuthStore } from "@/stores/auth";
const auth = useAuthStore();
const requiresMfa = ref(false);
import { computed, nextTick, onMounted, onUnmounted, reactive, ref, watch } from "vue";
import { apiClient as api } from "@/api/client";
const emit = defineEmits<{ linked: [value: boolean] }>();
const props = defineProps<{ recover?: boolean }>();
const enabled = ref(false), loading = ref(true), busy = ref(false), error = ref(""), notice = ref(""), complete = ref(false);
const account = ref<{ id: string; email: string; version: number } | null>(null);
const action = ref("password");
const errorElement = ref<HTMLElement>();
watch(error, async (value) => {
  if (!value) return;
  await nextTick();
  errorElement.value?.scrollIntoView({ block: "center", behavior: "smooth" });
});
const form = reactive({ email: "", password: "", newPassword: "", oldPassword: "", code: "", oldCode: "", confirm: "", totpCode: "" });
const codes = reactive<Record<string, { id: string; seconds: number }>>({});
const mode = computed(() => props.recover ? "recover" : !account.value ? "link" : action.value);
const purpose = computed(() => mode.value === "email" ? "email-new" : mode.value);
let timer: ReturnType<typeof setInterval> | undefined;
let operationRequestId = "";
const failure = (err: any) => err.response?.data?.message || err.message || "操作失败，请稍后重试";
async function refresh() {
  const { data } = await api.get("/auth/unified/account"); account.value = data.account; requiresMfa.value = !!data.requiresMfa; emit("linked", !!account.value);
}
onMounted(async () => {
  try {
    enabled.value = (await api.get("/auth/unified/config")).data.enabled;
    if (enabled.value && !props.recover) await refresh();
  } catch (err) { error.value = failure(err); }
  finally { loading.value = false; }
  timer = setInterval(() => Object.values(codes).forEach(code => { if (code.seconds > 0) code.seconds--; }), 1000);
});
onUnmounted(() => { if (timer) clearInterval(timer); });
watch(() => form.email, () => { for (const key of ["link", "recover", "email-new"]) delete codes[key]; form.code = ""; });
watch(() => [mode.value, form.email, form.password, form.newPassword, form.code, form.oldCode], () => { operationRequestId = ""; });
watch(mode, () => { error.value = ""; notice.value = ""; form.password = ""; form.newPassword = ""; form.confirm = ""; form.code = ""; form.oldCode = ""; });
async function send(p: string) {
  if (busy.value || codes[p]?.seconds) return;
  error.value = ""; notice.value = ""; busy.value = true;
  try {
    const { data } = await api.post(props.recover ? "/auth/unified/code" : "/auth/unified/account/code", { email: p === "email-old" ? account.value?.email : form.email, purpose: p });
    codes[p] = { id: data.challengeId, seconds: data.retryAfter || 60 };
    notice.value = `验证码已发送至 ${p === "email-old" ? account.value?.email : form.email}，5 分钟内有效。`;
  } catch (err) { error.value = failure(err); }
  finally { busy.value = false; }
}
async function submit() {
  if (busy.value) return;
  error.value = ""; notice.value = "";
  const password = mode.value === "link" ? form.password : form.newPassword;
  if (mode.value !== "email" && (Array.from(password).length < 12 || new TextEncoder().encode(password).length > 72)) { error.value = "通用密码至少 12 个字符，最多 72 字节"; return; }
  if (mode.value !== "email" && password !== form.confirm) { error.value = "两次输入的密码不一致，请核对"; return; }
  if (mode.value !== "password" && !codes[purpose.value]?.id) { error.value = "请先获取当前邮箱的验证码"; return; }
  busy.value = true;
  try {
    if (mode.value === "password" || mode.value === "email" || mode.value === "recover") operationRequestId ||= crypto.randomUUID();
    const { data } = await api.post(props.recover ? "/auth/unified/recover" : `/auth/unified/account/${mode.value}`, {
      password: form.password, newPassword: form.newPassword,
      totpCode: form.totpCode, version: account.value?.version,
      ...(operationRequestId ? { requestId: operationRequestId } : {}),
      ...(mode.value !== "password" ? { email: form.email, code: form.code, challengeId: codes[purpose.value]?.id } : {}),
      ...(mode.value === "email" ? { oldCode: form.oldCode, oldChallengeId: codes["email-old"]?.id } : {}),
      ...(mode.value === "link" ? { oldPassword: form.oldPassword } : {})
    });
    form.password = ""; form.newPassword = ""; form.oldPassword = ""; form.confirm = ""; form.totpCode = "";
    operationRequestId = "";
    if (mode.value === "link") { await refresh(); notice.value = "账号关联成功。两站使用同一邮箱和密码，原有订单和资产保留。"; }
    else { complete.value = true; auth.clearSession(); notice.value = "修改成功，请重新登录本站。另一站已有登录状态保持不变。" + (data?.notice || ""); }
  } catch (err) { error.value = failure(err); }
  finally { busy.value = false; }
}
async function logoutAll() {
  busy.value = true; error.value = "";
  try { await api.post("/auth/unified/account/logout-all", {}); complete.value = true; auth.clearSession(); notice.value = "已退出本站全部设备，另一站保持登录。"; }
  catch (err) { error.value = failure(err); }
  finally { busy.value = false; }
}
function goLogin() { window.location.assign("/login"); }
</script>

<template>
  <section id="unified-account-security" v-if="loading || enabled || error" class="account-security" :aria-busy="busy || loading">
    <h2>{{ recover ? "找回通用密码" : "通用账号安全" }}</h2>
    <p class="account-hint">商城与 AI 站共用邮箱和密码；登录、续期和退出分别管理。</p>
    <p v-if="loading" role="status">正在读取账号状态…</p>
    <p v-if="account" class="account-badge">已验证邮箱：{{ account.email }}</p>
    <p v-if="!loading && !account && !recover">升级前请验证本站原密码和邮箱。已有通用账号时，请填写其当前密码。</p>
    <p v-if="error" ref="errorElement" class="account-error" role="alert">{{ error }}</p>
    <p v-if="notice" class="account-notice" role="status">{{ notice }}</p>
    <button v-if="complete" class="account-button" type="button" @click="goLogin">返回登录</button>
    <template v-else-if="!loading && enabled">
      <div v-if="account && !recover" class="account-tabs">
        <button type="button" :disabled="busy" :aria-pressed="action === 'password'" @click="action = 'password'">修改密码</button>
        <button type="button" :disabled="busy" :aria-pressed="action === 'email'" @click="action = 'email'">更换邮箱</button>
      </div>
      <form @submit.prevent="submit">
        <label v-if="mode === 'link'">本站原密码<input v-model="form.oldPassword" type="password" autocomplete="current-password" required :disabled="busy" /></label>
        <label v-if="mode !== 'password'">{{ mode === 'email' ? '新邮箱' : '邮箱' }}<input v-model.trim="form.email" type="email" autocomplete="email" maxlength="120" required :disabled="busy" /></label>
        <label v-if="mode !== 'recover'">{{ mode === 'link' ? '通用密码（已有账号填写当前密码）' : '当前通用密码' }}<input v-model="form.password" type="password" autocomplete="current-password" required :disabled="busy" /></label>
        <label v-if="mode === 'email'">原邮箱验证码<div class="account-code"><input v-model="form.oldCode" inputmode="numeric" pattern="[0-9]{6}" maxlength="6" autocomplete="one-time-code" required :disabled="busy" /><button type="button" :disabled="busy || !!codes['email-old']?.seconds" @click="send('email-old')">{{ codes['email-old']?.seconds ? `${codes['email-old'].seconds} 秒` : '获取原邮箱验证码' }}</button></div></label>
        <label v-if="mode !== 'password'">{{ mode === 'email' ? '新邮箱验证码' : '邮箱验证码' }}<div class="account-code"><input v-model="form.code" inputmode="numeric" pattern="[0-9]{6}" maxlength="6" autocomplete="one-time-code" required :disabled="busy" /><button type="button" :disabled="busy || !form.email || !!codes[purpose]?.seconds" @click="send(purpose)">{{ codes[purpose]?.seconds ? `${codes[purpose].seconds} 秒` : '获取验证码' }}</button></div></label>
        <label v-if="mode === 'password' || mode === 'recover'">新通用密码<input v-model="form.newPassword" type="password" autocomplete="new-password" minlength="12" required :disabled="busy" /></label>
        <label v-if="mode !== 'email'">确认{{ mode === 'link' ? '通用' : '新' }}密码<input v-model="form.confirm" type="password" autocomplete="new-password" required :disabled="busy" /></label>
        <label v-if="!recover && requiresMfa">二次验证码（已开启二次验证时必填）<input v-model="form.totpCode" inputmode="numeric" autocomplete="one-time-code" maxlength="20" :disabled="busy" /></label>
        <p class="account-hint">密码至少 12 个字符，最多 72 字节。修改后，两站下次密码登录均使用新密码。</p>
        <button class="account-button" type="submit" :disabled="busy">{{ busy ? "处理中…" : mode === "link" ? "验证并关联账号" : "确认修改" }}</button>
      </form>
      <button v-if="account && !recover" class="account-logout" type="button" :disabled="busy" @click="logoutAll">退出本站全部设备</button>
    </template>
    <RouterLink v-if="recover" to="/login">返回登录；未升级的历史账号仍使用原找回入口</RouterLink>
  </section>
</template>

<style scoped>
.account-security { box-sizing: border-box; width: 100%; max-width: 620px; margin: 24px auto; padding: 28px; border: 1px solid #dbe2ea; border-radius: 20px; background: var(--panel, #fff); color: #172334; }
h2 { font-size: 24px; font-weight: 700; margin: 0 0 12px; }
p { line-height: 1.6; margin: 12px 0; overflow-wrap: anywhere; }
.account-hint { color: #536175; font-size: 14px; }
.account-badge, .account-notice { padding: 12px; background: #eaf8f0; color: #145d3b; border-radius: 10px; }
.account-error { padding: 12px; background: #fff0ed; color: #a12719; border-radius: 10px; position: sticky; top: 8px; z-index: 2; }
form { display: grid; gap: 18px; margin-top: 20px; }
label { display: grid; gap: 8px; font-size: 14px; font-weight: 600; }
input { width: 100%; min-width: 0; box-sizing: border-box; border: 1px solid #bcc8d5; border-radius: 9px; padding: 12px; font-size: 16px; background: #fff; color: #172334; }
input:focus { outline: 2px solid #3c74ed; outline-offset: 2px; }
button { border: 1px solid #bcc8d5; border-radius: 9px; padding: 11px 14px; font-size: 14px; background: #fff; color: #172334; cursor: pointer; }
button:disabled { opacity: .55; cursor: default; }
.account-button { background: #1f4fd1; color: white; border-color: #1f4fd1; min-height: 46px; }
.account-code { display: grid; grid-template-columns: minmax(0, 1fr) auto; gap: 8px; }
.account-tabs { display: flex; gap: 8px; margin: 20px 0; }
.account-tabs [aria-pressed="true"] { background: #eaf0ff; border-color: #1f4fd1; color: #1f4fd1; }
.account-logout { margin: 20px 0; color: #a12719; }
a { display: block; margin-top: 18px; color: #1f4fd1; }
@media(max-width: 420px) { .account-security { padding: 18px; margin: 16px auto; border-radius: 14px; } .account-code { grid-template-columns: minmax(0, 1fr); } }
</style>
