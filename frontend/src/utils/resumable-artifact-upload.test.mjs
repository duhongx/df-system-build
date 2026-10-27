import { test } from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import vm from 'node:vm'
import { createHash, randomBytes, webcrypto } from 'node:crypto'
import { transformSync } from 'esbuild'

function evaluate(name, imports = {}, globals = {}) {
  const source = fs.readFileSync(new URL(`./${name}.ts`, import.meta.url), 'utf8')
  const module = { exports: {} }
  const { code } = transformSync(source, { loader: 'ts', format: 'cjs', target: 'es2020' })
  vm.runInNewContext(code, {
    module, exports: module.exports, TextEncoder, AbortController, crypto: webcrypto, atob,
    setTimeout, clearTimeout, Error, ...globals,
    require(name) { assert.ok(Object.hasOwn(imports, name), name); return imports[name] },
  })
  return module.exports
}
const sha = bytes => createHash('sha256').update(bytes).digest('hex')
const actualSHA = evaluate('upload-sha256').uploadSHA256
const createHasher = () => ({ hash: async blob => sha(Buffer.from(await blob.arrayBuffer())), dispose() {} })
const uploadImports = {
  '../api/batch-deploy': {}, './upload-sha256': { uploadSHA256: actualSHA },
  './upload-chunk-hasher': { createUploadChunkHasher: createHasher },
}
const { uploadArtifacts } = evaluate('artifact-upload', uploadImports)
const sourceEntries = (contents = 'abcdefghijklmnopqrstuvwx') => [{ file: new File([contents], 'service.jar', { lastModified: 123 }), path: 'release/service.jar' }]

test('version.json uses the same confirmed chunks and resume fingerprint as artifacts', async () => {
  const h = harness()
  const raw = '{"appVersion":"v1","readme":"business notes"}'
  const entries = [...sourceEntries(), { file: new File([raw], 'version.json', { lastModified: 456 }), path: 'release/version.json' }]
  await h.run(entries)
  const session = [...h.sessions.values()][0]
  const index = session.files.findIndex(file => file.path === 'release/version.json')
  assert.ok(index >= 0)
  const received = Buffer.concat(h.puts.filter(put => put.file === index).sort((a, b) => a.chunk - b.chunk).map(put => put.body)).toString()
  assert.equal(received, raw)
})

function harness(runUpload = uploadArtifacts) {
  const data = new Map(), sessions = new Map(), puts = [], progress = [], phases = [], retries = []
  const storage = { getItem: key => data.get(key) || null, setItem: (key, value) => data.set(key, value), removeItem: key => data.delete(key) }
  let active = 0, maxActive = 0, completes = 0, lostReply = false, pauseAfter = 0, controller
  let failAlways = false, permanentError = false
  const clone = value => JSON.parse(JSON.stringify(value))
  const api = {
    async createArtifactUpload(id, files) {
      if (!sessions.has(id)) sessions.set(id, {
        id, batchId: 'batch-test', chunkSize: 4, maxConcurrency: 6, status: 'uploading',
        receivedBytes: 0, totalBytes: files.reduce((sum, file) => sum + file.size, 0), completedFiles: 0,
        files: files.map(file => ({ ...file, chunks: Array(Math.ceil(file.size / 4)).fill('') })),
      })
      return clone(sessions.get(id))
    },
    async putArtifactUploadChunk(id, file, chunk, blob, digest, signal, onProgress) {
      assert.equal(blob.size <= 4, true)
      const body = Buffer.from(await blob.arrayBuffer())
      assert.equal(digest, sha(body))
      puts.push({ id, file, chunk, body })
      if (failAlways) throw new Error('disconnected')
      if (permanentError) throw Object.assign(new Error('forbidden'), { response: { status: 403 } })
      active++; maxActive = Math.max(maxActive, active)
      try {
        onProgress({ loaded: blob.size })
        await new Promise(resolve => setTimeout(resolve, 2))
        if (signal.aborted) throw new Error('request aborted')
        const s = sessions.get(id)
        if (!s.files[file].chunks[chunk]) { s.receivedBytes += blob.size; s.files[file].chunks[chunk] = digest }
        if (pauseAfter && s.receivedBytes >= pauseAfter) { pauseAfter = 0; controller.abort() }
        if (lostReply) { lostReply = false; throw new Error('reply lost after commit') }
        return { receivedBytes: s.receivedBytes }
      } finally { active-- }
    },
    async completeArtifactUpload(id) {
      completes++
      const s = sessions.get(id)
      assert.equal(s.receivedBytes, s.totalBytes)
      s.status = 'processing'
      return clone(s)
    },
    async getArtifactUpload(id) {
      const s = sessions.get(id)
      s.status = 'completed'
      s.result = { batchId: s.batchId, count: s.files.length, failed: [], success: s.files.map(file => file.path) }
      return clone(s)
    },
    async cancelArtifactUpload(id) { sessions.delete(id) },
  }
  return {
    data, sessions, puts, progress, phases, retries, api,
    get maxActive() { return maxActive }, get completes() { return completes },
    set lostReply(value) { lostReply = value }, set pauseAfter(value) { pauseAfter = value },
    set failAlways(value) { failAlways = value }, set permanentError(value) { permanentError = value },
    run(entries = sourceEntries(), options = {}) {
      controller = new AbortController()
      return runUpload(entries, {
        signal: controller.signal, api, storage, createHasher, wait: async () => {},
        onProgress: value => progress.push(value), onPhase: (phase, detail) => phases.push({ phase, detail }),
        onSnapshot() {}, onRetry: value => retries.push(value), ...options,
      })
    },
  }
}

