<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { getSettings, updateSettings } from '../api/settings'
import {
  getSQLForceWhitelist,
  saveSQLForceWhitelist,
  type SQLForceWhitelistOption,
} from '../api/postgresql'

const loading = ref(false)
const saving = ref(false)
const whitelistOptions = ref<SQLForceWhitelistOption[]>([])
const whitelistEnabled = ref<string[]>([])

const form = ref<Record<string, string>>({
  sql_execute_lock_timeout: '5s',
  sql_execute_statement_timeout: '10min',
  sql_execute_skip_exists_column: 'true',
  sql_execute_skip_exists_table: 'true',
  sql_execute_skip_unique_constraint: 'true',
  sql_execute_force_blocked_sql: 'true',
})

const enabledCount = computed(() => whitelistEnabled.value.length)

function switchValue(key: string) {
  return form.value[key] === 'true'
}

function updateSwitchValue(key: string, value: boolean) {
  form.value[key] = value ? 'true' : 'false'
}

function updateSkipExistsColumn(value: string | number | boolean) {
  updateSwitchValue('sql_execute_skip_exists_column', Boolean(value))
}

function updateSkipExistsTable(value: string | number | boolean) {
  updateSwitchValue('sql_execute_skip_exists_table', Boolean(value))
}

function updateSkipUniqueConstraint(value: string | number | boolean) {
  updateSwitchValue('sql_execute_skip_unique_constraint', Boolean(value))
}

function updateForceBlockedSQL(value: string | number | boolean) {
  updateSwitchValue('sql_execute_force_blocked_sql', Boolean(value))
}

function isValidTimeoutLiteral(value: string) {
  return /^\d+(ms|s|m|min|h)?$/i.test(value.trim().replace(/\s+/g, ''))
}

function validateTimeoutConfig() {
  if (!isValidTimeoutLiteral(form.value.sql_execute_lock_timeout)) {
    ElMessage.error('锁等待超时格式不正确，请填写 500ms、5s、10min 或 1h 这类值')
    return false
  }
  if (!isValidTimeoutLiteral(form.value.sql_execute_statement_timeout)) {
    ElMessage.error('语句执行超时格式不正确，请填写 500ms、5s、10min 或 1h 这类值')
    return false
  }
  return true
}

async function loadSettings() {
  loading.value = true
  try {
    const [settings, whitelist] = await Promise.all([
      getSettings(),
      getSQLForceWhitelist(),
    ])
    for (const key of Object.keys(form.value)) {
      if (settings[key] !== undefined) {
        form.value[key] = settings[key]
      }
    }
    whitelistOptions.value = whitelist.available || []
    whitelistEnabled.value = whitelist.enabled || []
  } catch (err: any) {
    ElMessage.error(err?.message || '加载 SQL 执行配置失败')
  } finally {
    loading.value = false
  }
}

async function saveSettings() {
  if (!validateTimeoutConfig()) return
  saving.value = true
  try {
    await updateSettings(form.value)
    const whitelist = await saveSQLForceWhitelist(whitelistEnabled.value)
    whitelistOptions.value = whitelist.available || []
    whitelistEnabled.value = whitelist.enabled || []
    ElMessage.success('SQL 执行配置已保存')
  } catch (err: any) {
    ElMessage.error(err?.message || '保存 SQL 执行配置失败')
  } finally {
    saving.value = false
  }
}

onMounted(loadSettings)
</script>

