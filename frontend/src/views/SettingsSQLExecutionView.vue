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
    <p>以下选项默认关闭，执行文件时可以单独调整。未开启跳过的数据库错误会停止当前文件。</p>
    <el-form label-width="190px">
      <el-form-item label="已存在字段跳过"><el-switch v-model="form.sql_execute_skip_exists_column" /><span class="hint">字段已存在（42701）</span></el-form-item>
      <el-form-item label="对象已存在跳过"><el-switch v-model="form.sql_execute_skip_exists_table" /><span class="hint">表、索引等对象已存在（42P07）</span></el-form-item>
      <el-form-item label="唯一冲突跳过"><el-switch v-model="form.sql_execute_skip_unique_constraint" /><span class="hint">唯一约束冲突（23505）</span></el-form-item>
      <el-form-item><el-button type="primary" :loading="saving" @click="save">保存</el-button></el-form-item>
    </el-form>
    <p class="hint">按文件中的 schema 同名账号连接环境配置中的 PostgreSQL 数据库。每句自动提交，最长执行 60 秒；已成功提交的 SQL 不自动回滚。</p>
  </el-card>
</template>
<style scoped>
.settings-card { max-width: 900px; }
p { line-height: 1.8; }
.hint { color: #64748b; margin-left: 12px; font-size: 13px; }
</style>