test('HTTP SHA256 fallback matches standard vectors and an 8 MiB chunk', () => {
  for (const length of [0, 1, 55, 56, 63, 64, 65, 1000, 8 * 1024 * 1024]) {
    const bytes = randomBytes(length)
    assert.equal(actualSHA(bytes), sha(bytes), `length=${length}`)
  }
})

test('hashing worker supports both intranet HTTP fallback and Web Crypto', async () => {
  for (const crypto of [undefined, webcrypto]) {
    let reply
    const self = { crypto, postMessage: value => { reply = value } }
    evaluate('upload-hash.worker', { './upload-sha256': { uploadSHA256: actualSHA } }, { self })
    await self.onmessage({ data: new Blob(['worker-content']) })
    assert.equal(reply.digest, sha('worker-content'))
  }
})

test('parallel chunks are bounded and progress advances only after server acknowledgement', async () => {
  const h = harness()
  await h.run()
  assert.equal(h.maxActive, 3)
  assert.equal(h.puts.length, 6)
  assert.ok(h.progress.some(item => item.inFlightBytes > 0 && item.confirmedBytes === 0))
  assert.equal(h.progress.at(-1).confirmedBytes, 24)
  assert.equal(h.progress.at(-1).transferredBytes, 24)
  assert.equal(h.data.size, 0, 'completed session removed from resume storage')
  assert.equal(h.completes, 1)
  assert.ok(h.phases.some(item => item.phase === 'processing'))
})

test('lost acknowledgement retries only that chunk and never doubles confirmed bytes', async () => {
  const h = harness(); h.lostReply = true
  await h.run(sourceEntries(), { concurrency: 1 })
  assert.equal(h.puts.filter(item => item.chunk === 0).length, 2)
  assert.equal(h.puts.length, 7)
  assert.equal(h.progress.at(-1).confirmedBytes, 24)
  assert.equal(h.progress.at(-1).transferredBytes, 28)
  assert.equal(h.retries.length, 1)
})

test('pause and reselect same files reuse durable chunks instead of starting another batch', async () => {
  const h = harness(); h.pauseAfter = 8
  await assert.rejects(h.run(sourceEntries(), { concurrency: 1 }), /暂停/)
  assert.equal(h.data.size, 1)
  const id = h.puts[0].id
  const calls = h.puts.length
  await h.run(sourceEntries(), { concurrency: 1 })
  assert.ok(h.puts.slice(calls).every(item => item.id === id && item.chunk >= 2))
  assert.equal(h.puts.length, 6)
  assert.ok(h.phases.some(item => item.detail.includes('复用 2 个')))
})

