import { afterEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

const { fetchCodexModelsManifestMock, saveAsMock } = vi.hoisted(() => ({
  fetchCodexModelsManifestMock: vi.fn(),
  saveAsMock: vi.fn()
}))

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string, params?: Record<string, unknown>) => params
      ? `${key}:${JSON.stringify(params)}`
      : key
  })
}))

vi.mock('file-saver', () => ({
  saveAs: saveAsMock
}))

vi.mock('@/api/codex', () => ({
  fetchCodexModelsManifest: fetchCodexModelsManifestMock
}))

import CodexQuickConfigModal from '../CodexQuickConfigModal.vue'

function mountModal() {
  return mount(CodexQuickConfigModal, {
    props: {
      show: true,
      apiKey: 'sk-modal-test',
      baseUrl: 'https://example.com/v1',
      platform: 'openai'
    },
    global: {
      stubs: {
        BaseDialog: {
          template: '<div><slot /><slot name="footer" /></div>'
        },
        Icon: {
          template: '<span />'
        }
      }
    }
  })
}

describe('CodexQuickConfigModal', () => {
  afterEach(() => {
    fetchCodexModelsManifestMock.mockReset()
    saveAsMock.mockReset()
  })

  it('leaves model catalog import disabled by default', () => {
    const wrapper = mountModal()

    expect((wrapper.get('[data-testid="quick-config-import-catalog"]').element as HTMLInputElement).checked).toBe(false)
    expect(fetchCodexModelsManifestMock).not.toHaveBeenCalled()
    expect(wrapper.get('[data-testid="quick-config-copy"]').attributes('disabled')).toBeUndefined()
    expect(wrapper.get('[data-testid="quick-config-download"]').attributes('disabled')).toBeUndefined()
    expect(wrapper.find('pre code').text()).not.toContain('codex-models.json')
  })

  it('loads the current account catalog and includes it in the generated script', async () => {
    fetchCodexModelsManifestMock.mockResolvedValue({
      content: '{\n  "models": [{"slug": "gpt-test"}]\n}',
      modelCount: 1
    })
    const wrapper = mountModal()

    await wrapper.get('[data-testid="quick-config-import-catalog"]').setValue(true)
    expect(fetchCodexModelsManifestMock).toHaveBeenCalledWith(
      'https://example.com/v1',
      'sk-modal-test',
      expect.any(AbortSignal)
    )
    await flushPromises()

    expect(wrapper.get('[data-testid="quick-config-catalog-ready"]').text()).toContain('1')
    const script = wrapper.find('pre code').text()
    const configPayload = script.match(/PAYLOAD='([^']+)'/)?.[1]
    expect(configPayload).toBeDefined()
    expect(decodeBase64Utf8(configPayload!)).toContain('model_catalog_json')
    expect(script).toContain('codex-models.json')
    expect(wrapper.get('[data-testid="quick-config-download"]').attributes('disabled')).toBeUndefined()
  })

  it('disables export while catalog loading and shows an error on failure', async () => {
    let resolveRequest: (value: unknown) => void = () => undefined
    fetchCodexModelsManifestMock.mockImplementation(() => new Promise((resolve) => {
      resolveRequest = resolve
    }))
    const wrapper = mountModal()

    await wrapper.get('[data-testid="quick-config-import-catalog"]').setValue(true)
    expect(wrapper.get('[data-testid="quick-config-catalog-loading"]').exists()).toBe(true)
    expect(wrapper.get('[data-testid="quick-config-copy"]').attributes('disabled')).toBeDefined()
    expect(wrapper.get('[data-testid="quick-config-download"]').attributes('disabled')).toBeDefined()

    resolveRequest({ content: '{}', modelCount: 0 })
    await flushPromises()
    expect(wrapper.get('[data-testid="quick-config-catalog-ready"]').exists()).toBe(true)

    fetchCodexModelsManifestMock.mockRejectedValueOnce(new Error('request failed'))
    await wrapper.get('[data-testid="quick-config-import-catalog"]').setValue(false)
    await wrapper.get('[data-testid="quick-config-import-catalog"]').setValue(true)
    await flushPromises()
    expect(wrapper.get('[data-testid="quick-config-catalog-error"]').exists()).toBe(true)
    expect(wrapper.get('[data-testid="quick-config-download"]').attributes('disabled')).toBeDefined()
  })
})

function decodeBase64Utf8(value: string): string {
  const bytes = atob(value)
    .split('')
    .map((character) => character.charCodeAt(0))
  return new TextDecoder().decode(new Uint8Array(bytes))
}
