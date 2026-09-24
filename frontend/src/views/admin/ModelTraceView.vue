<template>
  <AppLayout>
    <div class="mx-auto flex w-full max-w-7xl flex-col gap-6 p-4 md:p-6">
      <div>
        <h1 class="text-2xl font-semibold text-gray-900 dark:text-white">
          {{ t('admin.accounts.modelTrace.title') }}
        </h1>
        <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
          {{ t('admin.accounts.modelTrace.description') }}
        </p>
      </div>

      <section class="card space-y-5 p-5 md:p-6">
        <div class="max-w-xl">
          <label for="modeltrace-model" class="mb-1 block text-sm font-medium text-gray-700 dark:text-gray-200">
            {{ t('admin.accounts.modelTrace.detectionModel') }}
          </label>
          <select
            id="modeltrace-model"
            v-model="modelChoice"
            class="input w-full"
            :disabled="running || settingsLoading"
          >
            <option value="">
              {{ t('admin.accounts.modelTrace.backendDefaultModel', { model: configuredDefaultModel || t('admin.accounts.modelTrace.platformDefaultModel') }) }}
            </option>
            <option v-for="model in suggestedModels" :key="model" :value="model">{{ model }}</option>
            <option value="custom">{{ t('admin.accounts.modelTrace.customModel') }}</option>
          </select>
          <input
            v-if="modelChoice === 'custom'"
            v-model="customModelID"
            type="text"
            class="input mt-2 w-full font-mono text-sm"
            :placeholder="t('admin.accounts.modelTrace.customModelPlaceholder')"
            :disabled="running"
            maxlength="200"
          />
          <p class="mt-1.5 text-xs text-gray-500 dark:text-gray-400">
            {{ t('admin.accounts.modelTrace.detectionModelHint') }}
          </p>
        </div>

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

        <div v-if="scope === 'selected'" class="max-w-md">
          <label for="modeltrace-account-ids" class="mb-1 block text-sm font-medium text-gray-700 dark:text-gray-200">
            {{ t('admin.accounts.modelTrace.selectedAccounts') }}
          </label>
          <textarea
            id="modeltrace-account-ids"
            v-model="accountIDsText"
            rows="3"
            class="input w-full font-mono text-sm"
            :placeholder="t('admin.accounts.modelTrace.accountIdsPlaceholder')"
            :disabled="running"
          ></textarea>
        </div>

        <div v-if="scope === 'group'" class="max-w-md">
          <label for="modeltrace-group" class="mb-1 block text-sm font-medium text-gray-700 dark:text-gray-200">
            {{ t('admin.accounts.modelTrace.selectGroup') }}
          </label>
          <select id="modeltrace-group" v-model.number="groupID" class="input w-full" :disabled="running || groupsLoading">
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

        <div class="flex justify-end">
          <button class="btn btn-primary" :disabled="!canRun || running || groupsLoading" @click="runDetection">
            <span v-if="running" class="inline-flex items-center gap-2">
              <span class="h-4 w-4 animate-spin rounded-full border-2 border-white/40 border-t-white"></span>
              {{ t('admin.accounts.modelTrace.running') }}
            </span>
            <span v-else>{{ t('admin.accounts.modelTrace.start') }}</span>
          </button>
        </div>
      </section>

      <section v-if="response" class="card space-y-4 p-5 md:p-6" aria-live="polite">
        <div class="grid gap-3 sm:grid-cols-3">
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

        <div class="flex flex-col gap-3 md:flex-row md:items-center md:justify-between">
          <div class="flex min-w-0 flex-1 items-center gap-2">
            <input
              v-model="resultSearch"
              type="search"
              class="input min-w-0 flex-1 md:max-w-md"
              :placeholder="t('admin.accounts.modelTrace.resultSearchPlaceholder')"
              :disabled="running"
            />
            <span class="shrink-0 text-xs text-gray-500 dark:text-gray-400">
              {{ t('admin.accounts.modelTrace.showingResults', { shown: filteredResults.length, total: resultRows.length }) }}
            </span>
          </div>
          <div class="flex shrink-0 items-center gap-2">
            <div class="flex rounded-lg border border-gray-200 p-0.5 dark:border-dark-600" role="group">
              <button
                v-for="filter in resultFilters"
                :key="filter.value"
                type="button"
                class="rounded-md px-2.5 py-1.5 text-xs font-medium transition-colors"
                :class="resultFilter === filter.value
                  ? 'bg-primary-100 text-primary-700 dark:bg-primary-900/40 dark:text-primary-300'
                  : 'text-gray-500 hover:bg-gray-100 dark:text-gray-400 dark:hover:bg-dark-700'"
                :aria-pressed="resultFilter === filter.value"
                @click="resultFilter = filter.value"
              >
                {{ filter.label }}
              </button>
            </div>
            <button type="button" class="btn btn-secondary btn-sm" :disabled="running" @click="clearResults">
              {{ t('admin.accounts.modelTrace.clearResults') }}
            </button>
          </div>
        </div>

        <div class="overflow-x-auto rounded-lg border border-gray-200 dark:border-dark-600">
          <table class="min-w-full divide-y divide-gray-200 text-sm dark:divide-dark-600">
            <thead class="bg-gray-50 text-left text-xs font-medium uppercase tracking-wide text-gray-500 dark:bg-dark-700 dark:text-gray-400">
              <tr>
                <th class="px-3 py-2">{{ t('admin.accounts.modelTrace.account') }}</th>
                <th class="px-3 py-2">{{ t('admin.accounts.modelTrace.model') }}</th>
                <th class="px-3 py-2">{{ t('admin.accounts.modelTrace.prediction') }}</th>
                <th class="px-3 py-2">{{ t('admin.accounts.modelTrace.samples') }}</th>
              </tr>
            </thead>
            <tbody class="divide-y divide-gray-100 dark:divide-dark-700">
              <tr v-for="item in filteredResults" :key="`${item.account_id}-${item.account_name}`" class="align-top">
                <td class="px-3 py-2">
                  <div class="font-medium text-gray-900 dark:text-white">{{ item.account_name || `#${item.account_id}` }}</div>
                  <div class="text-xs text-gray-500 dark:text-gray-400">{{ item.platform }} · #{{ item.account_id }}</div>
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
                  <span v-else class="text-red-600 dark:text-red-400">{{ item.error || t('admin.accounts.modelTrace.failed') }}</span>
                </td>
                <td class="px-3 py-2 text-gray-600 dark:text-gray-300">
                  {{ item.used_outputs }}/3 · {{ t('admin.accounts.modelTrace.attempts', { count: item.attempts }) }}
                  <div v-if="item.errors?.length" class="mt-1 max-w-xs truncate text-xs text-amber-700 dark:text-amber-300" :title="item.errors.join('\n')">
                    {{ item.errors[0] }}
                  </div>
                </td>
              </tr>
              <tr v-if="filteredResults.length === 0">
                <td colspan="4" class="px-3 py-6 text-center text-gray-500 dark:text-gray-400">
                  {{ resultRows.length === 0 ? t('admin.accounts.modelTrace.noAccounts') : t('admin.accounts.modelTrace.noMatchingResults') }}
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import type { ModelTraceDetectionResponse } from '@/api/admin/accounts'
import type { AdminGroup } from '@/types'
import { extractApiErrorMessage } from '@/utils/apiError'
import AppLayout from '@/components/layout/AppLayout.vue'

