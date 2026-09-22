<template>
  <BaseDialog
    :show="show"
    :title="t('usage.latencyDetail.title')"
    width="wide"
    :close-on-click-outside="true"
    @close="emit('close')"
  >
    <div v-if="detail" class="space-y-5">
      <div class="flex flex-wrap items-start justify-between gap-3 rounded-xl bg-gray-50 p-4 dark:bg-dark-800/70">
        <div class="min-w-0">
          <div class="break-all text-sm font-semibold text-gray-900 dark:text-white">{{ detail.model }}</div>
          <div class="mt-1 break-all font-mono text-xs text-gray-500 dark:text-gray-400">{{ detail.request_id || '-' }}</div>
        </div>
        <div class="text-xs text-gray-500 dark:text-gray-400">{{ formatDateTime(detail.created_at) }}</div>
      </div>

      <div class="grid grid-cols-2 gap-3 sm:grid-cols-3">
        <SummaryCard :label="t('usage.latencyFirstToken')" :value="formatDuration(detail.first_token_ms)" />
        <SummaryCard :label="t('usage.latencyDuration')" :value="formatDuration(detail.duration_ms)" />
        <SummaryCard
          :label="t('usage.latencyDetail.requestTotal')"
          :value="formatDuration(detail.latency_breakdown?.request_total_ms)"
        />
      </div>

      <div v-if="loading" class="flex items-center justify-center gap-2 py-8 text-sm text-gray-500 dark:text-gray-400">
        <span class="h-4 w-4 animate-spin rounded-full border-2 border-primary-500 border-t-transparent"></span>
        {{ t('usage.latencyDetail.loading') }}
      </div>

      <template v-else-if="hasBreakdown">
        <div class="grid gap-4 lg:grid-cols-3">
          <section v-for="group in timingGroups" :key="group.title" class="rounded-xl border border-gray-200 p-4 dark:border-dark-700">
            <h4 class="mb-3 text-sm font-semibold text-gray-900 dark:text-white">{{ group.title }}</h4>
            <dl class="space-y-2.5">
              <div v-for="row in group.rows" :key="row.label" class="flex items-start justify-between gap-4 text-sm">
                <dt class="text-gray-500 dark:text-gray-400">{{ row.label }}</dt>
                <dd class="text-right font-medium tabular-nums text-gray-900 dark:text-white">{{ row.value }}</dd>
              </div>
            </dl>
          </section>
        </div>

        <section class="rounded-xl border border-gray-200 p-4 dark:border-dark-700">
          <h4 class="mb-3 text-sm font-semibold text-gray-900 dark:text-white">{{ t('usage.latencyDetail.result') }}</h4>
          <dl class="grid gap-x-8 gap-y-2.5 sm:grid-cols-2">
            <div v-for="row in resultRows" :key="row.label" class="flex items-start justify-between gap-4 text-sm">
              <dt class="text-gray-500 dark:text-gray-400">{{ row.label }}</dt>
              <dd class="text-right font-medium text-gray-900 dark:text-white">{{ row.value }}</dd>
            </div>
          </dl>
        </section>
      </template>

      <div v-else class="rounded-xl border border-dashed border-gray-300 px-4 py-8 text-center text-sm text-gray-500 dark:border-dark-600 dark:text-gray-400">
        {{ t('usage.latencyDetail.unavailable') }}
      </div>
    </div>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import SummaryCard from '@/components/admin/usage/UsageDetailSummaryCard.vue'
import { formatDateTime } from '@/utils/format'
import type { UsageLatencyBreakdown, UsageLog } from '@/types'

const props = defineProps<{
  show: boolean
  detail: UsageLog | null
  loading?: boolean
}>()

const emit = defineEmits<{ close: [] }>()
const { t } = useI18n()

const breakdown = computed(() => props.detail?.latency_breakdown ?? null)
const hasBreakdown = computed(() => breakdown.value != null && Object.keys(breakdown.value).length > 0)

