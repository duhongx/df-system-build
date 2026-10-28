<script setup lang="ts">
import { computed, ref, reactive, watch, onBeforeUnmount } from "vue";
import { ElMessage } from "element-plus";
import type { OpsDatabase } from "../../api/operations";
import {
  getDatabaseAccess,
  getRolePrivileges,
  previewDatabaseAccess,
  applyDatabaseAccess,
  type AccessCatalog,
  type AccessRole,
  type AccessPrivileges,
  type AccessRequest,
  type AccessPlan,
} from "../../api/database-access";

const props = defineProps<{ connection: OpsDatabase }>();
const emit = defineEmits<{ changed: [] }>();
const catalog = ref<AccessCatalog>();
const loading = ref(false),
  error = ref("");
const tab = ref("roles"),
  search = ref(""),
  hideSystem = ref(true),
  roleType = ref("");
const page = ref(1),
  pageSize = 20;
let generation = 0,
  detailGeneration = 0,
  formGeneration = 0;
const detail = ref<AccessRole>(),
  privileges = ref<AccessPrivileges>(),
  detailError = ref(""),
  detailLoading = ref(false),
  drawer = ref(false);
const formOpen = ref(false),
  previewOpen = ref(false),
  previewing = ref(false),
  applying = ref(false),
  formError = ref("");
const plan = ref<AccessPlan>();
let pending:
  | { id: number; revision: number; request: AccessRequest }
  | undefined;
const emptyForm = (): AccessRequest => ({
  revision: props.connection.revision,
  action: "create_role",
  role: "",
  password: "",
  login: true,
  schema: "",
  owner: "",
  mode: "",
  allSchemas: false,
  schemas: [],
  future: true,
  creators: [],
  memberRoles: [],
  remove: false,
});
const form = reactive<AccessRequest>(emptyForm());
const target = computed(
  () =>
    `${props.connection.host}:${props.connection.port} / ${props.connection.database}`,
);
const rows = computed(() =>
  (catalog.value?.roles || []).filter(
    (r) =>
      (!hideSystem.value || !r.name.startsWith("pg_")) &&
      (!roleType.value || (roleType.value === "login" ? r.login : !r.login)) &&
      r.name.toLowerCase().includes(search.value.toLowerCase()),
  ),
);
const schemaRows = computed(() =>
  (catalog.value?.schemas || []).filter((s) =>
    [s.name, s.owner].some((v) =>
      v.toLowerCase().includes(search.value.toLowerCase()),
    ),
  ),
);
const visibleRoles = computed(() =>
  rows.value.slice((page.value - 1) * pageSize, page.value * pageSize),
);
const permissionForm = computed(
  () =>
    form.action === "permissions" ||
    (form.action === "create_role" && form.mode !== ""),
);
const formTitle = computed(
  () =>
    ({
      create_role: form.login ? "创建用户" : "创建权限角色",
      create_schema: "创建 Schema",
      permissions: "配置对象权限",
      membership: "管理角色成员关系",
      password: "重置密码",
    })[form.action],
);
const editable = (role: AccessRole) =>
  !role.name.startsWith("pg_") &&
  role.name !== catalog.value?.actor &&
  (!role.superuser || !!catalog.value?.superuser);
const chosenSchemas = computed(() =>
  form.allSchemas
    ? catalog.value?.schemas.map((s) => s.name) || []
    : form.schemas,
);

