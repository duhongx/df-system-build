<script setup lang="ts">
import { ref, computed, watch, reactive, nextTick } from "vue";
import { ElMessage, ElMessageBox } from "element-plus";
import {
  listSavedQueries,
  saveQueryFavorite,
  deleteQueryFavorite,
  type SavedQuery,
  type WorkspaceDraft,
} from "../../api/database-workbench";
import type { OpsDatabase } from "../../api/operations";
import { formatDate } from "../../utils/database-workbench.mjs";
const props = defineProps<{
  modelValue: boolean;
  connections: OpsDatabase[];
  connectionId?: number;
  draft?: WorkspaceDraft;
  closed: WorkspaceDraft[];
}>();
const emit = defineEmits<{
  "update:modelValue": [boolean];
  open: [SavedQuery];
  restore: [WorkspaceDraft];
  discard: [WorkspaceDraft];
}>();
const rows = ref<SavedQuery[]>([]),
  keyword = ref(""),
  scope = ref("current"),
  tab = ref("saved"),
  busy = ref(false),
  error = ref(""),
  editing = ref(false),
  saving = ref(false);
const draft = reactive<Partial<SavedQuery>>({});
const filtered = computed(() =>
  rows.value.filter(
    (row) =>
      (scope.value === "all" ||
        !row.databaseId ||
        row.databaseId === props.connectionId) &&
      [row.name, row.sql].some((text) =>
        text.toLowerCase().includes(keyword.value.toLowerCase()),
      ),
  ),
);
const database = (id: number) =>
  id
    ? props.connections.find((d) => d.id === id)?.name || `已移除连接 #${id}`
    : "全部连接";
