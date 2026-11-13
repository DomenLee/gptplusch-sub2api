import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import ProfilePasswordForm from '@/components/user/profile/ProfilePasswordForm.vue'

const { changePasswordMock, showSuccessMock, showErrorMock, clearSessionMock, pushMock } = vi.hoisted(() => ({
  changePasswordMock: vi.fn(),
  showSuccessMock: vi.fn(),
  showErrorMock: vi.fn(),
  clearSessionMock: vi.fn(),
  pushMock: vi.fn()
}))

vi.mock('vue-router', () => ({ useRouter: () => ({ push: pushMock }) }))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => ({ clearSession: clearSessionMock }) }))

vi.mock('@/api', () => ({
  userAPI: {
    changePassword: changePasswordMock
  }
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showSuccess: showSuccessMock,
    showError: showErrorMock
  })
}))

vi.mock('vue-i18n', async (importOriginal) => {
  const actual = await importOriginal<typeof import('vue-i18n')>()
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => {
        const translations: Record<string, string> = {
          'profile.changePassword': 'Change Password',
          'profile.currentPassword': 'Current Password',
          'profile.newPassword': 'New Password',
          'profile.confirmNewPassword': 'Confirm New Password',
          'profile.passwordHint': 'Password must be at least 8 characters long',
          'profile.changingPassword': 'Changing...',
          'profile.changePasswordButton': 'Change Password',
          'profile.passwordsNotMatch': 'New passwords do not match',
          'profile.passwordTooShort': 'Password must be at least 8 characters long',
          'profile.passwordChangeSuccess': 'Password changed successfully',
          'profile.passwordChangeFailed': 'Failed to change password'
        }
        return translations[key] ?? key
      }
    })
  }
})

describe('ProfilePasswordForm', () => {
  beforeEach(() => { vi.resetAllMocks() })
  const shared = { id: 'shared-id', email: 'user@example.test', version: 2 }

  it.each(['12345678901', '😀'.repeat(6), '密'.repeat(25)])('retains shared password length validation for %s', async (password) => {
    const wrapper = mount(ProfilePasswordForm, { props: { unifiedAccount: shared } })
    await wrapper.get('#old_password').setValue('CurrentPassword2026')
    await wrapper.get('#new_password').setValue(password)
    await wrapper.get('#confirm_password').setValue(password)
    await wrapper.get('form').trigger('submit')
    expect(changePasswordMock).not.toHaveBeenCalled()
    expect(showErrorMock).toHaveBeenCalled()
    wrapper.unmount()
  })

  it('reuses the original endpoint with MFA and a stable request on retry, then exits only this site', async () => {
    changePasswordMock.mockRejectedValueOnce({ message: '暂未确认，请重试' }).mockResolvedValueOnce({ reauthenticate: true })
    const wrapper = mount(ProfilePasswordForm, { props: { unifiedAccount: shared, requiresMfa: true } })
    await wrapper.get('#old_password').setValue('CurrentPassword2026')
    await wrapper.get('#new_password').setValue('ChangedPassword2026')
    await wrapper.get('#confirm_password').setValue('ChangedPassword2026')
    await wrapper.get('#password_totp').setValue('123456')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    const proof = changePasswordMock.mock.calls[0][2]
    expect(proof).toMatchObject({ credential_version: 2, totp_code: '123456' })
    expect(proof.request_id.length).toBeGreaterThanOrEqual(16)
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(changePasswordMock.mock.calls[1][2]).toEqual(proof)
    expect(clearSessionMock).toHaveBeenCalledOnce()
    expect(pushMock).toHaveBeenCalledWith('/login')
    wrapper.unmount()
  })

  it('shows validation failures as toast messages instead of inline errors', async () => {
    const wrapper = mount(ProfilePasswordForm)

    await wrapper.get('#old_password').setValue('old-password')
    await wrapper.get('#new_password').setValue('new-password')
    await wrapper.get('#confirm_password').setValue('different-password')
    await wrapper.get('form').trigger('submit.prevent')

    expect(changePasswordMock).not.toHaveBeenCalled()
    expect(showErrorMock).toHaveBeenCalledWith('New passwords do not match')
    expect(wrapper.find('.input-error-text').exists()).toBe(false)
  })

  it.each([
    [{ status: 400, code: 'PASSWORD_INCORRECT', message: 'current password is incorrect' }, 'current password is incorrect'],
    [{ response: { data: { detail: 'backend failure' } } }, 'backend failure'],
    [{}, 'Failed to change password'],
  ])('shows API failure %j as a toast', async (error, expectedMessage) => {
    changePasswordMock.mockRejectedValue(error)

    const wrapper = mount(ProfilePasswordForm)

    await wrapper.get('#old_password').setValue('old-password')
    await wrapper.get('#new_password').setValue('new-password')
    await wrapper.get('#confirm_password').setValue('new-password')
    await wrapper.get('form').trigger('submit.prevent')

    expect(changePasswordMock).toHaveBeenCalledWith('old-password', 'new-password')
    expect(showErrorMock).toHaveBeenLastCalledWith(expectedMessage)
    expect(wrapper.find('.input-error-text').exists()).toBe(false)
  })
})
