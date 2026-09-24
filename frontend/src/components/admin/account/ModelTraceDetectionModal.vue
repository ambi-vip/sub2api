<template>
  <BaseDialog
    :show="show"
    :title="t('admin.accounts.modelTrace.title')"
    width="extra-wide"
    @close="handleClose"
  >
    <div class="space-y-5">
      <p class="text-sm text-gray-600 dark:text-gray-300">
        {{ t('admin.accounts.modelTrace.description') }}
      </p>

      <div class="grid gap-3 md:grid-cols-3">
        <label
          v-for="option in scopeOptions"
          :key="option.value"
          class="flex cursor-pointer gap-3 rounded-lg border p-3 transition-colors"
          :class="scope === option.value
            ? 'border-primary-500 bg-primary-50 dark:bg-primary-900/20'
            : 'border-gray-200 dark:border-dark-600'"
        >
          <input
            v-model="scope"
            type="radio"
            name="modeltrace-scope"
            :value="option.value"
            :disabled="running"
            class="mt-1 accent-primary-600"
          />
          <span>
            <span class="block text-sm font-medium text-gray-900 dark:text-white">{{ option.label }}</span>
            <span class="mt-0.5 block text-xs text-gray-500 dark:text-gray-400">{{ option.hint }}</span>
          </span>
        </label>
      </div>

      <div v-if="scope === 'group'" class="max-w-md">
        <label for="modeltrace-group" class="mb-1 block text-sm font-medium text-gray-700 dark:text-gray-200">
          {{ t('admin.accounts.modelTrace.selectGroup') }}
        </label>
        <select id="modeltrace-group" v-model.number="groupID" class="input w-full" :disabled="running">
          <option :value="0" disabled>{{ t('admin.accounts.modelTrace.selectGroup') }}</option>
          <option v-for="group in groups" :key="group.id" :value="group.id">{{ group.name }}</option>
        </select>
      </div>

      <div class="rounded-lg border border-amber-200 bg-amber-50 px-3 py-2 text-sm text-amber-900 dark:border-amber-700/50 dark:bg-amber-900/20 dark:text-amber-200">
        {{ t('admin.accounts.modelTrace.usageNotice') }}
      </div>

      <div v-if="requestError" role="alert" class="rounded-lg border border-red-200 bg-red-50 px-3 py-2 text-sm text-red-700 dark:border-red-800 dark:bg-red-950/30 dark:text-red-300">
        {{ requestError }}
      </div>

      <section v-if="response" class="space-y-4" aria-live="polite">
        <div class="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
          <div class="rounded-lg bg-gray-50 p-3 dark:bg-dark-700">
            <div class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.accounts.modelTrace.progress') }}</div>
            <div class="mt-1 text-lg font-semibold text-gray-900 dark:text-white">{{ response.completed }} / {{ response.total }}</div>
            <div class="mt-2 h-1.5 overflow-hidden rounded-full bg-gray-200 dark:bg-dark-600">
              <div class="h-full rounded-full bg-primary-600 transition-all" :style="{ width: `${response.progress_percent}%` }"></div>
            </div>
          </div>
          <div class="rounded-lg bg-gray-50 p-3 dark:bg-dark-700">
            <div class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.accounts.modelTrace.queueStatus') }}</div>
            <div class="mt-1 text-sm font-semibold text-gray-900 dark:text-white">{{ t('admin.accounts.modelTrace.queuedCount', { count: response.queued }) }} · {{ t('admin.accounts.modelTrace.runningCount', { count: response.running }) }}</div>
            <div class="mt-1 text-xs text-gray-500 dark:text-gray-400">{{ t('admin.accounts.modelTrace.concurrency', { count: response.concurrency }) }}</div>
          </div>
          <div class="rounded-lg bg-gray-50 p-3 dark:bg-dark-700">
            <div class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.accounts.modelTrace.detected') }}</div>
            <div class="mt-1 text-lg font-semibold text-gray-900 dark:text-white">{{ response.detected }}</div>
          </div>
          <div class="rounded-lg bg-gray-50 p-3 dark:bg-dark-700">
            <div class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.accounts.modelTrace.failed') }}</div>
            <div class="mt-1 text-lg font-semibold text-gray-900 dark:text-white">{{ response.failed }}</div>
          </div>
          <div class="rounded-lg bg-gray-50 p-3 dark:bg-dark-700">
            <div class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.accounts.modelTrace.method') }}</div>
            <div class="mt-1 text-sm font-semibold text-gray-900 dark:text-white">{{ response.method }}</div>
          </div>
        </div>

        <div v-if="predictionRows.length" class="rounded-lg border border-gray-200 dark:border-dark-600">
          <div class="border-b border-gray-200 px-3 py-2 text-sm font-semibold text-gray-900 dark:border-dark-600 dark:text-white">
            {{ t('admin.accounts.modelTrace.summary') }}
          </div>
          <div class="divide-y divide-gray-100 dark:divide-dark-600">
            <div v-for="prediction in predictionRows" :key="prediction.prediction" class="flex items-center justify-between gap-4 px-3 py-2 text-sm">
              <span class="font-medium text-gray-800 dark:text-gray-100">{{ prediction.prediction_name }}</span>
              <span class="text-gray-600 dark:text-gray-300">
                {{ t('admin.accounts.modelTrace.predictionCount', { count: prediction.count }) }} · {{ percentage(prediction.average_probability) }}%
              </span>
            </div>
          </div>
        </div>

        <div class="overflow-x-auto rounded-lg border border-gray-200 dark:border-dark-600">
          <table class="min-w-full divide-y divide-gray-200 text-sm dark:divide-dark-600">
            <thead class="bg-gray-50 text-left text-xs font-medium uppercase tracking-wide text-gray-500 dark:bg-dark-700 dark:text-gray-400">
              <tr>
                <th class="px-3 py-2">{{ t('admin.accounts.modelTrace.account') }}</th>
                <th class="px-3 py-2">{{ t('admin.accounts.modelTrace.status') }}</th>
                <th class="px-3 py-2">{{ t('admin.accounts.modelTrace.model') }}</th>
                <th class="px-3 py-2">{{ t('admin.accounts.modelTrace.prediction') }}</th>
                <th class="px-3 py-2">{{ t('admin.accounts.modelTrace.samples') }}</th>
              </tr>
            </thead>
            <tbody class="divide-y divide-gray-100 dark:divide-dark-700">
              <tr v-for="item in resultRows" :key="`${item.account_id}-${item.account_name}`" class="align-top">
                <td class="px-3 py-2">
                  <div class="font-medium text-gray-900 dark:text-white">{{ item.account_name || `#${item.account_id}` }}</div>
                  <div class="text-xs text-gray-500 dark:text-gray-400">{{ item.platform }} · #{{ item.account_id }}</div>
                </td>
                <td class="px-3 py-2">
                  <span class="inline-flex items-center gap-1.5 rounded-full px-2 py-1 text-xs font-medium" :class="statusClass(item.status)">
                    <span v-if="item.status === 'running'" class="h-2.5 w-2.5 animate-spin rounded-full border-2 border-current border-t-transparent"></span>
                    {{ statusLabel(item.status) }}
                  </span>
                </td>
                <td class="px-3 py-2 text-gray-600 dark:text-gray-300">{{ item.model_id || '—' }}</td>
                <td class="px-3 py-2">
                  <template v-if="item.status === 'success'">
                    <div class="font-medium text-gray-900 dark:text-white">{{ item.prediction_name }} · {{ percentage(item.probability || 0) }}%</div>
                    <div class="text-xs text-gray-500 dark:text-gray-400">
                      {{ t('admin.accounts.modelTrace.family') }}: {{ item.family_name }} · {{ percentage(item.family_probability || 0) }}%
                    </div>
                    <div v-if="item.candidates?.length" class="mt-1 text-xs text-gray-500 dark:text-gray-400">
                      {{ item.candidates.map((candidate) => `${candidate.display_name} ${percentage(candidate.probability)}%`).join(' · ') }}
                    </div>
                  </template>
                  <span v-else-if="item.status === 'failed'" class="text-red-600 dark:text-red-400">{{ item.error || t('admin.accounts.modelTrace.failed') }}</span>
                  <span v-else class="text-gray-500 dark:text-gray-400">{{ statusLabel(item.status) }}</span>
                </td>
                <td class="px-3 py-2 text-gray-600 dark:text-gray-300">
                  <div class="flex items-center justify-between gap-2 text-xs">
                    <span>{{ item.used_outputs }}/3 · {{ t('admin.accounts.modelTrace.attempts', { count: item.attempts }) }}</span>
                    <span>{{ accountProgress(item) }}%</span>
                  </div>
                  <div class="mt-1 h-1.5 min-w-32 overflow-hidden rounded-full bg-gray-200 dark:bg-dark-600">
                    <div class="h-full rounded-full bg-primary-500 transition-all" :style="{ width: `${accountProgress(item)}%` }"></div>
                  </div>
                  <div v-if="item.errors?.length" class="mt-1 max-w-xs truncate text-xs text-amber-700 dark:text-amber-300" :title="item.errors.join('\n')">
                    {{ item.errors[0] }}
                  </div>
                </td>
              </tr>
              <tr v-if="resultRows.length === 0">
                <td colspan="5" class="px-3 py-6 text-center text-gray-500 dark:text-gray-400">{{ t('admin.accounts.modelTrace.noAccounts') }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>
    </div>

    <template #footer>
      <div class="flex flex-wrap items-center justify-between gap-3">
        <span v-if="response" class="text-xs text-gray-500 dark:text-gray-400">
          {{ t('admin.accounts.modelTrace.total', { count: response.total }) }}
        </span>
        <div class="ml-auto flex gap-2">
          <button class="btn btn-secondary" :disabled="running" @click="handleClose">{{ t('common.close') }}</button>
          <button class="btn btn-primary" :disabled="!canRun || running" @click="runDetection">
            <span v-if="running" class="inline-flex items-center gap-2">
              <span class="h-4 w-4 animate-spin rounded-full border-2 border-white/40 border-t-white"></span>
              {{ t('admin.accounts.modelTrace.running') }}
            </span>
            <span v-else>{{ t('admin.accounts.modelTrace.start') }}</span>
          </button>
        </div>
      </div>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import type { ModelTraceAccountResult, ModelTraceDetectionResponse } from '@/api/admin/accounts'
import type { AdminGroup } from '@/types'
import { extractApiErrorMessage } from '@/utils/apiError'
import BaseDialog from '@/components/common/BaseDialog.vue'

const props = defineProps<{
  show: boolean
  groups: AdminGroup[]
  selectedAccountIds: number[]
}>()
const emit = defineEmits<{ (event: 'close'): void }>()
const { t } = useI18n()

type Scope = 'all' | 'selected' | 'group'
const scope = ref<Scope>('all')
const groupID = ref(0)
const running = ref(false)
const requestError = ref('')
const response = ref<ModelTraceDetectionResponse | null>(null)
const pollTimer = ref<ReturnType<typeof setInterval> | null>(null)
const pollInFlight = ref(false)

const predictionRows = computed(() => (Array.isArray(response.value?.predictions) ? response.value.predictions : []))
const resultRows = computed(() => (Array.isArray(response.value?.results) ? response.value.results : []))

const scopeOptions = computed(() => [
  { value: 'all' as const, label: t('admin.accounts.modelTrace.all'), hint: t('admin.accounts.modelTrace.allHint') },
  { value: 'selected' as const, label: t('admin.accounts.modelTrace.selected'), hint: t('admin.accounts.modelTrace.selectedHint', { count: props.selectedAccountIds.length }) },
  { value: 'group' as const, label: t('admin.accounts.modelTrace.group'), hint: t('admin.accounts.modelTrace.groupHint') }
])
const canRun = computed(() => {
  if (scope.value === 'selected') return props.selectedAccountIds.length > 0
  if (scope.value === 'group') return groupID.value > 0
  return true
})

watch(() => props.show, (isOpen) => {
  if (!isOpen) return
  stopPolling()
  scope.value = 'all'
  groupID.value = 0
  requestError.value = ''
  response.value = null
})
watch([scope, groupID], () => {
  stopPolling()
  response.value = null
  requestError.value = ''
})

function handleClose() {
  if (!running.value) emit('close')
}

function percentage(value: number) {
  return (value * 100).toFixed(1)
}

async function runDetection() {
  if (!canRun.value || running.value) return
  running.value = true
  requestError.value = ''
  response.value = null
  stopPolling()
  try {
    response.value = await adminAPI.accounts.startModelTraceDetection({
      scope: scope.value,
      ...(scope.value === 'selected' ? { account_ids: props.selectedAccountIds } : {}),
      ...(scope.value === 'group' ? { group_id: groupID.value } : {})
    })
    if (response.value.status === 'succeeded' || !response.value.job_id) {
      running.value = false
    } else {
      startPolling(response.value.job_id)
    }
  } catch (error) {
    requestError.value = extractApiErrorMessage(error, t('admin.accounts.modelTrace.failed'))
    running.value = false
  }
}

function startPolling(jobID: string) {
  stopPolling()
  pollTimer.value = setInterval(() => void pollJob(jobID), 1000)
  void pollJob(jobID)
}

async function pollJob(jobID: string) {
  if (pollInFlight.value) return
  pollInFlight.value = true
  try {
    response.value = await adminAPI.accounts.getModelTraceDetection(jobID)
    if (response.value.status === 'succeeded') {
      running.value = false
      stopPolling()
    }
  } catch (error) {
    requestError.value = extractApiErrorMessage(error, t('admin.accounts.modelTrace.failed'))
  } finally {
    pollInFlight.value = false
  }
}

function stopPolling() {
  if (pollTimer.value) {
    clearInterval(pollTimer.value)
    pollTimer.value = null
  }
}

onUnmounted(stopPolling)

function statusLabel(status: ModelTraceAccountResult['status']) {
  return t(`admin.accounts.modelTrace.statuses.${status}`)
}

function statusClass(status: ModelTraceAccountResult['status']) {
  if (status === 'success') return 'bg-green-100 text-green-700 dark:bg-green-900/30 dark:text-green-300'
  if (status === 'failed') return 'bg-red-100 text-red-700 dark:bg-red-900/30 dark:text-red-300'
  if (status === 'running') return 'bg-blue-100 text-blue-700 dark:bg-blue-900/30 dark:text-blue-300'
  return 'bg-gray-100 text-gray-600 dark:bg-dark-600 dark:text-gray-300'
}

function accountProgress(item: ModelTraceAccountResult) {
  if (item.status === 'success' || item.status === 'failed') return 100
  return Math.min(99, Math.round((item.used_outputs / 3) * 100))
}
</script>
