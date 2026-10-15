import { defineStore } from 'pinia'
import { ref, computed } from 'vue'
import { getSettings } from '../api/settings'

export const useSettingsStore = defineStore('settings', () => {
  const deploySource = ref<'artifact' | 'both'>('artifact')
  const k8sNamespace = ref('default')
  const customerEnv = ref('')
  const loaded = ref(false)

  async function fetchSettings() {
    try {
      const data = await getSettings()
      if (data.deploy_source) {
        deploySource.value = data.deploy_source === 'both' || data.deploy_source === 'source' ? 'both' : 'artifact'
      }
      k8sNamespace.value = data.k8s_namespace || 'default'
      customerEnv.value = (data.customer_env || '').trim()
      loaded.value = true
    } catch (_) {
      deploySource.value = 'artifact'
      customerEnv.value = ''
      // Keep source-build entry points closed when settings cannot be read.
      loaded.value = true
    }
  }

  const isLocal = computed(() => customerEnv.value === '本地')
  const showTaskList = computed(() => deploySource.value === 'both' && isLocal.value)
  const autoRollout = computed(() => customerEnv.value === '本地')
  const showBatchDeploy = computed(() => deploySource.value === 'artifact' || deploySource.value === 'both')

  return {
    deploySource,
    k8sNamespace,
    customerEnv,
    autoRollout,
    isLocal,
    loaded,
    fetchSettings,
    showTaskList,
    showBatchDeploy,
  }
})