function clearSensitive() {
  formGeneration++;
  form.password = "";
  pending = undefined;
  plan.value = undefined;
  previewOpen.value = false;
  formOpen.value = false;
}
async function load() {
  const token = ++generation;
  loading.value = true;
  error.value = "";
  try {
    const data = await getDatabaseAccess(props.connection.id);
    if (token === generation) catalog.value = data;
  } catch (e: any) {
    if (token === generation) error.value = e.message;
  } finally {
    if (token === generation) loading.value = false;
  }
}
watch(
  () => [props.connection.id, props.connection.revision],
  () => {
    generation++;
    detailGeneration++;
    clearSensitive();
    catalog.value = undefined;
    privileges.value = undefined;
    drawer.value = false;
    page.value = 1;
    load();
  },
  { immediate: true, flush: "sync" },
);
watch([search, roleType, hideSystem, tab], () => {
  page.value = 1;
});
watch(chosenSchemas, (names) => {
  if (!formOpen.value) return;
  form.creators = [
    ...new Set(
      (catalog.value?.schemas || [])
        .filter((s) => names.includes(s.name))
        .map((s) => s.owner),
    ),
  ];
});
onBeforeUnmount(() => {
  generation++;
  detailGeneration++;
  clearSensitive();
});
function open(
  action: AccessRequest["action"],
  role?: AccessRole,
  schema?: string,
  login = true,
) {
  clearSensitive();
  Object.assign(form, emptyForm(), {
    action,
    role: role?.name || "",
    login,
    owner: catalog.value?.actor || "",
  });
  if (action === "permissions") {
    form.mode = "readOnly";
    form.schemas = schema ? [schema] : [];
  }
  if (action === "membership") form.memberRoles = [];
  formError.value = "";
  formOpen.value = true;
  if (schema)
    form.creators = [
      ...new Set(
        (catalog.value?.schemas || [])
          .filter((s) => s.name === schema)
          .map((s) => s.owner),
      ),
    ];
}
async function showRole(role: AccessRole) {
  const token = ++detailGeneration;
  const id = props.connection.id;
  detail.value = role;
  privileges.value = undefined;
  detailError.value = "";
  detailLoading.value = true;
  drawer.value = true;
  try {
    const data = await getRolePrivileges(id, role.name);
    if (token === detailGeneration) privileges.value = data;
  } catch (e: any) {
    if (token === detailGeneration) detailError.value = e.message;
  } finally {
    if (token === detailGeneration) detailLoading.value = false;
  }
}
async function preview() {
  formError.value = "";
  if (
    (form.action === "create_role" && form.login) ||
    form.action === "password"
  ) {
    if (new TextEncoder().encode(form.password).length < 8) {
      formError.value = "请填写至少 8 字节的密码";
      return;
    }
  }
  if (permissionForm.value && chosenSchemas.value.length === 0) {
    formError.value = "请选择授权范围";
    return;
  }
  if (permissionForm.value && form.future && form.creators.length === 0) {
    formError.value = "请选择未来对象的创建者";
    return;
  }
  const id = props.connection.id,
    revision = props.connection.revision,
    token = generation,
    formToken = formGeneration;
  const request = JSON.parse(JSON.stringify(form)) as AccessRequest;
  request.revision = revision;
  if (
    request.action !== "password" &&
    !(request.action === "create_role" && request.login)
  )
    request.password = "";
  previewing.value = true;
  try {
    const data = await previewDatabaseAccess(id, request);
    if (
      token !== generation ||
      formToken !== formGeneration ||
      id !== props.connection.id ||
      revision !== props.connection.revision
    )
      return;
    pending = { id, revision, request };
    plan.value = data;
    previewOpen.value = true;
  } catch (e: any) {
    if (token === generation && formToken === formGeneration)
      formError.value = e.message;
  } finally {
    previewing.value = false;
  }
}
async function apply() {
  const change = pending,
    currentPlan = plan.value;
  if (!change || !currentPlan || currentPlan.blockers.length || applying.value)
    return;
  if (
    change.id !== props.connection.id ||
    change.revision !== props.connection.revision
  ) {
    clearSensitive();
    ElMessage.error("连接已变化，请重新预览");
    return;
  }
  const label = target.value;
  applying.value = true;
  try {
    const result = await applyDatabaseAccess(change.id, {
      ...change.request,
      previewHash: currentPlan.hash,
    });
    clearSensitive();
    if (result.status === "SUCCESS") {
      ElMessage.success(`${label}：${result.message}`);
      emit("changed");
    } else ElMessage.error(`${label}：${result.message}`);
    if (result.warning) ElMessage.warning(result.warning);
    await load();
    if (drawer.value && detail.value) await showRole(detail.value);
  } catch (e: any) {
    formError.value = e.message;
    previewOpen.value = false;
    pending = undefined;
    plan.value = undefined;
  } finally {
    applying.value = false;
  }
}
</script>

