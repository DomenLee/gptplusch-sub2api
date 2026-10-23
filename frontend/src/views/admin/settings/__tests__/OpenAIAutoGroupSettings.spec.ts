import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import OpenAIAutoGroupSettings from '../OpenAIAutoGroupSettings.vue'

const { loadSettings, saveSettings } = vi.hoisted(() => ({ loadSettings: vi.fn(), saveSettings: vi.fn() }))

vi.mock('@/api/admin', () => ({ adminAPI: { settings: { getOpenAIAutoGroupSettings: loadSettings, updateOpenAIAutoGroupSettings: saveSettings } } }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))

describe('OpenAIAutoGroupSettings', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    loadSettings.mockResolvedValue({ default_high_rate: false, enabled: false, window_minutes: 5, failure_threshold: 3 })
    saveSettings.mockImplementation(async settings => settings)
  })

  it('loads and saves configurable thresholds', async () => {
    const wrapper = mount(OpenAIAutoGroupSettings)
    await flushPromises()
    await wrapper.get('#auto-group-window').setValue(10)
    await wrapper.get('#auto-group-threshold').setValue(6)
    await wrapper.get('button.btn-primary').trigger('click')
    await flushPromises()
    expect(saveSettings).toHaveBeenCalledWith({ default_high_rate: false, enabled: false, window_minutes: 10, failure_threshold: 6 })
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
