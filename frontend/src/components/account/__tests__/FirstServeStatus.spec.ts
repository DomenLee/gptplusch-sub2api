import { afterEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import FirstServeStatus from '../FirstServeStatus.vue'
import { getFirstServeStatus } from '@/api/admin/accounts'

vi.mock('@/api/admin/accounts', () => ({ getFirstServeStatus: vi.fn() }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string, params?: Record<string, unknown>) => params ? `${key}:${Object.values(params).join(',')}` : key }) }))

afterEach(() => { vi.clearAllMocks(); vi.useRealTimers() })

describe('FirstServeStatus', () => {
  it('shows the failed account and retry action, then clears the error after refresh', async () => {
    vi.mocked(getFirstServeStatus).mockRejectedValueOnce(new Error('unavailable')).mockResolvedValue([])
    const wrapper = mount(FirstServeStatus, { props: { accountId: 1, accountName: 'Account A' } })
    await flushPromises()
    expect(wrapper.get('[role="alert"]').text()).toContain('Account A')
    await wrapper.get('button').trigger('click')
    await flushPromises()
    expect(wrapper.find('[role="alert"]').exists()).toBe(false)
    wrapper.unmount()
  })

  it('shows the affected proxy and reason, and cancels polling on unmount', async () => {
    vi.useFakeTimers()
    vi.mocked(getFirstServeStatus).mockResolvedValue([{
      id: 'session-1', account_id: 1, proxy_id: 2, proxy_name: 'Proxy B', conn_id: 'conn-1',
      expires_at: '2026-09-24T10:30:00Z', updated_at: '2026-09-24T10:10:00Z',
      first_token_ms: 15001, rotations: 1, reason: 'proxy_unavailable', active: true,
      config: { ttl_minutes: 12, ttft_seconds: 4, max_switches: 2, cooldown_seconds: 10, proxy_mode: 'all', proxy_ids: [] }
    }])
    const wrapper = mount(FirstServeStatus, { props: { accountId: 1, accountName: 'Account A' } })
    await flushPromises()
    expect(wrapper.text()).toContain('Account A · Proxy B')
    expect(wrapper.get('[role="alert"]').text()).toContain('proxy_unavailable')
    expect(wrapper.get('[role="alert"]').text()).toContain('4,2,10')
    const signal = vi.mocked(getFirstServeStatus).mock.calls[0]?.[1]
    wrapper.unmount()
    expect(signal?.aborted).toBe(true)
    await vi.advanceTimersByTimeAsync(20000)
    expect(getFirstServeStatus).toHaveBeenCalledTimes(1)
  })
})