test('same name, size and timestamp cannot resume changed file contents', async () => {
  const h = harness(); h.pauseAfter = 4
  await assert.rejects(h.run(sourceEntries(), { concurrency: 1 }), /暂停/)
  const calls = h.puts.length
  await assert.rejects(h.run(sourceEntries('ABCDEFGHIJKLMNOPQRSTUVWX')), /本地文件内容已变化/)
  assert.equal(h.puts.length, calls, 'changed file must be caught before any missing chunk is sent')
  assert.equal(h.completes, 0)
})

test('explicit restart cleans the previous upload and creates a new identity', async () => {
  const h = harness(); h.pauseAfter = 4
  await assert.rejects(h.run(sourceEntries(), { concurrency: 1 }), /暂停/)
  const oldID = h.puts[0].id
  const calls = h.puts.length
  await h.run(sourceEntries(), { restart: true })
  assert.equal(h.sessions.has(oldID), false)
  assert.ok(h.puts.slice(calls).every(item => item.id !== oldID))
})

test('retry exhaustion retains resumable state and authorization failure is not retried', async () => {
  const h = harness(); h.failAlways = true
  await assert.rejects(h.run(sourceEntries(), { concurrency: 1 }), /disconnected/)
  assert.equal(h.puts.length, 4)
  assert.equal(h.retries.length, 3)
  assert.equal(h.data.size, 1)
  h.failAlways = false; h.permanentError = true
  const calls = h.puts.length
  await assert.rejects(h.run(sourceEntries(), { concurrency: 1 }), /forbidden/)
  assert.equal(h.puts.length - calls, 1)
})

test('a stalled request is aborted after inactivity and stops after three retries', async () => {
  let nextTimer = 0, attempts = 0
  const timers = new Map()
  const engine = evaluate('artifact-upload', uploadImports, {
    setTimeout(callback, ms) { assert.equal(ms, 90_000); const id = ++nextTimer; timers.set(id, callback); return id },
    clearTimeout(id) { timers.delete(id) },
  }).uploadArtifacts
  const h = harness(engine)
  h.api.putArtifactUploadChunk = async (id, file, chunk, body, digest, signal) => {
    attempts++
    return new Promise((resolve, reject) => signal.addEventListener('abort', () => reject(new Error('aborted')), { once: true }))
  }
  const outcome = h.run(sourceEntries(), { concurrency: 1 }).then(() => null, error => error)
  for (let i = 0; i < 4; i++) {
    for (let turn = 0; turn < 50; turn++) await Promise.resolve()
    assert.equal(timers.size, 1)
    const [id, callback] = timers.entries().next().value
    timers.delete(id); callback()
  }
  assert.match((await outcome).message, /90 秒/)
  assert.equal(attempts, 4)
  assert.equal(h.retries.length, 3)
  assert.equal(timers.size, 0)
  assert.equal(h.data.size, 1)
})

test('reconnecting during server validation polls without retransmitting or creating another version', async () => {
  const h = harness()
  let first = true
  const get = h.api.getArtifactUpload
  h.api.getArtifactUpload = async id => { if (first) throw Object.assign(new Error('unavailable'), { response: { status: 403 } }); return get(id) }
  await assert.rejects(h.run(), /unavailable/)
  assert.equal(h.completes, 1)
  const calls = h.puts.length
  first = false
  await h.run()
  assert.equal(h.puts.length, calls)
  assert.equal(h.completes, 1)
})

test('duplicate basenames are rejected before creating a server session', async () => {
  const h = harness(), entries = sourceEntries()
  entries.push({ file: new File(['other'], 'SERVICE.jar'), path: 'another/SERVICE.jar' })
  await assert.rejects(h.run(entries), /同名制品/)
  assert.equal(h.sessions.size, 0)
})
