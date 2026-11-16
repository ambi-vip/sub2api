<template>
  <section class="space-y-3 border-t border-gray-200 pt-4 dark:border-dark-600" data-testid="cpa-bps-settings">
    <div class="flex items-center gap-2">
      <label class="flex items-center gap-2">
        <input :checked="modelValue" type="checkbox" data-testid="cpa-bps-toggle"
          @change="emit('update:modelValue', ($event.target as HTMLInputElement).checked)" />
        <span>CPA-BPS</span>
      </label>
      <NewFeatureBadge :notice-id="NEW_FEATURE_NOTICES.cpaBPSChannel" :user-id="userId" />
    </div>
    <template v-if="modelValue">
      <p class="input-hint">{{ t('admin.accounts.openai.cpaBPSDesc') }}</p>
      <p class="text-sm text-amber-700 dark:text-amber-300">{{ t('admin.accounts.openai.cpaBPSHistory') }}</p>
      <div class="flex flex-wrap gap-2">
        <button type="button" class="btn btn-secondary btn-sm" :disabled="loading || (!preview && !savedCPA)"
          data-testid="cpa-bps-models" @click="fetchModels">{{ t('admin.accounts.openai.cpaBPSModels') }}</button>
        <button type="button" class="btn btn-secondary btn-sm" :disabled="!savedCPA"
          data-testid="cpa-bps-test" @click="showTest = true">{{ t('admin.accounts.openai.cpaBPSTest') }}</button>
      </div>
      <p class="input-hint">{{ t('admin.accounts.openai.cpaBPSSavedTest') }}</p>
      <p v-if="error" role="alert" class="text-sm text-red-600">{{ error }}</p>
      <p v-if="models !== null" data-testid="cpa-bps-model-list" class="break-words text-sm text-gray-600 dark:text-gray-300">
        {{ models.length ? models.join(', ') : t('admin.accounts.openai.cpaBPSEmptyModels') }}
      </p>
      <AccountTestModal v-if="showTest && account" :show="showTest" :account="account" @close="showTest = false" />
    </template>
  </section>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import type { Account } from '@/types'
import { getAvailableModels, syncUpstreamModelsPreview, type SyncUpstreamPreviewParams } from '@/api/admin/accounts'
import NewFeatureBadge from '@/components/common/NewFeatureBadge.vue'
import { NEW_FEATURE_NOTICES } from '@/constants/newFeatureNotices'
import AccountTestModal from './AccountTestModal.vue'

const props = defineProps<{
  modelValue: boolean
  userId?: number | null
  account?: Account | null
  preview?: SyncUpstreamPreviewParams
}>()
const emit = defineEmits<{ 'update:modelValue': [value: boolean] }>()
const { t } = useI18n()
const savedCPA = computed(() => props.account?.extra?.openai_cpa_bps === true)
const loading = ref(false)
const error = ref('')
const models = ref<string[] | null>(null)
const showTest = ref(false)
let generation = 0
watch(() => [props.modelValue, props.account?.id, props.preview], () => {
  generation++
  models.value = null
  error.value = ''
  loading.value = false
  showTest.value = false
})
async function fetchModels() {
  const current = ++generation
  loading.value = true
  error.value = ''
  models.value = null
  try {
    const found = props.preview
      ? (await syncUpstreamModelsPreview({ ...props.preview, openai_cpa_bps: true })).models
      : (await getAvailableModels(props.account!.id)).map(model => model.id)
    if (current === generation) models.value = found
  } catch {
    if (current === generation) error.value = t('admin.accounts.modelsLoadFailed')
  } finally {
    if (current === generation) loading.value = false
  }
}
</script>
