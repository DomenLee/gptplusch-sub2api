import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import OpenAIAutoGroupSettings from '../OpenAIAutoGroupSettings.vue'
import Select from '@/components/common/Select.vue'

const { loadSettings, saveSettings, loadGroups } = vi.hoisted(() => ({ loadSettings: vi.fn(), saveSettings: vi.fn(), loadGroups: vi.fn() }))

vi.mock('@/api/admin', () => ({ adminAPI: { groups: { getAllIncludingInactive: loadGroups }, settings: { getOpenAIAutoGroupSettings: loadSettings, updateOpenAIAutoGroupSettings: saveSettings } } }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))

describe('OpenAIAutoGroupSettings', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    loadGroups.mockResolvedValue([
      { id: 7, name: 'VIP', platform: 'openai', status: 'active', rate_multiplier: 3 },
      { id: 8, name: 'Backup', platform: 'openai', status: 'inactive', rate_multiplier: 1 },
    ])
    loadSettings.mockResolvedValue({ default_high_rate: false, enabled: false, window_minutes: 5, failure_threshold: 3 })
    saveSettings.mockImplementation(async settings => settings)
  })

  it('adds and removes skipped groups and saves them with the policy', async () => {
    const wrapper = mount(OpenAIAutoGroupSettings)
    await flushPromises()
    const selector = wrapper.getComponent(Select)
    expect(selector.props('options')).toEqual(expect.arrayContaining([expect.objectContaining({ value: 8 })]))
    selector.vm.$emit('update:modelValue', 7)
    selector.vm.$emit('update:modelValue', 7)
    await flushPromises()
    expect(wrapper.findAll('[data-group-id="7"]')).toHaveLength(1)
    await wrapper.get('button.btn-primary').trigger('click')
    await flushPromises()
    expect(saveSettings).toHaveBeenLastCalledWith(expect.objectContaining({ skip_group_ids: [7] }))
    await wrapper.get('[data-group-id="7"] button').trigger('click')
    await wrapper.get('button.btn-primary').trigger('click')
    await flushPromises()
    expect(saveSettings).toHaveBeenLastCalledWith(expect.objectContaining({ skip_group_ids: [] }))
    wrapper.unmount()
  })

  it('preserves missing group IDs when the group list fails and permits retry', async () => {
    loadSettings.mockResolvedValueOnce({ skip_group_ids: [99] })
    loadGroups.mockRejectedValueOnce(new Error('groups unavailable'))
    const wrapper = mount(OpenAIAutoGroupSettings)
    await flushPromises()
    expect(wrapper.get('[role="alert"]').text()).toContain('skipGroupsLoadFailed')
    expect(wrapper.find('[data-group-id="99"]').exists()).toBe(true)
    await wrapper.get('button.btn-primary').trigger('click')
    await flushPromises()
    expect(saveSettings).toHaveBeenLastCalledWith(expect.objectContaining({ skip_group_ids: [99] }))
    await wrapper.get('[role="alert"] button').trigger('click')
    await flushPromises()
    expect(wrapper.find('[role="alert"]').exists()).toBe(false)
    expect(wrapper.find('[data-group-id="99"]').exists()).toBe(true)
    wrapper.unmount()
  })

  it('loads and saves configurable thresholds', async () => {
    const wrapper = mount(OpenAIAutoGroupSettings)
    await flushPromises()
    await wrapper.get('#auto-group-window').setValue(10)
    await wrapper.get('#auto-group-threshold').setValue(6)
    await wrapper.get('button.btn-primary').trigger('click')
    await flushPromises()
    expect(saveSettings).toHaveBeenCalledWith(expect.objectContaining({ default_high_rate: false, enabled: false, failure_window: 10, failure_threshold: 6, latency_window: 10, slow_threshold: 3, recovery_samples: 30, protection_minutes: 30 }))
    expect(wrapper.get('[role="status"]').text()).toContain('saved')
    wrapper.unmount()
  })

  it('keeps invalid settings unsaved and shows an inline error', async () => {
    const wrapper = mount(OpenAIAutoGroupSettings)
    await flushPromises()
    await wrapper.get('#auto-group-threshold').setValue(1.5)
    await wrapper.get('button.btn-primary').trigger('click')
    expect(saveSettings).not.toHaveBeenCalled()
    expect(wrapper.get('[role="alert"]').text()).toContain('invalid')
    wrapper.unmount()
  })

  it('rejects thresholds larger than their request windows', async () => {
    const wrapper = mount(OpenAIAutoGroupSettings)
    await flushPromises()
    await wrapper.get('#auto-group-slow-threshold').setValue(11)
    await wrapper.get('button.btn-primary').trigger('click')
    expect(saveSettings).not.toHaveBeenCalled()
    expect(wrapper.get('[role="alert"]').text()).toContain('invalid')
    wrapper.unmount()
  })

  it('saves request rules together and supports observation mode', async () => {
    const wrapper = mount(OpenAIAutoGroupSettings)
    await flushPromises()
    await wrapper.get('#auto-group-protection').setValue(0)
    await wrapper.get('[data-testid="auto-group-latencyEnabled"]').trigger('click')
    await wrapper.get('[data-testid="auto-group-recoveryEnabled"]').trigger('click')
    await wrapper.get('[data-testid="auto-group-observe"]').trigger('click')
    await wrapper.get('button.btn-primary').trigger('click')
    await flushPromises()
    expect(saveSettings).toHaveBeenCalledWith(expect.objectContaining({ latency_enabled: true, recovery_enabled: true, observe_only: true, protection_minutes: 0 }))
    wrapper.unmount()
  })

  it('shows load failures and allows retry instead of saving defaults', async () => {
    loadSettings.mockRejectedValueOnce(new Error('network unavailable'))
    const wrapper = mount(OpenAIAutoGroupSettings)
    await flushPromises()
    expect(wrapper.get('[role="alert"]').text()).toContain('loadFailed')
    expect(wrapper.find('button.btn-primary').exists()).toBe(false)
    await wrapper.get('[role="alert"] button').trigger('click')
    await flushPromises()
    expect(wrapper.find('#auto-group-window').exists()).toBe(true)
    wrapper.unmount()
  })

  it('shows save failures in the visible card and preserves edited values', async () => {
    saveSettings.mockRejectedValueOnce(new Error('database unavailable'))
    const wrapper = mount(OpenAIAutoGroupSettings)
    await flushPromises()
    await wrapper.get('#auto-group-threshold').setValue(8)
    await wrapper.get('button.btn-primary').trigger('click')
    await flushPromises()
    expect(wrapper.get('[role="alert"]').text()).toContain('saveFailed')
    expect((wrapper.get('#auto-group-threshold').element as HTMLInputElement).value).toBe('8')
    wrapper.unmount()
  })
})
