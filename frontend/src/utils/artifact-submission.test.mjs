import { test } from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import vm from 'node:vm'
import { parse, compileScript } from '@vue/compiler-sfc'
import { transformSync } from 'esbuild'
import * as vue from 'vue'

function evaluate(source, imports = {}, globals = {}) {
  const module={exports:{}}
  const {code}=transformSync(source,{loader:'ts',format:'cjs',target:'es2020'})
  vm.runInNewContext(code,{module,exports:module.exports,Error,...globals,require(name){assert.ok(Object.hasOwn(imports,name),name);return imports[name]}})
  return module.exports
}
const {descriptor}=parse(fs.readFileSync(new URL('../views/BatchDeployView.vue',import.meta.url),'utf8'))
const script=compileScript(descriptor,{id:'artifact-submission'}).content
const utilities=Object.fromEntries(['update-task-workflow','batch-deploy-selection','upload-activity'].map(name=>[`../utils/${name}`,evaluate(fs.readFileSync(new URL(`./${name}.ts`,import.meta.url),'utf8'))]))
const deferred=()=>{let resolve,reject;const promise=new Promise((a,b)=>{resolve=a;reject=b});return{promise,resolve,reject}}
const flush=async()=>{for(let i=0;i<40;i++)await Promise.resolve()}
const pipelines=(count,status='PENDING')=>Array.from({length:count},(_,i)=>({id:i+1,batchId:'batch-test',status,appName:`service-${i}`}))

function harness(options={}) {
  const request=deferred(),navigation=[],timers=new Map(),unmount=[]
  let timerID=0,posts=0,reads=0,rows=[],readError,readOverride
  const apiAdapter=evaluate(fs.readFileSync(new URL('../api/batch-deploy.ts',import.meta.url),'utf8'),{'./request':{post(path,body,config){assert.equal(path,'/batch-deploy/execute');assert.equal(config.timeout,0);assert.equal(config.silentError,true);posts++;return request.promise}}})
  const imports={
    '../components/ReleaseVersionInfo.vue':{},
    vue:{...vue,onMounted(){},onUnmounted:callback=>unmount.push(callback),watch(){}},
    'vue-router':{useRoute:()=>({query:{}}),useRouter:()=>({replace:async route=>{navigation.push(route)}})},
    'element-plus':{ElMessage:{success(){},warning(){},error(){}},ElMessageBox:{alert:async()=>{}}},
    '@element-plus/icons-vue':{},'../api/settings':{},'../api/pipeline':{},'../api/server-mgmt':{},
    '../utils/artifact-upload':{},...utilities,
    '../stores/settings':{useSettingsStore:()=>({loaded:true,fetchSettings:options.settings|| (async()=>{}),autoRollout:false})},
    '../api/batch-deploy':{
      executeBatchDeploy:apiAdapter.executeBatchDeploy,
      async getArtifactVersionPipelines(){reads++;if(readOverride)return readOverride();if(readError)throw readError;return{pipelines:rows.slice()}},
      getArtifactVersion:async()=>({version:{versionNo:'batch-test',sourceType:'upload',localDir:'/uploads/test'},items:[]}),
      listArtifactVersions:async()=>({versions:[]}),
    },
  }
  const view=evaluate(script,imports,{window:{setInterval(callback){const id=++timerID;timers.set(id,{callback,interval:true});return id},clearInterval(id){timers.delete(id)},setTimeout(callback){const id=++timerID;timers.set(id,{callback,interval:false});return id},clearTimeout(id){timers.delete(id)}}}).default.setup({}, {expose(){}})
  view.batchId.value='batch-test';view.sourceDir.value='/uploads/test';view.workflowStep.value=2
  view.matchResults.value=Array.from({length:100},(_,i)=>({fileName:`service-${i}.jar`,appId:i+1,appName:`service-${i}`,appType:'java',matched:true,valid:true,skipped:false}))
  view.selectedFiles.value=view.matchResults.value.map(row=>row.fileName)
  return{view,request,navigation,timers,get posts(){return posts},get reads(){return reads},set rows(value){rows=value},set readError(value){readError=value},set readOverride(value){readOverride=value},async tick(){const item=Array.from(timers.entries()).find(([,timer])=>!timer.interval);if(item){timers.delete(item[0]);await item[1].callback();await flush()}},dispose(){unmount.forEach(callback=>callback())}}
}

