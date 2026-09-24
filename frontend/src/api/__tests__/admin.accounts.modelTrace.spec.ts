import { beforeEach, describe, expect, it, vi } from 'vitest'

const { post } = vi.hoisted(() => ({
  post: vi.fn()
}))

vi.mock('@/api/client', () => ({
  apiClient: { post }
}))

import { detectModelTrace } from '@/api/admin/accounts'

describe('admin ModelTrace API', () => {
  beforeEach(() => {
    post.mockReset()
  })

  it('normalizes nullable response arrays for the page renderer', async () => {
    post.mockResolvedValueOnce({
      data: {
        scope: 'all',
        total: 1,
        detected: 0,
        failed: 1,
        method: 'test',
        predictions: null,
        results: [
          {
            account_id: 7,
            account_name: 'test',
            platform: 'openai',
            account_type: 'oauth',
            status: 'failed',
            candidates: null,
            errors: null,
          },
        ],
      },
    })

    await expect(detectModelTrace({ scope: 'all' })).resolves.toMatchObject({
      predictions: [],
      results: [{ candidates: [], errors: [] }],
    })
  })

  it('passes an explicitly selected detection model to the backend', async () => {
    post.mockResolvedValueOnce({
      data: {
        scope: 'all',
        total: 0,
        detected: 0,
        failed: 0,
        method: 'test',
        predictions: [],
        results: [],
      },
    })

    await detectModelTrace({ scope: 'all', model_id: 'gpt-5.4' })

    expect(post).toHaveBeenCalledWith(
      '/admin/accounts/modeltrace/detect',
      { scope: 'all', model_id: 'gpt-5.4' },
      { timeout: 0 },
    )
  })
})
