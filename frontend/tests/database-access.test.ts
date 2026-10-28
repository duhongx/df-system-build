import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { createApp, h, reactive, nextTick, type App } from "vue";
import ElementPlus from "element-plus";
import DatabaseAccessPanel from "../src/components/postgresql/DatabaseAccessPanel.vue";

const api = vi.hoisted(() => ({
  getDatabaseAccess: vi.fn(),
  getRolePrivileges: vi.fn(),
  previewDatabaseAccess: vi.fn(),
  applyDatabaseAccess: vi.fn(),
}));
vi.mock("../src/api/database-access", () => api);
function catalog() {
  return {
    database: "business",
    actor: "manager",
    version: 180000,
    superuser: true,
    canCreateRoles: true,
    canCreateSchema: true,
    roles: [
      {
        name: "reader",
        login: true,
        superuser: false,
        createDB: false,
        createRole: false,
        replication: false,
        bypassRLS: false,
        inherit: true,
        limit: -1,
        validUntil: "",
        memberOf: [],
      },
      {
        name: "owner",
        login: true,
        superuser: false,
        createDB: false,
        createRole: false,
        replication: false,
        bypassRLS: false,
        inherit: true,
        limit: -1,
        validUntil: "",
        memberOf: [],
      },
    ],
    schemas: [{ name: "business", owner: "owner", objects: 2 }],
  };
}
function deferred<T>() {
  let resolve!: (data: T) => void;
  const promise = new Promise<T>((r) => {
    resolve = r;
  });
  return { promise, resolve };
}
let app: App | undefined;
beforeEach(() => {
  vi.clearAllMocks();
  vi.stubGlobal(
    "ResizeObserver",
    class {
      observe() {}
      unobserve() {}
      disconnect() {}
    },
  );
  window.matchMedia = vi.fn().mockImplementation(() => ({
    matches: false,
    addEventListener() {},
    removeEventListener() {},
  }));
  api.getDatabaseAccess.mockResolvedValue(catalog());
  api.getRolePrivileges.mockResolvedValue({
    role: "reader",
    schemas: [],
    defaults: [],
  });
  api.previewDatabaseAccess.mockResolvedValue({
    hash: "preview-1",
    target: "business / manager",
    summary: "配置 reader 为只读",
    sql: ["GRANT SELECT ON ALL TABLES IN SCHEMA business TO reader"],
    blockers: [],
    notes: [],
    schemas: ["business"],
  });
  api.applyDatabaseAccess.mockResolvedValue({
    recordId: 1,
    status: "SUCCESS",
    message: "操作已完成",
    warning: "",
  });
});
afterEach(() => {
  app?.unmount();
  app = undefined;
  document.body.replaceChildren();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});
