import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import LoginView from '@/views/auth/LoginView.vue'

const { getPublicSettingsMock, pushMock, loginMock } = vi.hoisted(() => ({
  getPublicSettingsMock: vi.fn(),
  pushMock: vi.fn(),
  loginMock: vi.fn()
}))

const publicSettings = {
  registration_enabled: true,
  turnstile_enabled: false,
  turnstile_site_key: '',
  tencent_captcha_enabled: false,
  tencent_captcha_app_id: '',
  aliyun_captcha_enabled: false,
  aliyun_captcha_scene_id: '',
  aliyun_captcha_prefix: '',
  linuxdo_oauth_enabled: false,
  dingtalk_oauth_enabled: false,
  wechat_oauth_enabled: false,
  backend_mode_enabled: false,
  oidc_oauth_enabled: false,
  oidc_oauth_provider_name: 'OIDC',
  github_oauth_enabled: false,
  google_oauth_enabled: false,
  password_reset_enabled: false,
  passkey_enabled: false,
  login_agreement_enabled: false,
  login_agreement_documents: []
}

vi.mock('vue-router', () => ({
  useRouter: () => ({
    push: pushMock,
    currentRoute: { value: { query: {} } }
  })
}))

vi.mock('vue-i18n', () => ({
  createI18n: () => ({
    global: {
      t: (key: string) => key
    }
  }),
  useI18n: () => ({
    t: (key: string) => key
  })
}))

vi.mock('@/stores', () => ({
  useAuthStore: () => ({
    login: loginMock,
    loginWithPasskey: vi.fn(),
    login2FA: vi.fn()
  }),
  useAppStore: () => ({
    showError: vi.fn(),
    showSuccess: vi.fn(),
    showWarning: vi.fn()
  })
}))

vi.mock('@/api/auth', () => ({
  buildOAuthLoginStartURL: vi.fn(),
  getPublicSettings: (...args: unknown[]) => getPublicSettingsMock(...args),
  isTotp2FARequired: vi.fn(() => false),
  isWeChatWebOAuthEnabled: vi.fn(() => false),
  startOAuthLogin: vi.fn()
}))

function mountLogin() {
  return mount(LoginView, {
    global: {
      stubs: {
        AuthLayout: { template: '<div><slot /><slot name="footer" /></div>' },
        DingTalkOAuthSection: true,
        EmailOAuthButtons: true,
        Icon: true,
        LinuxDoOAuthSection: true,
        LoginAgreementPrompt: true,
        OidcOAuthSection: true,
        RouterLink: { template: '<a><slot /></a>' },
        TotpLoginModal: true,
        TurnstileWidget: true,
        WechatOAuthSection: true,
        transition: false
      }
    }
  })
}

describe('LoginView registration entry', () => {
  beforeEach(() => {
    getPublicSettingsMock.mockReset()
    pushMock.mockReset()
    loginMock.mockReset()
    getPublicSettingsMock.mockResolvedValue(publicSettings)
  })

  it('only asks for an internal invitation after the server requires first-site activation', async () => {
    getPublicSettingsMock.mockResolvedValueOnce({ ...publicSettings, password_reset_enabled: true })
    loginMock.mockRejectedValueOnce({ code: 400, reason: 'INVITATION_CODE_REQUIRED', message: '需要内部邀请码' })
    const wrapper = mountLogin()
    await flushPromises()
    expect(wrapper.text()).not.toContain('首次开通需要内部邀请码')
    expect(wrapper.text().match(/auth.forgotPassword/g)).toHaveLength(1)
    expect(wrapper.text()).not.toContain('找回通用密码')
    await wrapper.get('#email').setValue('new@example.test')
    await wrapper.get('#password').setValue('Password2026')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(loginMock).toHaveBeenCalled()
    expect(loginMock.mock.calls[0][0]).not.toHaveProperty('invitation_code')
    expect(wrapper.text()).toContain('首次开通需要内部邀请码')
    await wrapper.get('input[placeholder="请输入本站发放的内部邀请码"]').setValue('INTERNAL-CODE')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(loginMock.mock.calls[1][0]).toHaveProperty('invitation_code', 'INTERNAL-CODE')
    await wrapper.get('#email').setValue('other@example.test')
    expect(wrapper.text()).not.toContain('首次开通需要内部邀请码')
    wrapper.unmount()
  })

  it('shows the registration entry when registration is enabled', async () => {
    const wrapper = mountLogin()
    await flushPromises()

    expect(wrapper.text()).toContain('auth.signUp')
  })

  it('hides the registration entry when registration is disabled', async () => {
    getPublicSettingsMock.mockResolvedValueOnce({
      ...publicSettings,
      registration_enabled: false
    })

    const wrapper = mountLogin()
    await flushPromises()

    expect(wrapper.text()).not.toContain('auth.signUp')
  })
})
