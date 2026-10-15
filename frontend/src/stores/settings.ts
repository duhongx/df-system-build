import { defineStore } from 'pinia'
import { ref, computed } from 'vue'
import { getSettings } from '../api/settings'

export const useSettingsStore = defineStore('settings', () => {
  const k8sNamespace = ref('default')
  const customerEnv = ref('')
  const loaded = ref(false)

  async function fetchSettings() {
    try {
      const data = await getSettings()
      k8sNamespace.value = data.k8s_namespace || 'default'
      customerEnv.value = (data.customer_env || '').trim()
      loaded.value = true
    } catch (_) {
      customerEnv.value = ''
      // Keep source-build entry points closed when settings cannot be read.
      loaded.value = true
    }
  }

  const isLocal = computed(() => customerEnv.value === '本地')
  const showTaskList = computed(() => isLocal.value)
  const autoRollout = computed(() => isLocal.value)

  return {
    k8sNamespace,
    customerEnv,
    autoRollout,
    isLocal,
    loaded,
    fetchSettings,
    showTaskList,
  }
})
