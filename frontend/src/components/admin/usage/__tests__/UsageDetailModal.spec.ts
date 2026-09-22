import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'

import UsageDetailModal from '../UsageDetailModal.vue'
import type { UsageLog, UsageLatencyBreakdown } from '@/types'

const messages: Record<string, string> = {
  'usage.latencyDetail.title': 'Request timing details',
  'usage.latencyDetail.loading': 'Loading detailed timing...',
  'usage.latencyDetail.unavailable': 'No stage timing is available',
  'usage.latencyDetail.requestTotal': 'Request total',
  'usage.latencyDetail.clientToGateway': 'Client → sub2api',
  'usage.latencyDetail.gateway': 'Inside sub2api',
  'usage.latencyDetail.gatewayToUpstream': 'sub2api → upstream',
  'usage.latencyDetail.result': 'Request result',
  'usage.latencyDetail.bodyWait': 'Body first-read wait',
  'usage.latencyDetail.bodyRead': 'Body receive',
  'usage.latencyDetail.bodySize': 'Body size',
  'usage.latencyDetail.uploadRate': 'Receive rate',
  'usage.latencyDetail.handlerBodyRead': 'Handler body read',
  'usage.latencyDetail.securityAudit': 'Security audit',
  'usage.latencyDetail.preflight': 'Preflight/auth',
  'usage.latencyDetail.routing': 'Routing',
  'usage.latencyDetail.largeRequestWait': 'Large-request queue',
  'usage.latencyDetail.handlerTotal': 'Handler total',
  'usage.latencyDetail.connectionAcquire': 'Connection acquire',
  'usage.latencyDetail.requestWrite': 'Upstream request write',
  'usage.latencyDetail.firstByteWait': 'Upstream first-byte wait',
  'usage.latencyDetail.responseHeader': 'Upstream response headers',
  'usage.latencyDetail.firstToken': 'First token',
  'usage.latencyDetail.responseStream': 'Response streaming',
  'usage.latencyDetail.outcome': 'Outcome',
  'usage.latencyDetail.upstreamAttempts': 'Upstream attempts',
  'usage.latencyDetail.connectionReused': 'Connection reused',
  'usage.latencyDetail.bodyComplete': 'Body fully received',
  'usage.latencyDetail.streamCompleted': 'Stream completed',
  'usage.latencyDetail.outcomeSuccess': 'Success',
  'usage.latencyFirstToken': 'First',
  'usage.latencyDuration': 'Total',
  'common.yes': 'Yes',
  'common.no': 'No',
}

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => messages[key] ?? key }),
  }
})

const createUsageLog = (latencyBreakdown: UsageLatencyBreakdown | null): UsageLog => ({
  id: 101,
  user_id: 1,
  api_key_id: 2,
  account_id: 3,
  request_id: 'req-latency-detail',
  model: 'gpt-5.6-sol',
  group_id: null,
  subscription_id: null,
  input_tokens: 100,
  output_tokens: 50,
  cache_creation_tokens: 0,
  cache_read_tokens: 0,
  cache_creation_5m_tokens: 0,
  cache_creation_1h_tokens: 0,
  input_cost: 0,
  output_cost: 0,
  cache_creation_cost: 0,
  cache_read_cost: 0,
  total_cost: 0,
  actual_cost: 0,
  rate_multiplier: 1,
  long_context_billing_applied: false,
  billing_type: 0,
  stream: true,
  native_compaction_v2: false,
  duration_ms: 3500,
  first_token_ms: 900,
  latency_breakdown: latencyBreakdown,
  image_count: 0,
  image_size: null,
  image_input_size: null,
  image_output_size: null,
  image_size_source: null,
  image_size_breakdown: null,
  image_input_tokens: 0,
  image_input_cost: 0,
  image_output_tokens: 0,
  image_output_cost: 0,
  user_agent: null,
  cache_ttl_overridden: false,
  created_at: '2026-09-22T08:00:00Z',
})

const mountModal = (detail: UsageLog) => mount(UsageDetailModal, {
  props: { show: true, detail },
  global: {
    stubs: {
      BaseDialog: {
        props: ['show'],
        template: '<div v-if="show"><slot /></div>',
      },
    },
  },
})

describe('UsageDetailModal', () => {
  it('renders the full stage timing breakdown and request result', () => {
    const wrapper = mountModal(createUsageLog({
      request_total_ms: 3500,
      handler_total_ms: 3100,
      ingress_body_wait_ms: 15,
      ingress_body_read_ms: 120,
      ingress_body_bytes: 2 * 1024 * 1024,
      ingress_body_mib_per_second: 16.25,
      handler_body_read_ms: 30,
      security_audit_ms: 8,
      preflight_ms: 45,
      routing_ms: 12,
      large_request_wait_ms: 250,
      upstream_connection_acquire_ms: 20,
      upstream_request_write_ms: 75,
      upstream_first_byte_wait_ms: 500,
      upstream_response_header_ms: 510,
      time_to_first_token_ms: 900,
      response_stream_ms: 2000,
      upstream_attempts: 2,
      upstream_connection_reused: true,
      ingress_body_complete: true,
      stream_completed: false,
      outcome: 'success',
    }))

    const text = wrapper.text()
    expect(text).toContain('Client → sub2api')
    expect(text).toContain('Inside sub2api')
    expect(text).toContain('sub2api → upstream')
    expect(text).toContain('Request result')
    expect(text).toContain('2.00 MiB')
    expect(text).toContain('16.25 MiB/s')
    expect(text).toContain('Large-request queue')
    expect(text).toContain('250ms')
    expect(text).toContain('Upstream first-byte wait')
    expect(text).toContain('500ms')
    expect(text).toContain('Response streaming')
    expect(text).toContain('2.00s')
    expect(text).toContain('Success')
    expect(text).toContain('Yes')
    expect(text).toContain('No')
  })

  it('keeps first-token and total duration for records without stage timing', () => {
    const wrapper = mountModal(createUsageLog(null))

    const text = wrapper.text()
    expect(text).toContain('First')
    expect(text).toContain('900ms')
    expect(text).toContain('Total')
    expect(text).toContain('3.50s')
    expect(text).toContain('No stage timing is available')
    expect(text).not.toContain('Client → sub2api')
  })
})