function mount() {
  const connection = reactive({
    id: 7,
    revision: 3,
    name: "primary",
    host: "127.0.0.1",
    port: 5432,
    database: "business",
    username: "manager",
  });
  const host = document.createElement("div");
  document.body.append(host);
  app = createApp({
    setup: () => () =>
      h(DatabaseAccessPanel, { connection: connection as any }),
  });
  app.use(ElementPlus);
  app.mount(host);
  return { host, connection };
}
function button(text: string, scope: ParentNode = document.body) {
  const found = Array.from(scope.querySelectorAll("button")).find(
    (b) => b.textContent?.trim() === text,
  );
  if (!found) throw new Error("Missing button: " + text);
  return found;
}
async function permissions() {
  await vi.waitFor(() => expect(document.body.textContent).toContain("reader"));
  button("配置权限").click();
  await nextTick();
  const checkbox = Array.from(
    document.querySelectorAll("label.el-checkbox"),
  ).find((x) => x.textContent?.includes("全部业务 Schema")) as HTMLElement;
  expect(checkbox).toBeTruthy();
  checkbox.click();
  await nextTick();
  button("预览变更").click();
}
it("previews the selected role and scope before executing, including future object creators", async () => {
  mount();
  await permissions();
  await vi.waitFor(() =>
    expect(api.previewDatabaseAccess).toHaveBeenCalledOnce(),
  );
  expect(api.previewDatabaseAccess).toHaveBeenCalledWith(
    7,
    expect.objectContaining({
      revision: 3,
      action: "permissions",
      role: "reader",
      mode: "readOnly",
      allSchemas: true,
      future: true,
      creators: ["owner"],
    }),
  );
  expect(api.applyDatabaseAccess).not.toHaveBeenCalled();
  await vi.waitFor(() => expect(button("执行变更").disabled).toBe(false));
  button("执行变更").click();
  await vi.waitFor(() =>
    expect(api.applyDatabaseAccess).toHaveBeenCalledWith(
      7,
      expect.objectContaining({
        role: "reader",
        revision: 3,
        previewHash: "preview-1",
      }),
    ),
  );
});
it("blocks execution when inherited or PUBLIC privileges prevent readonly access", async () => {
  api.previewDatabaseAccess.mockResolvedValue({
    hash: "blocked",
    summary: "只读",
    sql: [],
    notes: [],
    schemas: ["business"],
    blockers: ["PUBLIC 允许写入 business.items"],
  });
  mount();
  await permissions();
  await vi.waitFor(() =>
    expect(document.body.textContent).toContain(
      "PUBLIC 允许写入 business.items",
    ),
  );
  expect(button("执行变更").disabled).toBe(true);
  button("执行变更").click();
  expect(api.applyDatabaseAccess).not.toHaveBeenCalled();
});
it("discards a pending preview when the connection or revision changes", async () => {
  const pending = deferred<any>();
  api.previewDatabaseAccess.mockReturnValue(pending.promise);
  const { connection } = mount();
  await permissions();
  await vi.waitFor(() =>
    expect(api.previewDatabaseAccess).toHaveBeenCalledOnce(),
  );
  connection.id = 8;
  connection.revision = 4;
  connection.database = "other";
  await nextTick();
  pending.resolve({
    hash: "old-target",
    summary: "不能显示的旧连接预览",
    sql: [],
    notes: [],
    schemas: [],
    blockers: [],
  });
  await nextTick();
  await new Promise((r) => setTimeout(r, 0));
  expect(document.body.textContent).not.toContain("不能显示的旧连接预览");
  expect(api.applyDatabaseAccess).not.toHaveBeenCalled();
});
it("does not reopen a cancelled form when an in flight preview returns", async () => {
  const pending = deferred<any>();
  api.previewDatabaseAccess.mockReturnValue(pending.promise);
  mount();
  await permissions();
  await vi.waitFor(() =>
    expect(api.previewDatabaseAccess).toHaveBeenCalledOnce(),
  );
  button("取消").click();
  await nextTick();
  pending.resolve({
    hash: "cancelled",
    summary: "不应打开的已取消预览",
    sql: [],
    notes: [],
    schemas: [],
    blockers: [],
  });
  await nextTick();
  await new Promise((r) => setTimeout(r, 0));
  expect(document.body.textContent).not.toContain("不应打开的已取消预览");
  expect(api.applyDatabaseAccess).not.toHaveBeenCalled();
});
it("shows the actual failed collection instead of a made up empty role list", async () => {
  api.getDatabaseAccess.mockRejectedValue(new Error("连接账号无权限"));
  mount();
  await vi.waitFor(() =>
    expect(document.body.textContent).toContain("连接账号无权限"),
  );
  expect(button("创建用户").disabled).toBe(true);
});
it("keeps a new password out of storage and clears it when the form is cancelled", async () => {
  const store = vi.spyOn(Storage.prototype, "setItem");
  mount();
  await vi.waitFor(() => expect(button("创建用户").disabled).toBe(false));
  button("创建用户").click();
  await nextTick();
  const name = document.querySelector(
    'input[placeholder="输入新用户／角色名称"]',
  ) as HTMLInputElement;
  const password = document.querySelector(
    'input[autocomplete="new-password"]',
  ) as HTMLInputElement;
  name.value = "new_reader";
  name.dispatchEvent(new Event("input", { bubbles: true }));
  password.value = "private-password-2026";
  password.dispatchEvent(new Event("input", { bubbles: true }));
  await nextTick();
  button("预览变更").click();
  await vi.waitFor(() =>
    expect(api.previewDatabaseAccess).toHaveBeenCalledWith(
      7,
      expect.objectContaining({
        role: "new_reader",
        password: "private-password-2026",
        action: "create_role",
      }),
    ),
  );
  expect(document.body.textContent).not.toContain("private-password-2026");
  expect(store).not.toHaveBeenCalled();
  await vi.waitFor(() => expect(button("返回修改")).toBeTruthy());
  button("返回修改").click();
  await nextTick();
  button("取消").click();
  await nextTick();
  button("创建用户").click();
  await nextTick();
  expect(
    (
      document.querySelector(
        'input[autocomplete="new-password"]',
      ) as HTMLInputElement
    ).value,
  ).toBe("");
  store.mockRestore();
});
