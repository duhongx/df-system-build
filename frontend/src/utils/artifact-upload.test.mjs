import { test } from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import vm from 'node:vm'
import { parse, compileScript } from '@vue/compiler-sfc'
import { transformSync } from 'esbuild'
import * as vue from 'vue'

// Exercise the actual SFC setup and API adapter with deferred HTTP responses.
// No browser, server, or production artifact is needed for these regressions.
const viewSource = fs.readFileSync(new URL('../views/BatchDeployView.vue', import.meta.url), 'utf8')
const { descriptor } = parse(viewSource)
const viewScript = compileScript(descriptor, { id: 'artifact-upload-test' }).content
const apiSource = fs.readFileSync(new URL('../api/batch-deploy.ts', import.meta.url), 'utf8')
const utilitySources = Object.fromEntries(['update-task-workflow', 'batch-deploy-selection', 'upload-activity'].map(name => [
  `../utils/${name}`, fs.readFileSync(new URL(`./${name}.ts`, import.meta.url), 'utf8'),
]))

function evaluate(source, imports = {}, globals = {}) {
  const module = { exports: {} }
  const { code } = transformSync(source, { loader: 'ts', format: 'cjs', target: 'es2020' })
  vm.runInNewContext(code, {
    module, exports: module.exports, FormData, Error, AbortController, ...globals,
    require(name) {
      assert.ok(Object.hasOwn(imports, name), `unexpected dependency: ${name}`)
      return imports[name]
    },
  })
  return module.exports
}

function deferred() {
  let resolve, reject
  const promise = new Promise((a, b) => { resolve = a; reject = b })
  return { promise, resolve, reject }
}
const flush = async () => { for (let i = 0; i < 12; i++) await Promise.resolve() }
const accepted = { count: 2, batchId: 'upload-test-version', success: ['service.jar', 'web.zip'], failed: [] }
const files = () => [
  { file: new File(['service'], 'service.jar'), path: 'release/java/service.jar' },
  { file: new File(['web'], 'web.zip'), path: 'release/web/web.zip' },
  { file: new File(['skip'], 'readme.txt'), path: 'release/readme.txt' },
]

function harness(options = {}) {
  const uploads = [], navigation = [], messages = [], unmount = []
  let clock = 1000, timerID = 0
  const timers = new Map()
  class UploadDate extends Date { static now() { return clock } }
  let detailCalls = 0
  const api = evaluate(apiSource, { './request': {
    post(path, data, config) {
      assert.equal(path, '/batch-deploy/upload')
      assert.equal(config.timeout, 0, 'large uploads must not inherit the 30-second API timeout')
      const response = deferred()
      uploads.push({ data, progress: config.onUploadProgress, ...response })
      return response.promise
    },
  } })
  const imports = {
    '../components/ReleaseVersionInfo.vue': {},
    vue: { ...vue, onMounted() {}, watch() {}, onUnmounted(callback) { unmount.push(callback) } },
    'vue-router': { useRoute: () => ({ query: {} }), useRouter: () => ({ replace: async route => { navigation.push(route) } }) },
    'element-plus': { ElMessage: Object.fromEntries(['warning', 'success', 'error'].map(type => [type, text => messages.push({ type, text })])) },
    '@element-plus/icons-vue': {},
    '../api/batch-deploy': {
      uploadBatchArtifacts: api.uploadBatchArtifacts,
      getArtifactVersion: async () => {
        detailCalls++
        if (options.detail) return options.detail()
        return { version: { versionNo: accepted.batchId, sourceType: 'upload', localDir: '/uploads/test' }, items: [] }
      },
      listArtifactVersions: options.versions || (async () => ({ versions: [] })),
    },
    '../utils/artifact-upload': {
      uploadArtifacts(entries, options) {
        const response = deferred()
        uploads.push({ entries, ...response, progress(event) {
          options.onPhase('uploading', '')
          options.onProgress({ confirmedBytes: event.loaded, totalBytes: event.total, transferredBytes: event.networkLoaded ?? event.loaded, inFlightBytes: event.inFlightBytes || 0, completedFiles: 0, currentFiles: [] })
          if (event.total && event.loaded >= event.total) options.onPhase('processing', '')
        } })
        options.signal.addEventListener('abort', () => response.reject(new Error('paused')), { once: true })
        return response.promise
      },
    },
    '../api/settings': {}, '../api/pipeline': {},
    '../stores/settings': { useSettingsStore: () => ({ loaded: true }) },
    ...Object.fromEntries(Object.entries(utilitySources).map(([path, source]) => [path, evaluate(source)])),
  }
  const view = evaluate(viewScript, imports, { Date: UploadDate, window: {
    setInterval(callback) { const id = ++timerID; timers.set(id, callback); return id },
    clearInterval(id) { timers.delete(id) },
  } }).default.setup({}, { expose() {} })
  return { view, uploads, navigation, messages, timers, get detailCalls() { return detailCalls }, advance(ms) { clock += ms; for (const callback of timers.values()) callback() }, dispose() { unmount.forEach(callback => callback()) } }
}

