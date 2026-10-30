import {afterEach,expect,it} from "vitest";
import {createApp,h,reactive,type App} from "vue";
import ElementPlus from "element-plus";
import MonitoringDataTable from "../src/components/MonitoringDataTable.vue";
import type {InspectionCommandResult} from "../src/api/operations";
let app:App|undefined;
afterEach(()=>{app?.unmount();app=undefined;document.body.replaceChildren();});
function mount(check:InspectionCommandResult){const root=document.createElement("div");document.body.append(root);app=createApp({render:()=>h(MonitoringDataTable,{check})});app.use(ElementPlus);app.mount(root);return root;}
it("renders a valid empty backend table whose rows are null",()=>{
 const root=mount({code:"postgresql-slots",name:"复制槽",status:"NORMAL",value:"当前实例没有复制槽",tables:[{columns:["Slot"],rows:null}]} as unknown as InspectionCommandResult);
 expect(root.textContent).toContain("当前实例没有复制槽");expect(root.textContent).not.toContain("数据不可用");
});
it("paginates queue results and searches all pages",async()=>{
 const check={code:"rabbitmq-queues",name:"队列与积压",status:"NORMAL",tables:[{columns:["队列","待消费"],rows:Array.from({length:35},(_,i)=>[`业务队列-${i+1}`,String(i)])}]} as InspectionCommandResult;
 const root=mount(check);
 await new Promise(resolve=>setTimeout(resolve,0));
 expect(root.querySelectorAll(".el-table__body tbody tr")).toHaveLength(25);
 (root.querySelector(".btn-next") as HTMLElement).click();
 await new Promise(resolve=>setTimeout(resolve,0));
 expect(root.querySelector(".el-table__body")?.textContent).toContain("业务队列-35");
 const input=root.querySelector("input") as HTMLInputElement;input.value="业务队列-35";input.dispatchEvent(new Event("input",{bubbles:true}));
 await new Promise(resolve=>setTimeout(resolve,0));
 expect(root.querySelectorAll(".el-table__body tbody tr")).toHaveLength(1);
 expect(root.querySelector(".el-table__body")?.textContent).toContain("业务队列-35");
});
it("distinguishes unavailable data and stale estimates without displaying a zero time",async()=>{
 const check=reactive({code:"postgresql-table-bloat",name:"表膨胀估算",status:"UNKNOWN",reason:"后台统计尚未完成",collectedAt:"0001-01-01T00:00:00Z",attributes:{estimated:"true"}} as InspectionCommandResult);
 const root=mount(check);
 expect(root.textContent).toContain("数据不可用");expect(root.textContent).toContain("后台统计尚未完成");expect(root.textContent).not.toContain("更新于");
 check.status="RECORDED";check.attributes={estimated:"true",stale:"true"};check.collectedAt="2026-10-30T01:00:00Z";check.reason="膨胀统计更新延迟，显示上次估算";check.tables=[{columns:["表名","膨胀倍数估算"],rows:[["public.orders","1.8"]]}];
 await new Promise(resolve=>setTimeout(resolve,0));
 expect(root.textContent).toContain("数据过期");expect(root.textContent).toContain("public.orders");expect(root.textContent).toContain("更新于");expect(root.textContent).not.toContain("已记录");
});