<template>
  <section class="database-access">
    <div class="pg-toolbar">
      <div class="pg-title">
        <strong>用户、角色与 Schema</strong><span>{{ target }}</span>
      </div>
      <span class="toolbar-spacer" />
      <el-button size="small" :loading="loading" @click="load">刷新</el-button>
      <el-button
        size="small"
        :disabled="!catalog?.canCreateSchema"
        @click="open('create_schema')"
        >创建 Schema</el-button
      >
      <el-button
        size="small"
        :disabled="!catalog?.canCreateRoles"
        @click="open('create_role', undefined, undefined, false)"
        >创建角色</el-button
      >
      <el-button
        type="primary"
        size="small"
        :disabled="!catalog?.canCreateRoles"
        @click="open('create_role')"
        >创建用户</el-button
      >
    </div>
    <el-alert v-if="error" :title="error" type="error" :closable="false" />
    <div v-if="catalog" class="access-context">
      当前连接账号 <b>{{ catalog.actor }}</b
      ><span
        >角色属于当前实例；Schema 和对象授权仅作用于
        <b>{{ catalog.database }}</b> 数据库。</span
      >
    </div>
    <div class="access-filters">
      <el-radio-group v-model="tab" size="small"
        ><el-radio-button value="roles">用户与角色</el-radio-button
        ><el-radio-button value="schemas"
          >Schema</el-radio-button
        ></el-radio-group
      >
      <el-input
        v-model="search"
        clearable
        :placeholder="
          tab === 'roles' ? '搜索用户／角色' : '搜索 Schema／所有者'
        "
        size="small"
        class="access-search"
      />
      <template v-if="tab === 'roles'"
        ><el-select v-model="roleType" size="small" class="role-type"
          ><el-option label="全部类型" value="" /><el-option
            label="可登录用户"
            value="login" /><el-option
            label="权限角色"
            value="group" /></el-select
        ><el-checkbox v-model="hideSystem">隐藏系统角色</el-checkbox></template
      >
    </div>
    <el-table
      v-if="tab === 'roles'"
      :data="visibleRoles"
      v-loading="loading"
      size="small"
      row-key="name"
      class="access-table"
    >
      <el-table-column label="用户／角色" min-width="185"
        ><template #default="{ row }"
          ><el-button link type="primary" @click="showRole(row)">{{
            row.name
          }}</el-button
          ><el-tag v-if="row.name === catalog?.actor" size="small" type="info"
            >当前连接</el-tag
          ></template
        ></el-table-column
      >
      <el-table-column label="类型" width="115"
        ><template #default="{ row }">{{
          row.login ? "可登录用户" : "权限角色"
        }}</template></el-table-column
      >
      <el-table-column label="角色属性" min-width="180"
        ><template #default="{ row }"
          ><div class="role-flags">
            <el-tag v-if="row.superuser" size="small" type="danger"
              >超级用户</el-tag
            ><el-tag v-if="row.createDB" size="small" type="warning"
              >创建数据库</el-tag
            ><el-tag v-if="row.createRole" size="small" type="warning"
              >管理角色</el-tag
            ><el-tag v-if="row.replication" size="small" type="info"
              >复制</el-tag
            ><el-tag v-if="row.bypassRLS" size="small" type="warning"
              >绕过 RLS</el-tag
            ><span
              v-if="
                !row.superuser &&
                !row.createDB &&
                !row.createRole &&
                !row.replication &&
                !row.bypassRLS
              "
              >普通角色</span
            >
          </div></template
        ></el-table-column
      >
      <el-table-column label="直接所属角色" min-width="180"
        ><template #default="{ row }">{{
          row.memberOf.join("、") || "无"
        }}</template></el-table-column
      >
      <el-table-column label="操作" width="285" fixed="right"
        ><template #default="{ row }"
          ><el-button link type="primary" @click="showRole(row)"
            >查看权限</el-button
          ><el-button
            link
            type="primary"
            :disabled="!editable(row)"
            @click="open('permissions', row)"
            >配置权限</el-button
          ><el-dropdown trigger="click" :disabled="!editable(row)"
            ><el-button link :disabled="!editable(row)">更多</el-button
            ><template #dropdown
              ><el-dropdown-menu
                ><el-dropdown-item @click="open('membership', row)"
                  >角色成员关系</el-dropdown-item
                ><el-dropdown-item
                  v-if="row.login"
                  @click="open('password', row)"
                  >重置密码</el-dropdown-item
                ></el-dropdown-menu
              ></template
            ></el-dropdown
          ></template
        ></el-table-column
      >
    </el-table>
    <el-pagination
      v-if="tab === 'roles'"
      v-model:current-page="page"
      :total="rows.length"
      :page-size="pageSize"
      layout="total, prev, pager, next"
    />
    <el-table
      v-else
      :data="schemaRows"
      v-loading="loading"
      size="small"
      row-key="name"
      class="access-table"
      ><el-table-column
        prop="name"
        label="Schema"
        min-width="200"
      /><el-table-column
        prop="owner"
        label="所有者"
        min-width="180"
      /><el-table-column
        prop="objects"
        label="表／视图／序列数量"
        width="185"
      /><el-table-column width="130"
        ><template #default="{ row }"
          ><el-button
            link
            type="primary"
            @click="open('permissions', undefined, row.name)"
            >分配权限</el-button
          ></template
        ></el-table-column
      ></el-table
    >

    <el-drawer
      v-model="drawer"
      :title="`用户／角色：${detail?.name || ''}`"
      size="min(1060px, 92vw)"
    >
      <div v-if="detail" class="role-detail" v-loading="detailLoading">
        <p>
          <b>{{ detail.login ? "可登录用户" : "权限角色" }}</b> · 密码有效期
          {{ detail.validUntil || "未设置" }} · 连接上限
          {{ detail.limit < 0 ? "不限制" : detail.limit }} ·
          {{ detail.inherit ? "继承成员权限" : "不自动继承成员权限" }}
        </p>
        <p>
          直接所属角色：{{ detail.memberOf.join("、") || "无" }}（角色仍可能通过
          SET ROLE 使用成员角色权限）
        </p>
        <el-alert
          v-if="detailError"
          :title="detailError"
          type="error"
          :closable="false"
        />
        <h3>当前数据库的有效对象权限</h3>
        <p class="access-note">
          包含直接授权、PUBLIC
          和可继承权限；配置只读时会另外核验所有权、角色切换及高权限函数。
        </p>
        <el-table :data="privileges?.schemas || []" size="small"
          ><el-table-column
            prop="schema"
            label="Schema"
            min-width="150"
          /><el-table-column
            prop="owner"
            label="所有者"
            min-width="140"
          /><el-table-column label="访问／创建" width="120"
            ><template #default="{ row }"
              >{{ row.usage ? "可访问" : "无访问" }} /
              {{ row.create ? "可创建" : "不可创建" }}</template
            ></el-table-column
          ><el-table-column label="可读表／总表" width="125"
            ><template #default="{ row }"
              >{{ row.readable }} / {{ row.tables }}</template
            ></el-table-column
          ><el-table-column
            prop="writable"
            label="有写权限的表"
            width="120"
          /><el-table-column label="可推进序列／总数" width="145"
            ><template #default="{ row }"
              >{{ row.advanceable }} / {{ row.sequences }}</template
            ></el-table-column
          ></el-table
        >
        <h3>未来对象的已有默认授权</h3>
        <el-table :data="privileges?.defaults || []" size="small"
          ><el-table-column
            prop="creator"
            label="对象创建者"
            min-width="140" /><el-table-column
            prop="schema"
            label="Schema"
            min-width="140" /><el-table-column label="对象类型" width="100"
            ><template #default="{ row }">{{
              (
                {
                  r: "表／视图",
                  S: "序列",
                  f: "函数",
                  T: "类型",
                  n: "Schema",
                } as any
              )[row.kind] || row.kind
            }}</template></el-table-column
          ><el-table-column
            prop="source"
            label="授权来源"
            min-width="140" /><el-table-column
            prop="privileges"
            label="权限"
            min-width="220"
        /></el-table>
      </div>
    </el-drawer>

    <el-dialog
      v-model="formOpen"
      :title="formTitle"
      width="640px"
      :close-on-click-modal="false"
      :before-close="
        (done: () => void) => {
          if (!applying) {
            clearSensitive();
            done();
          }
        }
      "
    >
      <p class="access-note">
        目标：{{ target }} · 执行账号：{{ catalog?.actor }}
      </p>
      <el-form label-width="115px" @submit.prevent="preview">
        <el-form-item
          v-if="form.action !== 'create_schema'"
          label="用户／角色"
          required
          ><el-input
            v-if="form.action === 'create_role'"
            v-model="form.role"
            autocomplete="off"
            placeholder="输入新用户／角色名称" /><el-select
            v-else
            v-model="form.role"
            filterable
            placeholder="选择用户／角色"
            class="full-width"
            ><el-option
              v-for="r in catalog?.roles.filter(editable)"
              :key="r.name"
              :label="r.name"
              :value="r.name" /></el-select
        ></el-form-item>
        <template v-if="form.action === 'create_schema'"
          ><el-form-item label="Schema 名称" required
            ><el-input v-model="form.schema" autocomplete="off" /></el-form-item
          ><el-form-item label="所有者" required
            ><el-select v-model="form.owner" filterable class="full-width"
              ><el-option
                v-for="r in catalog?.roles"
                :key="r.name"
                :label="r.name"
                :value="r.name" /></el-select
          ></el-form-item>
          <p class="access-note">
            所有者可以创建、修改和删除此 Schema
            内的对象。只读用户应通过授权访问，不应设为所有者。
          </p></template
        >
        <el-form-item
          v-if="
            form.action === 'password' ||
            (form.action === 'create_role' && form.login)
          "
          label="密码"
          required
          ><el-input
            v-model="form.password"
            type="password"
            show-password
            autocomplete="new-password"
            placeholder="至少 8 字节，不保存到平台连接配置"
        /></el-form-item>
        <el-form-item
          v-if="form.action === 'create_role' || form.action === 'permissions'"
          label="对象权限"
          ><el-select v-model="form.mode" class="full-width"
            ><el-option
              v-if="form.action === 'create_role'"
              label="暂不授权"
              value="" /><el-option
              label="只读：查询数据，不允许写入或创建对象"
              value="readOnly" /><el-option
              label="读写：查询、新增、修改、删除业务数据"
              value="readWrite" /></el-select
        ></el-form-item>
        <template v-if="permissionForm"
          ><el-form-item label="授权范围" required
            ><div class="full-width">
              <el-checkbox v-model="form.allSchemas"
                >当前数据库全部业务 Schema（以本次预览为准）</el-checkbox
              ><el-select
                v-if="!form.allSchemas"
                v-model="form.schemas"
                multiple
                filterable
                placeholder="选择一个或多个 Schema"
                class="full-width"
                ><el-option
                  v-for="s in catalog?.schemas"
                  :key="s.name"
                  :label="`${s.name} · ${s.owner}`"
                  :value="s.name"
              /></el-select></div></el-form-item
          ><el-form-item label="未来新对象"
            ><el-checkbox v-model="form.future"
              >同时配置指定创建者的默认权限</el-checkbox
            ></el-form-item
          ><el-form-item v-if="form.future" label="对象创建者" required
            ><el-select
              v-model="form.creators"
              multiple
              filterable
              class="full-width"
              placeholder="通常选择业务对象所有者"
              ><el-option
                v-for="r in catalog?.roles"
                :key="r.name"
                :label="r.name"
                :value="r.name" /></el-select
          ></el-form-item>
          <p class="access-note">
            {{
              form.mode === "readOnly"
                ? "只读会收回目标用户在所选范围内的直接写权限。若继承权限、所有权或 PUBLIC 仍允许写入，预览会说明原因并阻止执行。"
                : "读写授权不包含创建表、删除表或修改结构。"
            }}未来授权仅覆盖选定创建者的新表、视图和序列。
          </p></template
        >
        <template v-if="form.action === 'membership'"
          ><el-form-item label="操作"
            ><el-radio-group v-model="form.remove"
              ><el-radio :value="false">加入角色</el-radio
              ><el-radio :value="true">移出角色</el-radio></el-radio-group
            ></el-form-item
          ><el-form-item label="所属角色" required
            ><el-select
              v-model="form.memberRoles"
              multiple
              filterable
              class="full-width"
              ><el-option
                v-for="r in catalog?.roles.filter(
                  (r) =>
                    r.name !== form.role &&
                    (!form.remove ||
                      catalog?.roles
                        .find((t) => t.name === form.role)
                        ?.memberOf.includes(r.name)),
                )"
                :key="r.name"
                :label="r.name"
                :value="r.name" /></el-select
          ></el-form-item>
          <p class="access-note">
            成员关系作用于整个实例，可能影响此用户在其他数据库的权限。
          </p></template
        >
      </el-form>
      <el-alert
        v-if="formError"
        :title="formError"
        type="error"
        :closable="false"
      />
      <template #footer
        ><el-button :disabled="applying" @click="clearSensitive">取消</el-button
        ><el-button type="primary" :loading="previewing" @click="preview"
          >预览变更</el-button
        ></template
      >
    </el-dialog>

    <el-dialog
      v-model="previewOpen"
      title="确认用户权限变更"
      width="850px"
      append-to-body
      :close-on-click-modal="false"
      :close-on-press-escape="!applying"
      :show-close="!applying"
    >
      <template v-if="plan"
        ><p>
          <b>{{ plan.summary }}</b>
        </p>
        <p>目标：{{ target }} · 执行账号：{{ catalog?.actor }}</p>
        <el-alert
          v-if="plan.blockers.length"
          title="以下问题未解决，不能执行本次变更"
          type="error"
          :closable="false"
          ><ul class="access-blockers">
            <li v-for="message in plan.blockers" :key="message">
              {{ message }}
            </li>
          </ul></el-alert
        >
        <p v-if="plan.schemas.length">
          Schema 范围：{{ plan.schemas.join("、") }}
        </p>
        <ul class="access-notes">
          <li v-for="note in plan.notes" :key="note">{{ note }}</li>
        </ul>
        <details class="access-sql" :open="!plan.blockers.length">
          <summary>将执行 {{ plan.sql.length }} 条 SQL（密码已隐藏）</summary>
          <pre>{{ plan.sql.join(";\n") + ";" }}</pre>
        </details>
      </template>
      <template #footer
        ><el-button
          :disabled="applying"
          @click="
            previewOpen = false;
            pending = undefined;
            plan = undefined;
          "
          >返回修改</el-button
        ><el-button
          type="primary"
          :disabled="!plan || !!plan.blockers.length"
          :loading="applying"
          @click="apply"
          >执行变更</el-button
        ></template
      >
    </el-dialog>
  </section>
