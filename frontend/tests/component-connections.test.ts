import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { createApp, h, type App } from "vue";
import ElementPlus from "element-plus";
import ComponentConnections from "../src/components/ComponentConnections.vue";

const api = vi.hoisted(() => ({
  listComponents: vi.fn(),
  saveComponent: vi.fn(),
  deleteComponent: vi.fn(),
}));
vi.mock("../src/api/monitoring", () => ({
  ...api,
  componentKinds: [{ value: "minio", label: "MinIO" }],
}));
vi.mock("../src/api/server-mgmt", () => ({
  listManagedServers: vi.fn().mockResolvedValue([]),
}));

let app: App | undefined;
beforeEach(() => {
  vi.clearAllMocks();
  api.listComponents.mockResolvedValue([]);
  api.saveComponent.mockResolvedValue({});
});
afterEach(() => {
  app?.unmount();
  app = undefined;
  document.body.replaceChildren();
});
function mount() {
  const root = document.createElement("div");
  document.body.append(root);
  app = createApp({ render: () => h(ComponentConnections) });
  app.use(ElementPlus);
  app.mount(root);
}
function click(label: string, scope: ParentNode = document.body) {
  const button = Array.from(scope.querySelectorAll("button")).find(b => b.textContent?.trim() === label);
  expect(button).toBeTruthy();
  button!.click();
}
function enter(label: string, value: string) {
  const item = Array.from(document.body.querySelectorAll(".el-form-item")).find(row => row.querySelector(".el-form-item__label")?.textContent === label);
  const input = item?.querySelector("input");
  expect(input, label).toBeTruthy();
  input!.value = value;
  input!.dispatchEvent(new Event("input", { bubbles: true }));
}

it("saves a MinIO API address and credentials without any managed hosts or mc alias", async () => {
  mount();
  await vi.waitFor(() => expect(api.listComponents).toHaveBeenCalled());
  click("新增组件连接");
  await vi.waitFor(() => expect(document.body.textContent).toContain("直接通过 API 采集"));
  expect(document.body.textContent).not.toContain("可留空复用主机已有 mc 别名");
  expect(document.body.textContent).not.toContain("旧连接 mc 别名");
  expect(document.body.textContent).not.toContain("已有每日记录");
  enter("名称", "客户 MinIO");
  enter("连接地址", "192.168.199.102:9000");
  enter("Access Key", "fixture-access");
  enter("Secret Key", "fixture-secret");
  click("保存连接");
  await vi.waitFor(() => expect(api.saveComponent).toHaveBeenCalledWith(expect.objectContaining({
    kind: "minio", endpoint: "http://192.168.199.102:9000", serverId: 0,
    username: "fixture-access", secret: "fixture-secret", options: {},
  })));
});

it("retains HTTPS and existing credentials when editing an API connection without a host", async () => {
  api.listComponents.mockResolvedValue([{ id: 5, kind: "minio", name: "TLS MinIO", endpoint: "https://minio.example:9443", username: "fixture-access", source: "", serverId: 0, enabled: true, optionsJSON: "{}", revision: 1 }]);
  mount();
  await vi.waitFor(() => expect(document.body.textContent).toContain("TLS MinIO"));
  click("编辑", document.querySelector(".el-table__body-wrapper")!);
  await vi.waitFor(() => expect(document.body.textContent).toContain("编辑组件连接"));
  click("保存连接");
  await vi.waitFor(() => expect(api.saveComponent).toHaveBeenCalledWith(expect.objectContaining({ id: 5, endpoint: "https://minio.example:9443", serverId: 0, secret: "", clearSecret: false })));
});
