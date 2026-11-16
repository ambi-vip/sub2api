import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import type { Account } from '@/types'
import CPABPSAccountSettings from '../CPABPSAccountSettings.vue'
import { NEW_FEATURE_NOTICES } from '@/constants/newFeatureNotices'

const mocks = vi.hoisted(() => ({ preview: vi.fn(), models: vi.fn() }))
vi.mock('../AccountTestModal.vue', () => ({ default: { props: ['account'], template: '<div data-testid="test-dialog" />' } }))
vi.mock('@/api/admin/accounts', () => ({ syncUpstreamModelsPreview: mocks.preview, getAvailableModels: mocks.models }))
vi.mock('vue-i18n', async () => ({ ...(await vi.importActual<typeof import('vue-i18n')>('vue-i18n')), useI18n: () => ({ t: (key: string) => key }) }))
enableAutoUnmount(afterEach)

const preview = { platform: 'openai', base_url: 'http://cpa.test/v1', api_key: 'synthetic' }
const saved = { id: 71, platform: 'openai', type: 'apikey', extra: { openai_cpa_bps: true } } as unknown as Account
function create(props = {}) {
  return mount(CPABPSAccountSettings, {
    props: { modelValue: true, ...props },
    global: { stubs: {
      NewFeatureBadge: { props: ['noticeId'], template: '<span data-testid="notice">{{ noticeId }}</span>' },
      AccountTestModal: { props: ['account'], template: '<div data-testid="test-dialog" />' },
    } },
  })
}

beforeEach(() => { vi.resetAllMocks() })

describe('CPA-BPS settings', () => {
  it('uses the registered notice and requires an explicitly saved CPA account for inference', () => {
    const wrapper = create({ preview })
    expect(wrapper.get('[data-testid="notice"]').text()).toBe(NEW_FEATURE_NOTICES.cpaBPSChannel)
    expect(wrapper.get<HTMLButtonElement>('[data-testid="cpa-bps-test"]').element.disabled).toBe(true)
    expect(mocks.preview).not.toHaveBeenCalled()
    expect(mocks.models).not.toHaveBeenCalled()
  })

  it('queries the exact preview instance with CPA mode and preserves an empty list', async () => {
    mocks.preview.mockResolvedValue({ models: [] })
    const wrapper = create({ preview })
    await wrapper.get('[data-testid="cpa-bps-models"]').trigger('click')
    await flushPromises()
    expect(mocks.preview).toHaveBeenCalledWith({ ...preview, openai_cpa_bps: true })
    expect(wrapper.get('[data-testid="cpa-bps-model-list"]').text()).toContain('cpaBPSEmptyModels')
    expect(wrapper.text()).not.toContain('gpt-')
  })

  it('shows only returned models and opens the saved-account test on explicit click', async () => {
    mocks.models.mockResolvedValue([{ id: 'actual-cpa-model' }])
    const wrapper = create({ account: saved })
    await wrapper.get('[data-testid="cpa-bps-models"]').trigger('click')
    await flushPromises()
    expect(mocks.models).toHaveBeenCalledWith(71)
    expect(wrapper.get('[data-testid="cpa-bps-model-list"]').text()).toBe('actual-cpa-model')
    expect(wrapper.find('[data-testid="test-dialog"]').exists()).toBe(false)
    await wrapper.get('[data-testid="cpa-bps-test"]').trigger('click')
    expect(wrapper.find('[data-testid="test-dialog"]').exists()).toBe(true)
  })

  it('reports discovery errors without substituting built-in models', async () => {
    mocks.preview.mockRejectedValue(new Error('network error'))
    const wrapper = create({ preview })
    await wrapper.get('[data-testid="cpa-bps-models"]').trigger('click')
    await flushPromises()
    expect(wrapper.get('[role="alert"]').text()).toBe('admin.accounts.modelsLoadFailed')
    expect(wrapper.find('[data-testid="cpa-bps-model-list"]').exists()).toBe(false)
  })

  it('discards a stale response after changing the instance or disabling the mode', async () => {
    let resolve!: (value: { models: string[] }) => void
    mocks.preview.mockReturnValue(new Promise(r => { resolve = r }))
    const wrapper = create({ preview })
    await wrapper.get('[data-testid="cpa-bps-models"]').trigger('click')
    await wrapper.setProps({ preview: { ...preview, base_url: 'http://other.test/v1' } })
    resolve({ models: ['stale-model'] })
    await flushPromises()
    expect(wrapper.find('[data-testid="cpa-bps-model-list"]').exists()).toBe(false)
    await wrapper.get('[data-testid="cpa-bps-toggle"]').setValue(false)
    expect(wrapper.emitted('update:modelValue')).toEqual([[false]])
  })
})
