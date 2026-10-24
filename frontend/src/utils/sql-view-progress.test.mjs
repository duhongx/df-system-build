import { test, mock } from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs/promises'
import { transform } from 'esbuild'
const source = await fs.readFile(new URL('./sql-view-progress.ts', import.meta.url), 'utf8')
const { code } = await transform(source, { loader: 'ts', format: 'esm' })
const { createViewTaskPoller, viewStageProgress } = await import('data:text/javascript;base64,' + Buffer.from(code).toString('base64'))
const flush = async () => { for (let i = 0; i < 5; i++) await Promise.resolve() }
const deferred = () => { let resolve, reject; const promise = new Promise((a,b) => {resolve=a;reject=b});return { promise, resolve, reject } }

test('progress counts completed work rather than all planned steps', () => {
 const steps=['SUCCESS','RUNNING','PENDING'].map(status=>({stepName:'ALTER_TABLE',status}))
 assert.deepEqual(viewStageProgress(steps,['ALTER_TABLE']), {status:'running',completed:1,skipped:0,count:3,error:''})
 steps[1].status='PENDING';assert.equal(viewStageProgress(steps,['ALTER_TABLE']).status,'running')
 steps[1].status='FAILED';steps[1].errorMessage='cast failed';assert.equal(viewStageProgress(steps,['ALTER_TABLE']).error,'cast failed')
 assert.equal(viewStageProgress([{stepName:'ALTER_TABLE',status:'SKIPPED'}],['ALTER_TABLE']).status,'skipped')
 const recovered=viewStageProgress([{stepName:'RESTORE_OBJECT',status:'SUCCESS'},{stepName:'RESTORE_OBJECT',status:'SKIPPED'}],['RESTORE_OBJECT'])
 assert.equal(recovered.status,'success');assert.equal(recovered.skipped,1)
})
test('polling never overlaps, retries failures and stops at terminal state', async () => {
 mock.timers.enable({apis:['setTimeout']})
 try {
  let pending=deferred();let calls=0;const values=[],errors=[]
  const poller=createViewTaskPoller(()=>{calls++;return pending.promise},v=>{values.push(v);if(v==='SUCCESS')poller.stop()},e=>errors.push(e),10)
  poller.start(1);mock.timers.tick(100);assert.equal(calls,1)
  pending.reject('network');await flush();assert.deepEqual(errors,['network'])
  pending=deferred();mock.timers.tick(10);assert.equal(calls,2)
  pending.resolve('RUNNING');await flush();assert.deepEqual(values,['RUNNING'])
  pending=deferred();mock.timers.tick(10);assert.equal(calls,3)
  pending.resolve('SUCCESS');await flush();mock.timers.tick(100);assert.equal(calls,3)
 } finally { mock.timers.reset() }
})
test('old task and unmounted page cannot overwrite current progress', async () => {
 mock.timers.enable({apis:['setTimeout']})
 try {
  const old=deferred(),current=deferred(),values=[]
  const poller=createViewTaskPoller(id=>id===1?old.promise:current.promise,v=>values.push(v),()=>{},10)
  poller.start(1);poller.start(2);old.resolve('stale');await flush();assert.deepEqual(values,[])
  poller.stop();current.resolve('unmounted');await flush();assert.deepEqual(values,[])
  mock.timers.tick(100)
 } finally { mock.timers.reset() }
})
