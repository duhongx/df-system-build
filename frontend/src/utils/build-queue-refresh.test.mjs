import { test } from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import vm from 'node:vm'
import { parse, compileScript } from '@vue/compiler-sfc'
import { transformSync } from 'esbuild'
import * as vue from 'vue'

const { descriptor } = parse(fs.readFileSync(new URL('../views/BuildQueueView.vue', import.meta.url), 'utf8'))
const script = compileScript(descriptor, { id: 'queue-refresh-test' }).content
const deferred = () => { let resolve, reject; const promise = new Promise((a,b) => { resolve=a; reject=b }); return { promise, resolve, reject } }
function harness(options = {}) {
  const module = { exports: {} }, timers = new Map(), unmount = []
  let timerID = 0, queueCalls = 0, batchCalls = 0
  const imports = {
    vue: { ...vue, onMounted() {}, onUnmounted: callback => unmount.push(callback) },
    'vue-router': { useRouter: () => ({ push() {} }) },
    'element-plus': {}, '../utils/time': {},
    '../api/pipeline': { getBuildQueue() { queueCalls++; return options.queue() } },
    '../api/batch-deploy': { listArtifactDeployBatches(_status, _ready, silent) { assert.equal(silent, true); batchCalls++; return options.batches() } },
  }
  const { code } = transformSync(script, { loader: 'ts', format: 'cjs' })
  vm.runInNewContext(code, { module, exports: module.exports, Error,
    setTimeout(callback) { const id=++timerID; timers.set(id,callback); return id }, clearTimeout(id) { timers.delete(id) },
    require(name) { assert.ok(Object.hasOwn(imports,name),name); return imports[name] },
  })
  return { view: module.exports.default.setup({}, { expose() {} }), timers, get queueCalls() { return queueCalls }, get batchCalls() { return batchCalls }, dispose() { unmount.forEach(callback => callback()) } }
}

test('one failed section does not erase healthy queue data or previous successful results', async () => {
  let queueFails = false, batchFails = false
  const h = harness({ queue: async () => { if(queueFails) throw new Error('queue timeout'); return { running: [{id:1}], pending:[{id:2}] } }, batches: async () => { if(batchFails) throw new Error('batch timeout'); return { batches:[{id:3}] } } })
  try {
    await h.view.load()
    batchFails = true
    await h.view.load()
    assert.equal(h.view.runningTasks.value[0].id,1)
    assert.equal(h.view.queuedTasks.value[0].id,2)
    assert.equal(h.view.deployBatches.value[0].id,3)
    assert.match(h.view.batchError.value,/batch timeout/)
    assert.equal(h.view.queueError.value,'')
    queueFails = true; batchFails = false
    await h.view.load()
    assert.match(h.view.queueError.value,/queue timeout/)
    assert.equal(h.view.batchError.value,'')
    assert.equal(h.view.runningTasks.value.length,1)
    assert.equal(h.view.deployBatches.value.length,1)
  } finally { h.dispose() }
})

test('slow refresh never overlaps another request and schedules only after completion', async () => {
  const queue = deferred(), batches = deferred()
  const h = harness({ queue: () => queue.promise, batches: () => batches.promise })
  try {
    const first = h.view.load()
    await h.view.load()
    assert.equal(h.queueCalls,1); assert.equal(h.batchCalls,1)
    assert.equal(h.timers.size,0)
    queue.resolve({running:[],pending:[]}); batches.resolve({batches:[]})
    await first
    assert.equal(h.timers.size,1)
    assert.equal(h.view.refreshing.value,false)
  } finally { h.dispose() }
})

test('leaving the queue during a request neither overwrites state nor starts another refresh', async () => {
  const queue = deferred(), batches = deferred()
  const h = harness({ queue: () => queue.promise, batches: () => batches.promise })
  const first = h.view.load()
  h.dispose()
  queue.resolve({running:[{id:1}],pending:[]}); batches.resolve({batches:[{id:2}]})
  await first
  assert.equal(h.view.runningTasks.value.length,0)
  assert.equal(h.view.deployBatches.value.length,0)
  assert.equal(h.timers.size,0)
})
