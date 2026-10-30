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
  expect(el.textContent).toContain("正在获取监控数据");
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
    : Promise.resolve({id:8,status:"NORMAL",intervalSeconds:60,stale:false,indicators:{onlineDisks:1},lanes:[{lane:"live",intervalSeconds:60,result:{reportChecks:[{code:"minio",name:"MinIO 磁盘",status:"NORMAL",value:"1 个磁盘在线"}]}}]}));
  const state=reactive({kind:"redis"});
  const el=mount(()=>h(ComponentInspectionPanel,{kind:state.kind}));
  await vi.waitFor(()=>expect(api.getMonitoring).toHaveBeenCalledWith("component",7));
  state.kind="minio";
  resolveOld({id:7,status:"NORMAL",intervalSeconds:30,lanes:[{lane:"live",intervalSeconds:30,result:{reportChecks:[{code:"old",name:"过期 Redis 结果",value:"old"}]}}]});
  await vi.waitFor(()=>expect(el.querySelector(".metric-cards")?.textContent).toContain("在线磁盘1"));
  expect(el.textContent).not.toContain("过期 Redis 结果");
});
it("polls data and stops after unmount without starting reports", async () => {
  vi.useFakeTimers();
  try {
    const el=mount(()=>h(ComponentInspectionPanel,{kind:"redis"}));
    await vi.waitFor(()=>expect(api.getMonitoring).toHaveBeenCalledTimes(1));
    await vi.advanceTimersByTimeAsync(5000);
    expect(api.getMonitoring.mock.calls.length).toBeGreaterThanOrEqual(2);
    expect(el.textContent).toContain("自动更新");
    app!.unmount();app=undefined;
    const calls=api.listMonitoring.mock.calls.length;
    await vi.advanceTimersByTimeAsync(10000);
    expect(api.listMonitoring).toHaveBeenCalledTimes(calls);
    expect(api.createConfiguredInspection).not.toHaveBeenCalled();
  } finally {vi.useRealTimers();}
});
it("shows failed collection separately from the last valid data", async () => {
 api.getMonitoring.mockResolvedValue({status:"ERROR",intervalSeconds:30,lastError:"连接超时",indicators:{connections:12},result:{},lanes:[{lane:"live",intervalSeconds:30,result:{reportChecks:[{code:"redis-info",name:"Redis 运行情况",status:"NORMAL",value:"上次连接 12"}]}}]});
 const el=mount(()=>h(ComponentInspectionPanel,{kind:"redis"}));
 await vi.waitFor(()=>expect(el.textContent).toContain("最近更新失败：连接超时"));
 expect(el.querySelector(".metric-cards")?.textContent).toContain("连接12");
});
it("Linux uses the existing host monitor view without launching inspection", async () => {
 api.listMonitoring.mockResolvedValue([{id:3,targetType:"host",kind:"hosts",name:"主机",endpoint:"127.0.0.1",status:"NORMAL",enabled:true,intervalSeconds:60,indicators:{cpuPercent:12},summary:"CPU 12%"}]);
 const el=mount(()=>h(ComponentInspectionPanel,{kind:"hosts"}));
 await vi.waitFor(()=>expect(el.textContent).toContain("共享主机监控"));
 expect(api.getMonitoring).not.toHaveBeenCalled();
 expect(api.createConfiguredInspection).not.toHaveBeenCalled();
});

it("shows PostgreSQL architecture and indicators without inspection labels, raw JSON or collection phases", async () => {
 api.listMonitoring.mockResolvedValue([{id:1,targetType:"database",kind:"postgresql",name:"业务库",endpoint:"pg:5432",status:"NORMAL",enabled:true,intervalSeconds:30,indicators:{connections:12}}]);
 api.getMonitoring.mockResolvedValue({status:"NORMAL",intervalSeconds:30,indicators:{connections:12,lockWaits:0,slowQueries:0},result:{reportChecks:[
  {code:"cloudhis-2",name:"PostgreSQL 部署架构",status:"RECORDED",value:"old verbose deployment paragraph",attributes:{architecture:"单机",version:"14.8",role:"读写实例"}},
  {code:"postgresql-archive",name:"归档",status:"RECORDED",attributes:{archiveState:"已开启"}},
 ]},lanes:[{lane:"facts",result:{reportChecks:[{name:"不应铺开的主机专项",value:"file paths and commands"}]}},{lane:"deep",result:{reportChecks:[{value:"raw SQL evidence"}]}}]});
 const el=mount(()=>h(ComponentInspectionPanel,{kind:"postgresql"}));
 await vi.waitFor(()=>expect(el.textContent).toContain("单机"));
 expect(el.textContent).toContain("14.8");
 expect(el.querySelector(".metric-cards")?.textContent).toContain("当前连接12");
 for(const text of ["已记录","采集明细","主机与文件专项","数据库深度专项","old verbose","file paths","raw SQL"]) expect(el.textContent).not.toContain(text);
 expect(el.querySelector("pre")).toBeNull();
});

it.each(["postgresql","kubernetes","minio","elasticsearch","redis","rabbitmq"])("uses the same metric cards and trend layout for %s", async kind => {
 api.listMonitoring.mockResolvedValue([{id:1,targetType:"component",kind,name:kind,endpoint:"fixture",status:"NORMAL",enabled:true,intervalSeconds:60,indicators:{}}]);
 api.getMonitoring.mockResolvedValue({status:"NORMAL",intervalSeconds:60,indicators:{},result:{reportChecks:[]}});
 const el=mount(()=>h(ComponentInspectionPanel,{kind}));
 await vi.waitFor(()=>expect(api.getMonitoring).toHaveBeenCalled());
 expect(el.querySelectorAll(".metric-card").length).toBeGreaterThanOrEqual(4);
 expect(el.querySelectorAll(".metric-panel")).toHaveLength(2);
 expect(el.querySelector("pre")).toBeNull();
});

it("highlights the current instance and keeps unavailable values distinct from zero", async () => {
 api.listMonitoring.mockResolvedValue([{id:8,targetType:"component",kind:"minio",name:"存储实例",endpoint:"fixture",status:"PARTIAL",enabled:true,intervalSeconds:60,indicators:{offlineDisks:0}}]);
 api.getMonitoring.mockResolvedValue({status:"PARTIAL",intervalSeconds:60,indicators:{offlineDisks:0},result:{reportChecks:[]}});
 const el=mount(()=>h(ComponentInspectionPanel,{kind:"minio"}));
 await vi.waitFor(()=>expect(el.querySelector(".el-table__body tr.current-row")?.textContent).toContain("存储实例"));
 const cards=Array.from(el.querySelectorAll(".metric-card"));
 expect(cards.find(c=>c.textContent?.includes("离线磁盘"))?.querySelector("strong")?.textContent).toBe("0");
 const missing=cards.find(c=>c.textContent?.includes("最高磁盘使用率"));
 expect(missing?.querySelector("strong")?.textContent).toBe("—");
 expect(missing?.querySelector("small")).toBeNull();
});