</template>

<style scoped>
.database-access {
  min-width: 0;
  display: flex;
  flex-direction: column;
  height: 100%;
  overflow: auto;
}
.access-context {
  margin: 0 20px 14px;
  padding: 12px 15px;
  background: var(--ui-bg-soft, #f5f7fa);
  border: 1px solid var(--ui-border, #e2e8f0);
  font-size: 12px;
  display: flex;
  gap: 24px;
  flex-wrap: wrap;
}
.access-filters {
  padding: 0 20px 14px;
  display: flex;
  align-items: center;
  gap: 12px;
  flex-wrap: wrap;
}
.access-search {
  max-width: 260px;
}
.role-type {
  width: 135px;
}
.access-table {
  flex-shrink: 0;
}
.role-flags {
  display: flex;
  gap: 5px;
  flex-wrap: wrap;
}
.el-pagination {
  padding: 16px;
  justify-content: flex-end;
}
.full-width {
  width: 100%;
}
.access-note {
  font-size: 12px;
  line-height: 1.8;
  color: var(--ui-muted, #64748b);
  margin: 8px 0 16px;
}
.access-notes {
  font-size: 13px;
  line-height: 1.8;
  padding-left: 20px;
}
.access-blockers {
  max-height: 220px;
  overflow: auto;
  line-height: 1.8;
  padding-left: 20px;
}
.access-sql {
  margin-top: 18px;
  font-size: 13px;
}
.access-sql summary {
  cursor: pointer;
  color: var(--ui-primary, #2563eb);
}
.access-sql pre {
  padding: 14px;
  background: #f5f7fa;
  border: 1px solid #e2e8f0;
  max-height: 330px;
  overflow: auto;
  white-space: pre-wrap;
  word-break: break-all;
  font: 12px/1.7 monospace;
}
.role-detail h3 {
  font-size: 15px;
  margin: 26px 0 12px;
}
.role-detail p {
  font-size: 13px;
  line-height: 1.8;
}
.el-table :deep(.el-button + .el-dropdown) {
  margin-left: 12px;
}
</style>
