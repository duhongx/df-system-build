<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue';
import { ElMessage } from 'element-plus';
import { executeFailedSQLStatement, listSQLRetryAccounts, type SQLChangeFile, type SQLChangeStatement, type SQLExecuteOptions, type SQLRetryAccountChoice, type SQLRetryTarget } from '../api/postgresql';

const props = defineProps<{ target: SQLRetryTarget | null; options: SQLExecuteOptions; disabled?: boolean }>();
const emit = defineEmits<{
  close: [];
  started: [result: {file: SQLChangeFile; statements: SQLChangeStatement[]}];
}>();
const username = ref('');
const passwordSource = ref<'configured' | 'manual'>('configured');
const password = ref('');
const accounts = ref<SQLRetryAccountChoice[]>([]);
const submitting = ref(false);
const loading = ref(false);
const error = ref('');
let generation = 0;
const passwordLabel = (source: string) => ({admin: '管理员密码', common: '统一业务密码', override: '该账号专用密码'}[source] || '已配置密码');
const selectedSource = computed(() => accounts.value.find(item => item.username === username.value)?.passwordSource);
const canSubmit = computed(() => !!props.target && !!username.value.trim() && !props.disabled && !submitting.value &&
  (passwordSource.value === 'configured' || !!password.value));

function close() {
  if (submitting.value) return;
  password.value = '';
  error.value = '';
  emit('close');
}
watch(() => props.target, async target => {
  const id = ++generation;
  username.value = '';
  password.value = '';
  passwordSource.value = 'configured';
  error.value = '';
  accounts.value = [];
  if (!target) { loading.value = false; return; }
  loading.value = true;
  try {
    const list = await listSQLRetryAccounts();
    if (id === generation) accounts.value = list;
  } catch {
    if (id === generation) error.value = '账号列表加载失败，可直接输入账号并选择密码来源。';
  } finally {
    if (id === generation) loading.value = false;
  }
});
watch(passwordSource, () => { password.value = ''; error.value = ''; });
onBeforeUnmount(() => { generation++; password.value = ''; });

async function submit() {
  if (!canSubmit.value || !props.target) return;
  submitting.value = true;
  error.value = '';
  try {
    const result = await executeFailedSQLStatement(props.target.statementId, {...props.options}, {
      username: username.value.trim(), passwordSource: passwordSource.value,
      ...(passwordSource.value === 'manual' ? {password: password.value} : {}),
    });
    password.value = '';
    emit('started', result);
    ElMessage.success('账号连接校验通过，已提交重试');
    emit('close');
  } catch (cause) {
    error.value = cause instanceof Error ? cause.message : '提交重试失败，请检查账号与密码。';
  } finally {
    submitting.value = false;
  }
}
</script>

<template>
  <el-dialog :model-value="!!target" title="切换账号重试" width="620px" append-to-body
    :show-close="!submitting" :close-on-click-modal="false" :close-on-press-escape="!submitting" @close="close">
    <template v-if="target">
      <el-descriptions :column="1" border class="retry-context">
        <el-descriptions-item label="SQL 文件">{{target.fileName}}</el-descriptions-item>
        <el-descriptions-item label="范围">{{target.transactionLabel || `第 ${target.lineNumber} 行 SQL`}}</el-descriptions-item>
        <el-descriptions-item label="原连接账号">{{target.originalAccount || target.schemaName || '旧版未记录'}}</el-descriptions-item>
        <el-descriptions-item label="文件 schema">{{target.schemaName}}</el-descriptions-item>
      </el-descriptions>
      <p class="retry-note">{{target.transactionLabel ? '本次将使用所选账号重试整个事务组。' : '本次只重试选中的 SQL；视图 DROP/CREATE 配对失败时会一起重试。'}}文件 schema 保留，后续执行使用原配置。</p>
      <el-form label-width="110px" :disabled="submitting">
        <el-form-item label="数据库账号">
          <el-select v-model="username" filterable allow-create default-first-option :loading="loading"
            placeholder="选择或输入，例如 df_zhushuju、postgres" class="account-input" autocomplete="off">
            <el-option v-for="item in accounts" :key="item.username" :value="item.username"
              :label="`${item.username}（${passwordLabel(item.passwordSource)}）`" />
          </el-select>
        </el-form-item>
        <el-form-item label="密码来源">
          <el-radio-group v-model="passwordSource">
            <el-radio value="configured">使用已配置密码</el-radio>
            <el-radio value="manual">临时输入密码</el-radio>
          </el-radio-group>
        </el-form-item>
        <el-form-item v-if="passwordSource==='configured'" label="">
          <span class="retry-note">{{username==='postgres' ? '使用环境配置中的管理员密码。' : selectedSource ? `使用${passwordLabel(selectedSource)}。` : '按环境配置中的业务密码方式读取；未配置时可临时输入密码。'}}</span>
        </el-form-item>
        <el-form-item v-else label="临时密码">
          <el-input v-model="password" type="password" show-password autocomplete="new-password" placeholder="仅用于本次重试" @keyup.enter="submit" />
        </el-form-item>
      </el-form>
      <el-alert v-if="error" :title="error" type="error" :closable="false" />
    </template>
    <template #footer>
      <el-button :disabled="submitting" @click="close">取消</el-button>
      <el-button type="primary" :loading="submitting" :disabled="!canSubmit" @click="submit">{{target?.transactionLabel ? '校验账号并整组重试' : '校验账号并重试'}}</el-button>
    </template>
  </el-dialog>
</template>

<style scoped>
.retry-context{margin-bottom:12px;overflow-wrap:anywhere}.retry-note{color:var(--el-text-color-secondary);font-size:13px;line-height:1.7}.account-input{width:100%}
</style>