const { t } = useI18n()
type Scope = 'all' | 'selected' | 'group'

const scope = ref<Scope>('all')
const groupID = ref(0)
const accountIDsText = ref('')
const groups = ref<AdminGroup[]>([])
const groupsLoading = ref(false)
const settingsLoading = ref(false)
const running = ref(false)
const requestError = ref('')
const response = ref<ModelTraceDetectionResponse | null>(null)
const resultSearch = ref('')
const resultFilter = ref<'all' | 'success' | 'failed'>('all')
const modelChoice = ref('')
const customModelID = ref('')
const configuredDefaultModel = ref('')
const suggestedModels = ['gpt-5.4', 'claude-sonnet-4-6', 'gemini-2.0-flash', 'grok-4.5']

const scopeOptions = computed(() => [
  { value: 'all' as const, label: t('admin.accounts.modelTrace.all'), hint: t('admin.accounts.modelTrace.allHint') },
  { value: 'selected' as const, label: t('admin.accounts.modelTrace.selected'), hint: t('admin.accounts.modelTrace.selectedHint', { count: selectedAccountIds.value.length }) },
  { value: 'group' as const, label: t('admin.accounts.modelTrace.group'), hint: t('admin.accounts.modelTrace.groupHint') },
])
const selectedAccountIds = computed(() => {
  const values = accountIDsText.value
    .split(/[\s,，]+/)
    .map((value) => Number(value.trim()))
    .filter((value) => Number.isInteger(value) && value > 0)
  return [...new Set(values)]
})
const canRun = computed(() => {
  if (scope.value === 'selected') return selectedAccountIds.value.length > 0
  if (scope.value === 'group') return groupID.value > 0
  if (modelChoice.value === 'custom') return customModelID.value.trim().length > 0
  return true
})
const selectedModelID = computed(() => {
  if (modelChoice.value === 'custom') return customModelID.value.trim()
  return modelChoice.value.trim()
})
const resultRows = computed(() => (Array.isArray(response.value?.results) ? response.value.results : []))
const predictionRows = computed(() => (Array.isArray(response.value?.predictions) ? response.value.predictions : []))
const resultFilters = computed(() => [
  { value: 'all' as const, label: t('admin.accounts.modelTrace.resultFilterAll') },
  { value: 'success' as const, label: t('admin.accounts.modelTrace.resultFilterSuccess') },
  { value: 'failed' as const, label: t('admin.accounts.modelTrace.resultFilterFailed') },
])
const filteredResults = computed(() => {
  const query = resultSearch.value.trim().toLowerCase()
  return resultRows.value.filter((item) => {
    if (resultFilter.value !== 'all' && item.status !== resultFilter.value) return false
    if (!query) return true
    return [item.account_name, item.platform, item.model_id, item.prediction_name, item.family_name]
      .filter(Boolean)
      .some((value) => String(value).toLowerCase().includes(query))
  })
})