<template>
  <div class="sql-execution-settings" v-loading="loading">
    <el-card shadow="never" class="page-card">
      <template #header>
        <div class="page-header">
          <div>
            <h2>SQL执行配置</h2>
            <p>配置实施 SQL 执行的默认超时、跳过规则和高风险 SQL 强制执行白名单。</p>
          </div>
          <el-button type="primary" :loading="saving" @click="saveSettings">保存</el-button>
        </div>
      </template>

      <el-form label-width="160px" class="settings-form">
        <section class="settings-section">
          <h3>执行超时</h3>
          <el-form-item label="锁等待超时">
            <el-input v-model="form.sql_execute_lock_timeout" placeholder="例如 5s" />
            <span class="field-help">等待表锁的时间，超过后本条 SQL 失败，可调整后重试。</span>
          </el-form-item>
          <el-form-item label="语句执行超时">
            <el-input v-model="form.sql_execute_statement_timeout" placeholder="例如 10min" />
            <span class="field-help">单条 SQL 最大执行时间，支持 ms、s、min、h。</span>
          </el-form-item>
        </section>

        <section class="settings-section">
          <h3>默认执行选项</h3>
          <el-form-item label="已存在字段跳过">
            <el-switch
              :model-value="switchValue('sql_execute_skip_exists_column')"
              @update:model-value="updateSkipExistsColumn"
            />
            <span class="field-help">新增字段已存在时自动跳过，继续执行后续 SQL。</span>
          </el-form-item>
          <el-form-item label="对象已存在跳过">
            <el-switch
              :model-value="switchValue('sql_execute_skip_exists_table')"
              @update:model-value="updateSkipExistsTable"
            />
            <span class="field-help">CREATE TABLE / INDEX 等对象已存在时自动跳过。</span>
          </el-form-item>
          <el-form-item label="唯一冲突跳过">
            <el-switch
              :model-value="switchValue('sql_execute_skip_unique_constraint')"
              @update:model-value="updateSkipUniqueConstraint"
            />
            <span class="field-help">唯一约束冲突时标记跳过，适合重复执行补丁 SQL。</span>
          </el-form-item>
          <el-form-item label="强制执行高风险 SQL">
            <el-switch
              :model-value="switchValue('sql_execute_force_blocked_sql')"
              @update:model-value="updateForceBlockedSQL"
            />
            <span class="field-help">开启后白名单内的高风险 SQL 可在确认后执行。</span>
          </el-form-item>
        </section>

        <section class="settings-section">
          <div class="section-title-row">
            <h3>白名单</h3>
            <el-tag type="info">已启用 {{ enabledCount }} 项</el-tag>
          </div>
          <el-checkbox-group v-model="whitelistEnabled" class="whitelist-grid">
            <el-checkbox
              v-for="item in whitelistOptions"
              :key="item.sqlType"
              :label="item.sqlType"
            >
              <span class="whitelist-content">
                <span class="whitelist-type">{{ item.sqlType }}</span>
                <span class="whitelist-label">{{ item.label }}</span>
              </span>
            </el-checkbox>
          </el-checkbox-group>
        </section>
      </el-form>
    </el-card>
  </div>
</template>

<style scoped>
.sql-execution-settings {
  padding: 16px;
}

.page-card {
  max-width: 1120px;
}

.page-header {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 16px;
}

.page-header h2 {
  margin: 0 0 8px;
  font-size: 18px;
}

.page-header p {
  margin: 0;
  color: #909399;
}

.settings-form {
  max-width: 960px;
}

.settings-section {
  padding: 8px 0 20px;
  border-bottom: 1px solid #ebeef5;
}

.settings-section:last-child {
  border-bottom: 0;
}

.settings-section h3 {
  margin: 12px 0 16px;
  font-size: 15px;
}

.field-help {
  display: block;
  margin-top: 6px;
  color: #909399;
  font-size: 12px;
  line-height: 1.5;
}

.section-title-row {
  display: flex;
  align-items: center;
  gap: 10px;
}

.whitelist-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(420px, 1fr));
  gap: 10px 18px;
}

.whitelist-grid :deep(.el-checkbox) {
  height: auto;
  margin-right: 0;
  align-items: flex-start;
  white-space: normal;
}

.whitelist-grid :deep(.el-checkbox__label) {
  flex: 1;
  min-width: 0;
  padding-left: 8px;
}

.whitelist-content {
  display: grid;
  grid-template-columns: 210px minmax(0, 1fr);
  column-gap: 12px;
  align-items: start;
  width: 100%;
  line-height: 1.4;
}

.whitelist-type {
  font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
  font-size: 12px;
  white-space: nowrap;
}

.whitelist-label {
  color: #606266;
  overflow-wrap: anywhere;
}

@media (max-width: 720px) {
  .whitelist-grid {
    grid-template-columns: 1fr;
  }

  .whitelist-content {
    grid-template-columns: 1fr;
    row-gap: 2px;
  }
}
</style>