const formatDuration = (ms: number | null | undefined): string => {
  if (ms == null) return '-'
  if (ms < 1000) return `${ms}ms`
  if (ms < 60_000) return `${(ms / 1000).toFixed(2)}s`
  const seconds = Math.round(ms / 1000)
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m ${seconds % 60}s`
  return `${Math.floor(seconds / 3600)}h ${Math.floor((seconds % 3600) / 60)}m`
}

const formatBytes = (bytes: number | undefined): string => {
  if (bytes == null) return '-'
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KiB`
  return `${(bytes / (1024 * 1024)).toFixed(2)} MiB`
}

const formatBool = (value: boolean | undefined): string => {
  if (value == null) return '-'
  return value ? t('common.yes') : t('common.no')
}

const formatOutcome = (value: string | undefined): string => {
  switch (value) {
    case 'success': return t('usage.latencyDetail.outcomeSuccess')
    case 'partial_error': return t('usage.latencyDetail.outcomePartialError')
    case 'client_disconnected': return t('usage.latencyDetail.outcomeClientDisconnected')
    case 'upstream_error': return t('usage.latencyDetail.outcomeUpstreamError')
    case 'large_request_limited': return t('usage.latencyDetail.outcomeLargeRequestLimited')
    case 'routing_error': return t('usage.latencyDetail.outcomeRoutingError')
    case 'request_rejected': return t('usage.latencyDetail.outcomeRequestRejected')
    default: return value || '-'
  }
}

type DetailRow = { label: string; value: string }
const msRow = (label: string, value: number | undefined): DetailRow => ({ label, value: formatDuration(value) })

const timingGroups = computed(() => {
  const b: UsageLatencyBreakdown = breakdown.value || {}
  return [
    {
      title: t('usage.latencyDetail.clientToGateway'),
      rows: [
        msRow(t('usage.latencyDetail.bodyWait'), b.ingress_body_wait_ms),
        msRow(t('usage.latencyDetail.bodyRead'), b.ingress_body_read_ms),
        { label: t('usage.latencyDetail.bodySize'), value: formatBytes(b.ingress_body_bytes) },
        { label: t('usage.latencyDetail.uploadRate'), value: b.ingress_body_mib_per_second == null ? '-' : `${b.ingress_body_mib_per_second.toFixed(2)} MiB/s` },
      ],
    },
    {
      title: t('usage.latencyDetail.gateway'),
      rows: [
        msRow(t('usage.latencyDetail.handlerBodyRead'), b.handler_body_read_ms),
        msRow(t('usage.latencyDetail.securityAudit'), b.security_audit_ms),
        msRow(t('usage.latencyDetail.preflight'), b.preflight_ms),
        msRow(t('usage.latencyDetail.routing'), b.routing_ms),
        msRow(t('usage.latencyDetail.largeRequestWait'), b.large_request_wait_ms),
        msRow(t('usage.latencyDetail.handlerTotal'), b.handler_total_ms),
      ],
    },
    {
      title: t('usage.latencyDetail.gatewayToUpstream'),
      rows: [
        msRow(t('usage.latencyDetail.connectionAcquire'), b.upstream_connection_acquire_ms),
        msRow(t('usage.latencyDetail.requestWrite'), b.upstream_request_write_ms),
        msRow(t('usage.latencyDetail.firstByteWait'), b.upstream_first_byte_wait_ms),
        msRow(t('usage.latencyDetail.responseHeader'), b.upstream_response_header_ms),
        msRow(t('usage.latencyDetail.firstToken'), b.time_to_first_token_ms),
        msRow(t('usage.latencyDetail.responseStream'), b.response_stream_ms),
      ],
    },
  ]
})

const resultRows = computed<DetailRow[]>(() => {
  const b = breakdown.value || {}
  return [
    { label: t('usage.latencyDetail.outcome'), value: formatOutcome(b.outcome) },
    { label: t('usage.latencyDetail.upstreamAttempts'), value: b.upstream_attempts == null ? '-' : String(b.upstream_attempts) },
    { label: t('usage.latencyDetail.connectionReused'), value: formatBool(b.upstream_connection_reused) },
    { label: t('usage.latencyDetail.bodyComplete'), value: formatBool(b.ingress_body_complete) },
    { label: t('usage.latencyDetail.streamCompleted'), value: formatBool(b.stream_completed) },
  ]
})
</script>