test('directory upload reports bytes, then server processing and application matching', async () => {
  const detail = deferred()
  const h = harness({ detail: () => detail.promise })
  const v = h.view
  const running = v.uploadEntries(files())
  assert.equal(v.uploadLoading.value, true)
  assert.equal(v.uploadProgress.value.fileCount, 2)
  assert.equal(v.uploadProgress.value.fileBytes, 10)
  assert.deepEqual(Array.from(h.uploads[0].entries, entry => entry.path), ['release/java/service.jar', 'release/web/web.zip'])
  h.uploads[0].progress({ loaded: 40, total: 100 })
  assert.equal(v.uploadPercent.value, 40)
  h.uploads[0].progress({ loaded: 100, total: 100 })
  assert.equal(v.uploadProgress.value.phase, 'processing')
  assert.match(v.uploadStatusText.value, /校验制品/)
  assert.equal(v.uploadLoading.value, true, '100% transmitted is not a completed server response')
  h.uploads[0].resolve(accepted)
  await flush()
  assert.equal(v.uploadProgress.value.phase, 'matching')
  h.uploads[0].progress({ loaded: 40, total: 100 })
  assert.equal(v.uploadPercent.value, 100, 'late transport events cannot revert the processing phase')
  detail.resolve({ version: { versionNo: accepted.batchId, sourceType: 'upload', localDir: '/uploads/test' }, items: [] })
  await running
  assert.equal(v.uploadLoading.value, false)
  assert.equal(v.workflowStep.value, 1)
  assert.equal(h.navigation[0].query.batchId, accepted.batchId)
})

test('directory upload retains version.json and shows business metadata after import', async () => {
  const version = { versionNo: accepted.batchId, sourceType: 'upload', appVersion: '2.15.3', releaseReadme: '门诊修复' }
  const h = harness({ detail: async () => ({ version, items: [] }) })
  const entries = [...files(), { file: new File(['{"appVersion":"2.15.3"}'], 'version.json'), path: 'release/version.json' }]
  const running = h.view.uploadEntries(entries)
  assert.ok(h.uploads[0].entries.some(entry => entry.path === 'release/version.json'))
  h.uploads[0].resolve(accepted)
  await running
  assert.equal(h.view.releaseVersion.value.appVersion, '2.15.3')
  assert.equal(h.view.releaseVersion.value.releaseReadme, '门诊修复')
})

test('business-only version.json creates a readable record without reporting a failed artifact upload', async () => {
  const h = harness({ detail: async () => ({ version: { versionNo: accepted.batchId, appVersion: 'v1' }, items: [] }) })
  const running = h.view.uploadEntries([{ file: new File(['{"appVersion":"v1"}'], 'version.json'), path: 'version.json' }])
  h.uploads[0].resolve({ ...accepted, count: 0, success: [] })
  await running
  assert.equal(h.view.uploadProgress.value.phase, 'success')
  assert.equal(h.view.releaseVersion.value.appVersion, 'v1')
  assert.equal(h.navigation[0].query.batchId, accepted.batchId)
})