test('large submission remains visible, discovers accepted tasks and never submits twice',async()=>{
  const h=harness()
  try{
    const running=h.view.executeDeploy();await flush()
    assert.equal(h.posts,1);assert.equal(h.view.executing.value,true)
    await h.view.executeDeploy();assert.equal(h.posts,1)
    h.rows=pipelines(30,'IMAGE_READY');await h.tick()
    assert.equal(h.view.submittedIds.value.length,30)
    assert.equal(h.view.imagesComplete.value,false,'a partial list is not a completed submission')
    h.rows=pipelines(100)
    h.request.resolve({pipelines:pipelines(100),errors:[],consumedFiles:[]})
    await running;await flush()
    assert.equal(h.view.executing.value,false)
    assert.equal(h.view.submittedIds.value.length,100)
    const reads=h.reads
    await h.view.refreshExecution()
    assert.equal(h.reads,reads+1,'100 pipelines use one batched progress request')
    assert.ok(h.navigation.some(route=>route.query.pipelineIds?.split(',').length===100))
  }finally{h.dispose()}
})

test('lost submission reply recovers accepted IDs and does not automatically repeat the build',async()=>{
  const h=harness()
  try{
    const running=h.view.executeDeploy();await flush()
    h.rows=pipelines(3)
    h.request.reject(new Error('connection lost'))
    await running;await flush()
    assert.equal(h.view.submittedIds.value.length,3)
    assert.equal(h.view.submissionUncertain.value,true)
    assert.match(h.view.submissionError.value,/已找到 3 个任务/)
    await h.view.executeDeploy()
    assert.equal(h.posts,1)
  }finally{h.dispose()}
})

test('a delayed partial submission snapshot cannot overwrite the acknowledged full result',async()=>{
  const h=harness(),old=deferred();let first=true
  h.readOverride=()=>{if(first){first=false;return old.promise}return{pipelines:pipelines(100)}}
  try{
    const running=h.view.executeDeploy();await flush()
    h.request.resolve({pipelines:pipelines(100),errors:[],consumedFiles:[]})
    await running;await flush()
    old.resolve({pipelines:pipelines(30)})
    await flush()
    assert.equal(h.view.submittedIds.value.length,100)
    assert.equal(h.view.submittedPipelines.value.length,100)
  }finally{h.dispose()}
})

test('progress read failure retains already accepted tasks',async()=>{
  const h=harness()
  try{
    h.rows=pipelines(100)
    await h.view.recoverSubmittedPipelines()
    h.readError=new Error('database busy')
    await h.view.refreshExecution()
    assert.equal(h.view.executionError.value,true)
    assert.equal(h.view.submittedPipelines.value.length,100)
    assert.equal(h.view.submittedIds.value.length,100)
  }finally{h.dispose()}
})

test('a configuration read failure before submitting does not create uncertainty or send a build request',async()=>{
  const h=harness({settings:async()=>{throw new Error('configuration unavailable')}})
  try{
    await h.view.executeDeploy()
    assert.equal(h.posts,0)
    assert.equal(h.view.submissionUncertain.value,false)
    assert.match(h.view.submissionError.value,/本次尚未创建任务/)
    assert.equal(h.view.executing.value,false)
  }finally{h.dispose()}
})

test('leaving during a submission does not redirect when the server later replies',async()=>{
  const h=harness(),running=h.view.executeDeploy()
  await flush();h.dispose()
  h.request.resolve({pipelines:pipelines(100),errors:[],consumedFiles:[]})
  await running
  assert.equal(h.navigation.length,0)
  assert.equal(h.timers.size,0)
})
