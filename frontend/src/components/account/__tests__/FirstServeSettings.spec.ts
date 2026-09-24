import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import type { Proxy, ProxyGroup } from '@/types'
import FirstServeSettings from '../FirstServeSettings.vue'
import { readFirstServeConfig, type FirstServeConfig } from '@/utils/firstServe'

vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string, params?: Record<string, unknown>) => params ? `${key}:${Object.values(params).join(',')}` : key }) }))

const groups = [{ id: 10, name: 'Group A', status: 'active', proxy_ids: [1, 2], member_count: 2, available_member_count: 2 },
  { id: 20, name: 'Group B', status: 'active', proxy_ids: [3], member_count: 1, available_member_count: 1 }] as ProxyGroup[]
const proxies = [1, 2, 3].map(id => ({ id, name: `Proxy ${id}`, host: `proxy-${id}`, port: 8080, ip_address: `203.0.113.${id}`, status: 'active', username: 'secret-user', password: 'secret-password' })) as Proxy[]

function render(config = readFirstServeConfig()) {
  const wrapper = mount(FirstServeSettings, {
    props: { modelValue: config, proxyGroupId: 10, accountName: 'Account A', proxyGroups: groups, proxies,
      'onUpdate:modelValue': (value: FirstServeConfig) => { void wrapper.setProps({ modelValue: value }) },
      'onUpdate:proxyGroupId': (value: number | null) => { void wrapper.setProps({ proxyGroupId: value }) }
    }
  })
  return wrapper
}

describe('FirstServeSettings', () => {
  it('limits proxy choices to the bound group, displays exit IPs without credentials, and requires explicit selection', async () => {
    const wrapper = render()
    await wrapper.get('[data-testid="first-serve-proxy-mode"]').setValue('selected')
    expect(wrapper.get('[role="alert"]').text()).toContain('Account A')
    expect(wrapper.text()).toContain('203.0.113.1')
    expect(wrapper.text()).not.toContain('secret-user')
    expect(wrapper.text()).not.toContain('secret-password')
    expect(wrapper.find('[data-testid="first-serve-proxy-3"]').exists()).toBe(false)
    await wrapper.get('[data-testid="first-serve-proxy-1"]').setValue(true)
    await wrapper.get('[data-testid="first-serve-proxy-2"]').setValue(true)
    expect(wrapper.vm.validate()).toBe(true)
    await wrapper.get('[data-testid="first-serve-group"]').setValue('20')
    expect(wrapper.vm.validate()).toBe(false)
    expect(wrapper.get('[role="alert"]').text()).toContain('#1, #2')
    expect(wrapper.props('modelValue').proxy_ids).toEqual([1, 2])
    expect(wrapper.props('modelValue').proxy_mode).toBe('selected')
    wrapper.unmount()
  })

  it('validates custom timing values and never treats an empty selected list as all proxies', async () => {
    const wrapper = render()
    await wrapper.get('[data-testid="first-serve-ttft_seconds"]').setValue('4')
    expect(wrapper.props('modelValue').ttft_seconds).toBe(4)
    expect(wrapper.vm.validate()).toBe(true)
    await wrapper.get('[data-testid="first-serve-cooldown_seconds"]').setValue('0')
    expect(wrapper.vm.validate()).toBe(false)
    expect(wrapper.get('[role="alert"]').text()).toContain('cooldown_seconds')
    await wrapper.get('[data-testid="first-serve-cooldown_seconds"]').setValue('10')
    await wrapper.get('[data-testid="first-serve-proxy-mode"]').setValue('selected')
    expect(wrapper.vm.validate()).toBe(false)
    await wrapper.get('[data-testid="first-serve-proxy-mode"]').setValue('all')
    expect(wrapper.vm.validate()).toBe(true)
    wrapper.unmount()
  })
})