test('unknown transfer size stays indeterminate instead of inventing a percentage', async () => {
  const h = harness()
  const running = h.view.uploadEntries(files())
  h.uploads[0].progress({ loaded: 50 })
  assert.equal(h.view.uploadProgress.value.total, undefined)
  assert.equal(h.view.uploadProgress.value.loaded, 50)
  assert.equal(h.view.uploadPercent.value, 0)
  h.uploads[0].resolve(accepted)
  await running
})

test('network failure remains visible and selecting the same directory can retry', async () => {
  const h = harness()
  const input = { files: [new File(['service'], 'service.jar')], value: 'service.jar' }
  const running = h.view.handleUploadInput({ target: input })
  h.uploads[0].reject(new Error('network disconnected'))
  await running
  assert.equal(h.view.uploadProgress.value.phase, 'failed')
  assert.match(h.view.uploadProgress.value.error, /network disconnected/)
  assert.equal(h.view.uploadLoading.value, false)
  assert.equal(input.value, '')
  const retry = h.view.uploadEntries(files())
  assert.equal(h.uploads.length, 2)
  h.uploads[1].resolve(accepted)
  await retry
  assert.equal(h.view.workflowStep.value, 1)
})

test('all files rejected by the server show their reasons and do not enter matching', async () => {
  const h = harness()
  const running = h.view.uploadEntries(files())
  h.uploads[0].resolve({ count: 0, failed: [{ fileName: 'service.jar', error: '文件损坏' }] })
  await running
  assert.equal(h.view.uploadProgress.value.phase, 'failed')
  assert.match(h.view.uploadProgress.value.error, /service.jar：文件损坏/)
  assert.equal(h.detailCalls, 0)
  assert.equal(h.view.workflowStep.value, 0)
})

test('accepted upload is retained when application matching cannot be read', async () => {
  const h = harness({ detail: async () => { throw new Error('detail unavailable') } })
  const running = h.view.uploadEntries(files())
  h.uploads[0].resolve(accepted)
  await running
  assert.equal(h.view.uploadProgress.value.batchId, accepted.batchId)
  assert.match(h.view.uploadStatusText.value, /文件已上传/)
  assert.match(h.view.uploadProgress.value.error, /无需重新上传/)
  assert.equal(h.navigation.length, 0)
})

test('history refresh failure does not misreport a successful upload', async () => {
  const h = harness({ versions: async () => { throw new Error('history unavailable') } })
  const running = h.view.uploadEntries(files())
  h.uploads[0].resolve(accepted)
  await running
  assert.equal(h.view.workflowStep.value, 1)
  assert.notEqual(h.view.uploadProgress.value.phase, 'failed')
})

test('in-flight upload cannot be reset or submitted twice', async () => {
  const h = harness()
  const running = h.view.uploadEntries(files())
  const progress = h.view.uploadProgress.value
  h.view.resetSelection()
  await h.view.uploadEntries(files())
  assert.equal(h.view.uploadProgress.value, progress)
  assert.equal(h.uploads.length, 1)
  h.uploads[0].resolve(accepted)
  await running
})

test('leaving the page before upload completes does not redirect to matching', async () => {
  const h = harness()
  const running = h.view.uploadEntries(files())
  h.dispose()
  h.uploads[0].progress({ loaded: 100, total: 100 })
  h.uploads[0].resolve(accepted)
  await running
  assert.equal(h.navigation.length, 0)
  assert.equal(h.detailCalls, 0)
  assert.equal(h.timers.size, 0)
})

test('11.5 GB upload shows decimal progress and bytes even while the integer stays at 31%', async () => {
  const h = harness(), v = h.view
  const running = v.uploadEntries(files())
  const gb = 1024 ** 3
  h.advance(12_000)
  h.uploads[0].progress({ loaded: 3.6 * gb, total: 11.5 * gb })
  assert.equal(v.uploadPercent.value, 31.3)
  assert.ok(v.uploadActivity.value.recentBps > 0)
  h.advance(5000)
  h.uploads[0].progress({ loaded: 3.6 * gb + 1024 ** 2, total: 11.5 * gb })
  assert.equal(v.uploadPercent.value, 31.3)
  assert.equal(v.uploadProgress.value.loaded, 3.6 * gb + 1024 ** 2)
  assert.ok(v.uploadRemainingSeconds.value > 0)
  h.uploads[0].resolve(accepted)
  await running
  assert.equal(h.timers.size, 0)
})

