import {afterEach,expect,it,vi} from 'vitest';
import {createApp,h,reactive,type App} from 'vue';
import ElementPlus from 'element-plus';
import HistoricalSlowQueries from '../src/components/HistoricalSlowQueries.vue';
const api=vi.hoisted(()=>({getMonitoringSlowQueries:vi.fn(),getReportSlowQueries:vi.fn()}));
vi.mock('../src/api/monitoring',()=>api);
let app:App|undefined;
afterEach(()=>{app?.unmount();app=undefined;document.body.replaceChildren();vi.clearAllMocks();});
const flush=()=>new Promise(resolve=>setTimeout(resolve,0));
const record=(id:number)=>({id,database:'business',eventTime:'2026-11-02T01:00:00Z',durationMs:5001,username:'reader',client:'local',sql:`select ${id}`,collectedAt:'2026-11-02T01:01:00Z'});
function mount(props:{databaseId?:number;reportId?:number}) {const root=document.createElement('div');document.body.append(root);app=createApp({render:()=>h(HistoricalSlowQueries,props)});app.use(ElementPlus);app.mount(root);return root;}
it('requests the next backend page using the same record ceiling rather than paginating a truncated list',async()=>{
 api.getMonitoringSlowQueries.mockImplementation((_id,params)=>Promise.resolve({list:[record(params.page===1?537:517)],page:params.page,size:20,total:537,asOfId:537,targets:[],availability:'available'}));
 const root=mount({databaseId:7});await flush();
 expect(root.textContent).toContain('已发生慢 SQL');expect(root.textContent).not.toContain('48h');
 expect(root.querySelector('.el-table__body')?.textContent).toContain('select 537');
 (root.querySelector('.btn-next') as HTMLElement).click();await flush();
 expect(api.getMonitoringSlowQueries).toHaveBeenLastCalledWith(7,expect.objectContaining({page:2,asOfId:537}));
 expect(root.querySelector('.el-table__body')?.textContent).toContain('select 517');
});
it('reads report pages exclusively from the frozen report endpoint',async()=>{
 api.getReportSlowQueries.mockResolvedValue({list:[record(81)],total:81,asOfId:0,targets:[{id:1,name:'生产 PostgreSQL'}],frozenAt:'2026-11-02T01:00:00Z',availability:'archived'});
 const root=mount({reportId:9});await flush();
 expect(api.getReportSlowQueries).toHaveBeenCalledWith(9,expect.objectContaining({page:1,size:20}));expect(api.getMonitoringSlowQueries).not.toHaveBeenCalled();
 expect(root.textContent).toContain('慢 SQL 存档');expect(root.textContent).toContain('快照');
});
it('discards a delayed response when the selected instance changes',async()=>{
 let finish:(value:unknown)=>void=()=>{};
 api.getMonitoringSlowQueries.mockImplementation((id)=>id===1?new Promise(resolve=>finish=resolve):Promise.resolve({list:[record(22)],total:1,targets:[],asOfId:22}));
 const props=reactive({databaseId:1});const root=mount(props);props.databaseId=2;await flush();
 finish({list:[record(11)],total:1,targets:[],asOfId:11});await flush();
 expect(root.querySelector('.el-table__body')?.textContent).toContain('select 22');expect(root.querySelector('.el-table__body')?.textContent).not.toContain('select 11');
});
