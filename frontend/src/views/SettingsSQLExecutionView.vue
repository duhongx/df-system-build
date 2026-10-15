<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { getSettings, updateSettings } from '../api/settings'
const saving = ref(false)
const form = ref({ sql_execute_skip_exists_column: false, sql_execute_skip_exists_table: false, sql_execute_skip_unique_constraint: false })
onMounted(async () => {
  const settings = await getSettings()
  for (const key of Object.keys(form.value) as (keyof typeof form.value)[]) form.value[key] = settings[key] === 'true'
})
async function save() {
  saving.value = true
  try {
    await updateSettings(Object.fromEntries(Object.entries(form.value).map(([key, value]) => [key, String(value)])))
    ElMessage.success('SQL 执行默认选项已保存')
  } finally { saving.value = false }
}
</script>
<template>
  <el-card class="settings-card">
    <template #header>SQL执行配置</template>

    <el-form label-width="190px">
      <el-form-item label="已存在字段跳过"><el-switch v-model="form.sql_execute_skip_exists_column" /></el-form-item>
      <el-form-item label="对象已存在跳过"><el-switch v-model="form.sql_execute_skip_exists_table" /></el-form-item>
      <el-form-item label="唯一冲突跳过"><el-switch v-model="form.sql_execute_skip_unique_constraint" /></el-form-item>
      <el-form-item><el-button type="primary" :loading="saving" @click="save">保存</el-button></el-form-item>
    </el-form>

  </el-card>
</template>
<style scoped>
.settings-card { max-width: 900px; }
</style>