test('ten minutes without bytes shows a stall, zero recent speed and no invented progress', async () => {
  const h = harness(), v = h.view
  const running = v.uploadEntries(files())
  h.advance(10_000)
  h.uploads[0].progress({ loaded: 3.6 * 1024 ** 3, total: 11.5 * 1024 ** 3 })
  h.advance(600_000)
  assert.equal(v.uploadPercent.value, 31.3)
  assert.equal(v.uploadActivity.value.idleMs, 600_000)
  assert.equal(v.uploadActivity.value.elapsedMs, 610_000)
  assert.equal(v.uploadActivity.value.recentBps, 0)
  assert.equal(v.uploadActivity.value.remainingSeconds, undefined)
  assert.equal(v.uploadActivity.value.stalled, true)
  h.uploads[0].progress({ loaded: 3.6 * 1024 ** 3 + 1024, total: 11.5 * 1024 ** 3 })
  assert.equal(v.uploadActivity.value.stalled, false)
  h.uploads[0].reject(new Error('network disconnected'))
  await running
  assert.equal(h.timers.size, 0)
  const elapsed = v.uploadActivity.value.elapsedMs
  h.advance(60_000)
  assert.equal(v.uploadActivity.value.elapsedMs, elapsed)
})

test('server processing has its own waiting time and is not diagnosed as a stalled transfer', async () => {
  const h = harness(), v = h.view
  const running = v.uploadEntries(files())
  h.advance(4000)
  h.uploads[0].progress({ loaded: 100, total: 100 })
  const average = v.uploadActivity.value.averageBps
  h.advance(120_000)
  assert.equal(v.uploadProgress.value.phase, 'processing')
  assert.equal(v.uploadPercent.value, 100)
  assert.equal(v.uploadActivity.value.stalled, false)
  assert.equal(v.uploadActivity.value.averageBps, average)
  assert.equal(v.uploadPhaseWait.value, 120)
  h.uploads[0].resolve(accepted)
  await running
  assert.equal(v.uploadProgress.value.phase, 'success')
})

test('bytes in flight do not advance confirmed progress and pause offers a safe continuation', async () => {
  const h = harness(), v = h.view
  const running = v.uploadEntries(files())
  h.advance(4000)
  h.uploads[0].progress({ loaded: 0, total: 10, networkLoaded: 5, inFlightBytes: 5 })
  assert.equal(v.uploadPercent.value, 0)
  assert.equal(v.uploadProgress.value.inFlightBytes, 5)
  assert.ok(v.uploadActivity.value.recentBps > 0)
  v.pauseUpload()
  await running
  assert.equal(v.uploadProgress.value.phase, 'paused')
  const resume = v.retryUpload()
  assert.equal(h.uploads.length, 2)
  assert.deepEqual(h.uploads[1].entries, h.uploads[0].entries)
  h.uploads[1].resolve(accepted)
  await resume
  assert.equal(v.uploadProgress.value.phase, 'success')
})

test('chunk API sends bounded raw bodies with checksum, cancellation and silent retry errors', async () => {
  let call
  const api = evaluate(apiSource, { './request': { put(...args) { call = args; return Promise.resolve({ receivedBytes: 5 }) } } })
  const body = new Blob(['chunk']), controller = new AbortController()
  await api.putArtifactUploadChunk('abc', 2, 3, body, 'checksum', controller.signal, () => {})
  assert.equal(call[0], '/batch-deploy/uploads/abc/files/2/chunks/3')
  assert.equal(call[1], body)
  assert.equal(call[2].timeout, 0)
  assert.equal(call[2].silentError, true)
  assert.equal(call[2].signal, controller.signal)
  assert.equal(call[2].headers['Content-Type'], 'application/octet-stream')
  assert.equal(call[2].headers['X-Chunk-SHA256'], 'checksum')
})
