<template>
  <BaseDialog
    :show="show"
    :title="t('keys.quickConfigureModal.title')"
    width="wide"
    @close="emit('close')"
  >
    <div class="space-y-4">
      <div class="flex items-start gap-3 rounded-lg border border-amber-200 bg-amber-50 p-3 dark:border-amber-800 dark:bg-amber-950/30">
        <Icon name="exclamationCircle" size="md" class="mt-0.5 flex-shrink-0 text-amber-500" />
        <p class="text-sm text-amber-800 dark:text-amber-200">
          {{ t('keys.quickConfigureModal.warning') }}
        </p>
      </div>

      <p class="text-sm text-gray-600 dark:text-gray-400">
        {{ t('keys.quickConfigureModal.description') }}
      </p>

      <div class="grid grid-cols-2 gap-1 rounded-lg bg-gray-100 p-1 dark:bg-dark-700" role="tablist">
        <button
          type="button"
          role="tab"
          :aria-selected="activePlatform === 'unix'"
          data-testid="quick-config-unix-tab"
          :class="tabClass(activePlatform === 'unix')"
          @click="activePlatform = 'unix'"
        >
          {{ t('keys.quickConfigureModal.macLinux') }}
        </button>
        <button
          type="button"
          role="tab"
          :aria-selected="activePlatform === 'windows'"
          data-testid="quick-config-windows-tab"
          :class="tabClass(activePlatform === 'windows')"
          @click="activePlatform = 'windows'"
        >
          {{ t('keys.quickConfigureModal.windows') }}
        </button>
      </div>

      <div class="overflow-hidden rounded-lg bg-gray-900 dark:bg-dark-900">
        <div class="flex items-center justify-between border-b border-gray-700 bg-gray-800 px-4 py-2 dark:bg-dark-800">
          <span class="truncate font-mono text-xs text-gray-400">{{ fileName }}</span>
          <div class="flex items-center gap-2">
            <button
              type="button"
              class="flex items-center gap-1.5 rounded-lg bg-gray-700 px-2.5 py-1 text-xs font-medium text-gray-300 transition-colors hover:bg-gray-600 hover:text-white"
              data-testid="quick-config-copy"
              @click="copyScript"
            >
              <Icon :name="copied ? 'check' : 'clipboard'" size="sm" />
              {{ copied ? t('keys.quickConfigureModal.copied') : t('keys.quickConfigureModal.copy') }}
            </button>
            <button
              type="button"
              class="flex items-center gap-1.5 rounded-lg bg-primary-600 px-2.5 py-1 text-xs font-medium text-white transition-colors hover:bg-primary-700"
              data-testid="quick-config-download"
              @click="downloadScript"
            >
              <Icon name="download" size="sm" />
              {{ t('keys.quickConfigureModal.download') }}
            </button>
          </div>
        </div>
        <pre class="max-h-96 overflow-auto p-4 text-xs leading-5 text-gray-100"><code>{{ script }}</code></pre>
      </div>

      <div class="rounded-lg border border-gray-200 bg-gray-50 p-3 text-sm text-gray-600 dark:border-dark-700 dark:bg-dark-800/50 dark:text-gray-300">
        <p class="font-medium text-gray-900 dark:text-white">{{ t('keys.quickConfigureModal.runTitle') }}</p>
        <code class="mt-1 block break-all text-xs">{{ runCommand }}</code>
      </div>
    </div>

    <template #footer>
      <div class="flex justify-end">
        <button type="button" class="btn btn-secondary" @click="emit('close')">
          {{ t('common.close') }}
        </button>
      </div>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { saveAs } from 'file-saver'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Icon from '@/components/icons/Icon.vue'
import {
  buildMacLinuxCodexQuickConfigScript,
  buildWindowsCodexQuickConfigScript,
  type CodexQuickConfigPlatform
} from '@/utils/codexQuickConfig'

const props = defineProps<{
  show: boolean
  apiKey: string
  baseUrl: string
  platform?: CodexQuickConfigPlatform | null
}>()

const emit = defineEmits<{ (event: 'close'): void }>()
const { t } = useI18n()

type TargetPlatform = 'unix' | 'windows'
const detectPlatform = (): TargetPlatform => /windows/i.test(navigator.userAgent) ? 'windows' : 'unix'
const activePlatform = ref<TargetPlatform>(detectPlatform())
const copied = ref(false)

const input = computed(() => ({
  apiKey: props.apiKey,
  baseUrl: props.baseUrl || window.location.origin,
  platform: props.platform
}))

const script = computed(() => activePlatform.value === 'windows'
  ? buildWindowsCodexQuickConfigScript(input.value)
  : buildMacLinuxCodexQuickConfigScript(input.value))

const fileName = computed(() => activePlatform.value === 'windows'
  ? 'sub2api-codex-config.ps1'
  : 'sub2api-codex-config.sh')

const runCommand = computed(() => activePlatform.value === 'windows'
  ? `powershell -ExecutionPolicy Bypass -File .\\${fileName.value}`
  : `chmod +x ${fileName.value} && ./${fileName.value}`)

const tabClass = (selected: boolean) => [
  'rounded-md px-3 py-2 text-sm font-medium transition-colors',
  selected
    ? 'bg-white text-primary-700 shadow-sm dark:bg-dark-800 dark:text-primary-300'
    : 'text-gray-600 hover:text-gray-900 dark:text-dark-300 dark:hover:text-white'
]

watch(() => props.show, (show) => {
  if (show) {
    activePlatform.value = detectPlatform()
    copied.value = false
  }
})

async function copyScript() {
  try {
    await navigator.clipboard.writeText(script.value)
    copied.value = true
    window.setTimeout(() => { copied.value = false }, 1800)
  } catch {
    copied.value = false
  }
}

function downloadScript() {
  saveAs(new Blob([script.value], { type: 'text/plain;charset=utf-8' }), fileName.value)
}
</script>