onMounted(async () => {
  groupsLoading.value = true
  settingsLoading.value = true
  const [groupsResult, settingsResult] = await Promise.allSettled([
    adminAPI.groups.getAll(),
    adminAPI.settings.getSettings(),
  ])
  if (groupsResult.status === 'fulfilled') {
    groups.value = groupsResult.value
  } else {
    requestError.value = extractApiErrorMessage(groupsResult.reason, t('admin.accounts.modelTrace.failed'))
  }
  if (settingsResult.status === 'fulfilled') {
    configuredDefaultModel.value = settingsResult.value.modeltrace_default_model?.trim() || ''
  }
  groupsLoading.value = false
  settingsLoading.value = false
})

watch([scope, groupID, accountIDsText, modelChoice, customModelID], () => {
  response.value = null
  requestError.value = ''
  resultSearch.value = ''
  resultFilter.value = 'all'
})

function percentage(value: number) {
  return (value * 100).toFixed(1)
}

function clearResults() {
  response.value = null
  resultSearch.value = ''
  resultFilter.value = 'all'
  requestError.value = ''
}

async function runDetection() {
  if (!canRun.value || running.value) return
  running.value = true
  requestError.value = ''
  response.value = null
  try {
    response.value = await adminAPI.accounts.detectModelTrace({
      scope: scope.value,
      ...(selectedModelID.value ? { model_id: selectedModelID.value } : {}),
      ...(scope.value === 'selected' ? { account_ids: selectedAccountIds.value } : {}),
      ...(scope.value === 'group' ? { group_id: groupID.value } : {}),
    })
  } catch (error) {
    requestError.value = extractApiErrorMessage(error, t('admin.accounts.modelTrace.failed'))
  } finally {
    running.value = false
  }
}
</script>