async function load() {
  busy.value = true;
  error.value = "";
  try {
    rows.value = await listSavedQueries();
  } catch (e: any) {
    error.value = e.message;
  } finally {
    busy.value = false;
  }
}
function edit(row?: SavedQuery) {
  Object.keys(draft).forEach((key) => delete (draft as any)[key]);
  Object.assign(
    draft,
    row
      ? { ...row }
      : {
          name: props.draft?.name || "",
          databaseId: props.draft?.databaseId || props.connectionId || 0,
          schema: props.draft?.schema || "",
          sql: props.draft?.sql || "",
          parametersJSON: props.draft?.parameters || "[]",
        },
  );
  editing.value = true;
}
async function save() {
  saving.value = true;
  try {
    await saveQueryFavorite(draft);
    editing.value = false;
    await load();
    ElMessage.success("SQL 已保存到我的 SQL");
  } finally {
    saving.value = false;
  }
}
async function remove(row: SavedQuery) {
  try {
    await ElMessageBox.confirm(`删除收藏“${row.name}”？`, "删除 SQL 收藏", {
      confirmButtonText: "删除",
      type: "warning",
    });
  } catch {
    return;
  }
  await deleteQueryFavorite(row);
  load();
}
watch(
  () => props.modelValue,
  (open) => {
    if (open) {
      tab.value = "saved";
      load();
    }
  },
);
defineExpose({
  saveCurrent: () => {
    emit("update:modelValue", true);
    void nextTick(() => edit());
  },
});
</script>
<template>
  <el-drawer
    :model-value="modelValue"
    title="我的 SQL"
    size="min(760px,92vw)"
    @update:model-value="emit('update:modelValue', $event)"
    ><el-tabs v-model="tab"
      ><el-tab-pane label="SQL 收藏" name="saved" /><el-tab-pane
        :label="`关闭的窗口（${closed.length}）`"
        name="closed" /></el-tabs
    ><template v-if="tab === 'saved'"
      ><div class="saved-toolbar">
        <el-input
          v-model="keyword"
          size="small"
          clearable
          placeholder="搜索标题或 SQL"
        /><el-select v-model="scope" size="small" style="width: 150px"
          ><el-option value="current" label="当前连接及通用" /><el-option
            value="all"
            label="全部收藏" /></el-select
        ><el-button size="small" type="primary" @click="edit()"
          >新增收藏</el-button
        ><el-button size="small" @click="load">刷新</el-button>
      </div>
      <el-alert v-if="error" type="error" :title="error" :closable="false" />
      <div v-loading="busy">
        <el-empty
          v-if="!filtered.length && !busy"
          description="收藏常用查询，下次可直接载入 SQL 窗口"
        />
        <article v-for="row in filtered" :key="row.id" class="saved-query">
          <header>
            <strong>{{ row.name }}</strong
            ><el-button text size="small" @click="edit(row)">编辑</el-button
            ><el-button text type="danger" size="small" @click="remove(row)"
              >删除</el-button
            >
          </header>
          <p>
            {{ database(row.databaseId) }} · Schema
            {{ row.schema || "连接默认" }} · {{ formatDate(row.updatedAt) }}
          </p>
          <pre class="sql-code">{{ row.sql }}</pre>
          <el-button
            size="small"
            type="primary"
            plain
            @click="emit('open', row)"
            >{{
              row.databaseId ? "在对应连接打开" : "在当前连接打开"
            }}</el-button
          >
        </article>
      </div></template
    ><template v-else
      ><p class="muted">
        关闭的 SQL 窗口保留草稿和原连接版本。恢复后不会自动执行 SQL。
      </p>
      <el-empty v-if="!closed.length" description="没有已关闭的窗口" />
      <article
        v-for="row in closed.slice().reverse()"
        :key="row.id"
        class="saved-query"
      >
        <header>
          <strong>{{ row.name }}</strong
          ><el-button
            text
            type="danger"
            size="small"
            @click="emit('discard', row)"
            >删除草稿</el-button
          >
        </header>
        <p>{{ row.target }} · Schema {{ row.schema || "连接默认" }}</p>
        <pre class="sql-code">{{ row.sql }}</pre>
        <el-button
          size="small"
          type="primary"
          plain
          @click="emit('restore', row)"
          >恢复窗口</el-button
        >
      </article></template
    >
    <el-dialog
      v-model="editing"
      :title="draft.id ? '编辑 SQL 收藏' : '收藏 SQL'"
      width="680px"
      append-to-body
      ><el-form label-position="top"
        ><el-form-item label="标题" required
          ><el-input v-model="draft.name" maxlength="128"
        /></el-form-item>
        <div class="favorite-options">
          <el-form-item label="适用连接"
            ><el-select v-model="draft.databaseId"
              ><el-option label="全部连接（通用 SQL）" :value="0" /><el-option
                v-for="db in connections"
                :key="db.id"
                :label="db.name"
                :value="db.id" /></el-select></el-form-item
          ><el-form-item label="Schema"
            ><el-input v-model="draft.schema" placeholder="留空使用连接默认"
          /></el-form-item>
        </div>
        <el-form-item label="SQL" required
          ><el-input
            v-model="draft.sql"
            type="textarea"
            :rows="9"
            class="favorite-sql" /></el-form-item
        ><el-form-item label="绑定参数（JSON 数组）"
          ><el-input v-model="draft.parametersJSON" /></el-form-item></el-form
      ><template #footer
        ><el-button @click="editing = false">取消</el-button
        ><el-button type="primary" :loading="saving" @click="save"
          >保存收藏</el-button
        ></template
      ></el-dialog
    >
  </el-drawer>
</template>
<style scoped>
.saved-toolbar {
  display: flex;
  gap: 10px;
  margin: 2px 0 16px;
}
.saved-toolbar > .el-input {
  flex: 1;
  min-width: 130px;
}
.saved-query {
  padding: 14px 0;
  border-bottom: 1px solid var(--ui-border);
}
.saved-query header {
  display: flex;
  gap: 8px;
  align-items: center;
}
.saved-query header strong {
  flex: 1;
  font-size: 13px;
}
.saved-query p,
.muted {
  font-size: 11px;
  color: var(--ui-muted);
  overflow-wrap: anywhere;
}
.saved-query pre {
  max-height: 180px;
}
.favorite-options {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 16px;
}
.favorite-sql :deep(textarea) {
  font:
    12px/1.8 ui-monospace,
    SFMono-Regular,
    Consolas,
    monospace;
}
</style>
