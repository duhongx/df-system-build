import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { createApp, h, reactive, type App } from "vue";
import ElementPlus from "element-plus";
import InspectionsView from "../src/views/InspectionsView.vue";
import ComponentInspectionPanel from "../src/components/ComponentInspectionPanel.vue";

const api = vi.hoisted(() => ({
  listInspectionRuns: vi.fn(),
  listInspectionPlans: vi.fn(),
  deleteInspectionPlan: vi.fn(),
  deleteInspection: vi.fn(),
  downloadInspection: vi.fn(),
  createConfiguredInspection: vi.fn(),
  listComponents: vi.fn(),
  listMonitoring: vi.fn(),
  getMonitoring: vi.fn(),
  getMonitoringHistory: vi.fn(),
  refreshMonitoring: vi.fn(),
  getCollection: vi.fn(),
  collectComponent: vi.fn(),
  collectDatabaseInspection: vi.fn(),
  listOpsDatabases: vi.fn(),
  push: vi.fn(),
}));
vi.mock("../src/api/monitoring", () => api);
vi.mock("../src/api/operations", () => api);
vi.mock("../src/views/ServerMonitorView.vue", () => ({ default: { props: ["serverId", "embedded"], render: () => "共享主机监控" } }));
vi.mock("vue-router", () => ({
  useRoute: () => ({ query: {} }),
  useRouter: () => ({ push: api.push }),
}));
vi.mock("../src/api/server-mgmt", () => ({
  listManagedServers: vi.fn().mockResolvedValue([]),
}));
let app: App | undefined;
beforeEach(() => {
  vi.clearAllMocks();
  api.listInspectionRuns.mockResolvedValue({ list: [], total: 0 });
  api.listInspectionPlans.mockResolvedValue([]);
  api.createConfiguredInspection.mockResolvedValue({ id: 91 });
  api.listComponents.mockResolvedValue([
    {
      id: 7,
      kind: "redis",
      name: "生产 Redis",
      endpoint: "redis://redis:6379",
      revision: 2,
      enabled: true,
    },
    {
      id: 8,
      kind: "minio",
      name: "生产 MinIO",
      endpoint: "",
      revision: 1,
      enabled: true,
      source: "",
    },
  ]);
  api.listMonitoring.mockResolvedValue([
    {id:7,targetType:"component",kind:"redis",name:"生产 Redis",endpoint:"redis://redis:6379",enabled:true,status:"NORMAL",intervalSeconds:30,indicators:{},summary:"当前运行正常"},
    {id:8,targetType:"component",kind:"minio",name:"生产 MinIO",endpoint:"主机 mc 别名",enabled:true,status:"NORMAL",intervalSeconds:60,indicators:{},summary:"磁盘在线"},
  ]);
  api.getMonitoring.mockImplementation((_kind:string,id:number)=>Promise.resolve({id,enabled:true,status:"PENDING",intervalSeconds:30,stale:true,lastError:"",lanes:[{lane:"live",intervalSeconds:30,stale:true}]}));
  api.getMonitoringHistory.mockResolvedValue({samples:[],stepSeconds:30});
  api.refreshMonitoring.mockResolvedValue(null);
});
afterEach(() => {
  app?.unmount();
  app = undefined;
  document.body.replaceChildren();
});
function mount(render: () => ReturnType<typeof h>) {
  const el = document.createElement("div");
  document.body.append(el);
  app = createApp({ render });
  app.use(ElementPlus);
  app.mount(el);
  return el;
}
function button(root: Element, label: string) {
  const b = Array.from(root.querySelectorAll("button")).find(
    (v) => v.textContent?.trim() === label,
  );
  expect(b, `button ${label}`).toBeTruthy();
  b!.click();
}
it("generates an all-configured-sources report without selecting command profiles", async () => {
  const el = mount(() => h(InspectionsView));
  await vi.waitFor(() => expect(api.listInspectionRuns).toHaveBeenCalled());
  button(el, "生成巡检报告");
  await vi.waitFor(() =>
    expect(document.body.textContent).toContain("本次报告范围"),
  );
  button(document.body, "汇总并生成");
  await vi.waitFor(() =>
    expect(api.createConfiguredInspection).toHaveBeenCalledWith(
      "系统巡检报告",
      undefined,
    ),
  );
  expect(api.push).toHaveBeenCalledWith("/inspections/91");
  expect(document.body.textContent).not.toContain("执行范围");
});
it("opens monitoring automatically without requiring an inspection", async () => {
  const el = mount(() => h(ComponentInspectionPanel, { kind: "redis" }));
  await vi.waitFor(() => expect(api.getMonitoring).toHaveBeenCalledWith("component", 7));
  expect(el.textContent).toContain("后台自动采集中");
  expect(el.textContent).not.toContain("立即检查");
  expect(api.collectComponent).not.toHaveBeenCalled();
  expect(api.createConfiguredInspection).not.toHaveBeenCalled();
});
it("leaves loading state when no component connections exist", async () => {
  api.listMonitoring.mockResolvedValue([]);
  const el = mount(() => h(ComponentInspectionPanel, { kind: "redis" }));
  await vi.waitFor(() => expect(el.textContent).toContain("尚未登记此组件"));
  expect(api.getMonitoring).not.toHaveBeenCalled();
});
it("discards late results after switching component kinds", async () => {
  let resolveOld: (v: unknown) => void = () => {};
  api.getMonitoring.mockImplementation((_kind: string, id: number) => id === 7
    ? new Promise(resolve => { resolveOld=resolve; })
    : Promise.resolve({id:8,status:"NORMAL",intervalSeconds:60,stale:false,lanes:[{lane:"live",intervalSeconds:60,result:{reportChecks:[{code:"minio",name:"MinIO 磁盘",status:"NORMAL",value:"1 个磁盘在线"}]}}]}));
  const state=reactive({kind:"redis"});
  const el=mount(()=>h(ComponentInspectionPanel,{kind:state.kind}));
  await vi.waitFor(()=>expect(api.getMonitoring).toHaveBeenCalledWith("component",7));
  state.kind="minio";
  resolveOld({id:7,status:"NORMAL",intervalSeconds:30,lanes:[{lane:"live",intervalSeconds:30,result:{reportChecks:[{code:"old",name:"过期 Redis 结果",value:"old"}]}}]});
  await vi.waitFor(()=>expect(el.textContent).toContain("1 个磁盘在线"));
  expect(el.textContent).not.toContain("过期 Redis 结果");
});
it("polls data and stops after unmount without starting reports", async () => {
  vi.useFakeTimers();
  try {
    const el=mount(()=>h(ComponentInspectionPanel,{kind:"redis"}));
    await vi.waitFor(()=>expect(api.getMonitoring).toHaveBeenCalledTimes(1));
    await vi.advanceTimersByTimeAsync(5000);
    expect(api.getMonitoring.mock.calls.length).toBeGreaterThanOrEqual(2);
    expect(el.textContent).toContain("后台持续采集");
    app!.unmount();app=undefined;
    const calls=api.listMonitoring.mock.calls.length;
    await vi.advanceTimersByTimeAsync(10000);
    expect(api.listMonitoring).toHaveBeenCalledTimes(calls);
    expect(api.createConfiguredInspection).not.toHaveBeenCalled();
  } finally {vi.useRealTimers();}
});
it("shows failed collection separately from the last valid data", async () => {
 api.getMonitoring.mockResolvedValue({status:"ERROR",intervalSeconds:30,lastError:"连接超时",result:{},lanes:[{lane:"live",intervalSeconds:30,result:{reportChecks:[{code:"redis-info",name:"Redis 运行情况",status:"NORMAL",value:"上次连接 12"}]}}]});
 const el=mount(()=>h(ComponentInspectionPanel,{kind:"redis"}));
 await vi.waitFor(()=>expect(el.textContent).toContain("最近采集失败：连接超时"));
 expect(el.textContent).toContain("上次连接 12");
});
it("Linux uses the existing host monitor view without launching inspection", async () => {
 api.listMonitoring.mockResolvedValue([{id:3,targetType:"host",kind:"hosts",name:"主机",endpoint:"127.0.0.1",status:"NORMAL",enabled:true,intervalSeconds:60,indicators:{cpuPercent:12},summary:"CPU 12%"}]);
 const el=mount(()=>h(ComponentInspectionPanel,{kind:"hosts"}));
 await vi.waitFor(()=>expect(el.textContent).toContain("共享主机监控"));
 expect(api.getMonitoring).toHaveBeenCalledWith("host",3);
 expect(api.createConfiguredInspection).not.toHaveBeenCalled();
});
