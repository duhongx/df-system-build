import { defineStore } from 'pinia'
import { ref, computed } from 'vue'
import { getSettings } from '../api/settings'

export const useSettingsStore = defineStore('settings', () => {
  const deploySource = ref<'source' | 'artifact' | 'both'>('both')
  const loaded = ref(false)

  async function fetchSettings() {
    try {
      const data = await getSettings()
      if (data.deploy_source) {
        deploySource.value = data.deploy_source as 'source' | 'artifact' | 'both'
      }
      loaded.value = true
    } catch (_) {
      // fallback to default 'both'
      loaded.value = true
    }
  }

  const showTaskList = computed(() => deploySource.value === 'source' || deploySource.value === 'both')
  const showBatchDeploy = computed(() => deploySource.value === 'artifact' || deploySource.value === 'both')

  return {
    deploySource,
    loaded,
    fetchSettings,
    showTaskList,
    showBatchDeploy,
  }
})
