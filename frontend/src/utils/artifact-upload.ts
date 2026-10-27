import {
  createArtifactUpload, getArtifactUpload, putArtifactUploadChunk, completeArtifactUpload, cancelArtifactUpload,
  type ArtifactUploadSession, type ArtifactUploadFile, type BatchUploadResult,
} from '../api/batch-deploy'
import { uploadSHA256 } from './upload-sha256'
import { createUploadChunkHasher } from './upload-chunk-hasher'

export type UploadEntry = { file: File; path: string }
export interface ArtifactUploadProgress {
  confirmedBytes: number
  totalBytes: number
  transferredBytes: number
  inFlightBytes: number
  completedFiles: number
  currentFiles: string[]
}
type UploadPhase = 'preparing' | 'uploading' | 'processing'
const defaultAPI = { createArtifactUpload, getArtifactUpload, putArtifactUploadChunk, completeArtifactUpload, cancelArtifactUpload }
interface UploadOptions {
  signal: AbortSignal
  concurrency?: number
  restart?: boolean
  onProgress: (progress: ArtifactUploadProgress) => void
  onPhase: (phase: UploadPhase, detail: string) => void
  onSnapshot: (session: ArtifactUploadSession) => void
  onRetry: (detail: string) => void
  // Dependency injection keeps retry/recovery tests independent of real servers.
  api?: typeof defaultAPI
  storage?: Pick<Storage, 'getItem' | 'setItem' | 'removeItem'>
  createHasher?: typeof createUploadChunkHasher
  wait?: (ms: number, signal: AbortSignal) => Promise<void>
}

const volatileSessions = new Map<string, string>()
function browserStorage() {
  try { return localStorage } catch { return undefined }
}
function getStored(storage: UploadOptions['storage'], key: string) {
  try { return storage?.getItem(key) || volatileSessions.get(key) } catch { return volatileSessions.get(key) }
}
function setStored(storage: UploadOptions['storage'], key: string, value?: string) {
  if (value) volatileSessions.set(key, value)
  else volatileSessions.delete(key)
  try { if (value) storage?.setItem(key, value); else storage?.removeItem(key) } catch { /* private browsing: this tab still resumes */ }
}
function userScope(storage: UploadOptions['storage']) {
  try {
    const payload = storage?.getItem('df-token')?.split('.')[1]
    if (payload) return String(JSON.parse(atob(payload.replace(/-/g, '+').replace(/_/g, '/'))).userId || 'anonymous')
  } catch { /* unauthenticated requests are rejected by the server */ }
  return 'anonymous'
}
function newUploadID() {
  return Array.from(crypto.getRandomValues(new Uint8Array(16)), value => value.toString(16).padStart(2, '0')).join('')
}
function throwIfAborted(signal: AbortSignal) {
  if (signal.aborted) throw new Error('上传已暂停，已确认的分片已保留')
}
function wait(ms: number, signal: AbortSignal): Promise<void> {
  return new Promise((resolve, reject) => {
    const abort = () => { clearTimeout(timer); reject(new Error('上传已暂停')) }
    const timer = setTimeout(() => { signal.removeEventListener('abort', abort); resolve() }, ms)
    if (signal.aborted) abort()
    else signal.addEventListener('abort', abort, { once: true })
  })
}
function retryable(error: unknown) {
  const status = (error as { response?: { status?: number } })?.response?.status
  return !status || status === 408 || status === 429 || status >= 500
}

export async function uploadArtifacts(entries: UploadEntry[], options: UploadOptions): Promise<BatchUploadResult> {
  const api = options.api || defaultAPI
  const storage = options.storage || browserStorage()
  const delay = options.wait || wait
  const controller = new AbortController()
  const signal = controller.signal
  const externalAbort = () => controller.abort()
  if (options.signal.aborted) controller.abort()
  else options.signal.addEventListener('abort', externalAbort, { once: true })
  const hashers: ReturnType<typeof createUploadChunkHasher>[] = []
  let failure: unknown
  try {
    throwIfAborted(signal)
    const sorted = [...entries].sort((a, b) => a.path < b.path ? -1 : a.path > b.path ? 1 : 0)
    const names = new Set<string>()
    const files: ArtifactUploadFile[] = sorted.map(entry => {
      const name = entry.path.replace(/\\/g, '/').split('/').pop() || ''
      const key = name.toLowerCase()
      const manifest = name === 'version.json'
      if ((!manifest && !/\.(jar|zip)$/i.test(name)) || entry.file.size <= 0) throw new Error(`文件格式或大小无效：${entry.path}`)
      if (manifest && entry.file.size > 1024 * 1024) throw new Error('version.json 不能超过 1 MiB')
      if (names.has(key)) throw new Error(`目录内存在同名制品文件：${name}`)
      names.add(key)
      return { path: entry.path, size: entry.file.size, lastModified: entry.file.lastModified }
    })
    if (!files.length) throw new Error('请选择制品文件')
    const fingerprint = uploadSHA256(new TextEncoder().encode(JSON.stringify(files)))
    const key = `df-artifact-upload-v1:${userScope(storage)}:${fingerprint}`
    let id = getStored(storage, key)
    if (id && !/^[0-9a-f]{32}$/.test(id)) id = undefined
    if (options.restart && id) {
      // Do not silently abandon gigabytes on disk. If cleanup cannot be confirmed,
      // preserve the old session so the user can retry rather than lose its handle.
      await api.cancelArtifactUpload(id, signal)
      setStored(storage, key)
      id = undefined
    }
    id ||= newUploadID()
    setStored(storage, key, id)
    async function retry<T>(operation: () => Promise<T>, label: string): Promise<T> {
      for (let attempt = 0; ; attempt++) {
        throwIfAborted(signal)
        try { return await operation() } catch (error) {
          throwIfAborted(signal)
          if (attempt >= 3 || !retryable(error)) throw error
          options.onRetry(`${label}，${2 ** attempt} 秒后第 ${attempt + 1}/3 次重试`)
          await delay(1000 * 2 ** attempt, signal)
        }
      }
    }
    options.onPhase('preparing', '读取服务端已接收的分片…')
    let session: ArtifactUploadSession
    try { session = await retry(() => api.createArtifactUpload(id!, files, signal), '连接失败') } catch (error) {
      if ((error as { response?: { status?: number } })?.response?.status === 404) {
        throw new Error('服务端尚未支持分片上传，请先部署新版 dfops')
      }
      throw error
    }
    options.onSnapshot(session)
    if (session.status === 'canceled') throw new Error('本次上传已取消，请重新上传')
    if (!Number.isSafeInteger(session.chunkSize) || session.chunkSize <= 0 || session.chunkSize > 64 * 1024 * 1024 || session.files.length !== sorted.length) {
      throw new Error('服务端上传清单无效')
    }
    let confirmedBytes = session.receivedBytes
    let transferredBytes = 0
    const inFlight = new Map<number, { name: string; loaded: number }>()
    function report() {
      options.onProgress({
        confirmedBytes, totalBytes: session.totalBytes, transferredBytes,
        inFlightBytes: Array.from(inFlight.values()).reduce((sum, item) => sum + item.loaded, 0),
        completedFiles: session.files.filter(file => file.chunks?.every(Boolean)).length,
        currentFiles: [...new Set(Array.from(inFlight.values(), item => item.name))],
      })
    }
    report()
    if (session.status === 'uploading') {
      type Chunk = { file: number; chunk: number; offset: number; length: number; digest?: string }
      const existing: Chunk[] = [], missing: Chunk[] = []
      session.files.forEach((file, fileIndex) => {
        const local = files[fileIndex]!
        if (file.path !== local.path || file.size !== local.size || file.chunks?.length !== Math.ceil(file.size / session.chunkSize)) {
          throw new Error('本地文件与服务端上传清单不一致')
        }
        file.chunks.forEach((digest, chunk) => {
          const item = { file: fileIndex, chunk, offset: chunk * session.chunkSize, length: Math.min(session.chunkSize, file.size - chunk * session.chunkSize), digest }
          ;(digest ? existing : missing).push(item)
        })
      })
      const concurrency = Math.max(1, Math.min(options.concurrency || 3, session.maxConcurrency || 3, 6))
      async function runChunks(jobs: Chunk[], verifyOnly: boolean) {
        let cursor = 0
        let verified = 0
        const workers = Array.from({ length: Math.min(concurrency, jobs.length) }, async () => {
          const hasher = (options.createHasher || createUploadChunkHasher)()
          hashers.push(hasher)
          const abortHash = () => hasher.dispose()
          signal.addEventListener('abort', abortHash, { once: true })
          try {
            while (cursor < jobs.length) {
              throwIfAborted(signal)
              const jobIndex = cursor++, job = jobs[jobIndex]!
              const entry = sorted[job.file]!
              const blob = entry.file.slice(job.offset, job.offset + job.length)
              const digest = await hasher.hash(blob)
              throwIfAborted(signal)
              if (verifyOnly) {
                if (digest !== job.digest) throw new Error(`本地文件内容已变化：${entry.path}。请点击“重新上传”创建新记录`)
                options.onPhase('preparing', `已核对 ${++verified} / ${jobs.length} 个已接收分片，无需重传`)
                continue
              }
              await retry(async () => {
                const requestController = new AbortController()
                const abort = () => requestController.abort()
                signal.addEventListener('abort', abort, { once: true })
                let timedOut = false
                let timer: ReturnType<typeof setTimeout>
                const resetTimer = () => {
                  clearTimeout(timer)
                  timer = setTimeout(() => { timedOut = true; requestController.abort() }, 90_000)
                }
                let sent = 0
                let active = true
                inFlight.set(jobIndex, { name: entry.path, loaded: 0 })
                resetTimer()
                try {
                  await api.putArtifactUploadChunk(id!, job.file, job.chunk, blob, digest, requestController.signal, event => {
                    if (!active || signal.aborted) return
                    const loaded = Math.min(job.length, Math.max(sent, event.loaded))
                    if (loaded > sent) resetTimer()
                    transferredBytes += loaded - sent
                    sent = loaded
                    inFlight.set(jobIndex, { name: entry.path, loaded })
                    report()
                  })
                  transferredBytes += job.length - sent
                } catch (error) {
                  if (timedOut) throw new Error('当前分片 90 秒无新增传输或服务端响应')
                  throw error
                } finally {
                  active = false
                  clearTimeout(timer!)
                  signal.removeEventListener('abort', abort)
                  inFlight.delete(jobIndex)
                  report()
                }
              }, `${entry.path} 分片 ${job.chunk + 1} 传输失败`)
              session.files[job.file]!.chunks![job.chunk] = digest
              confirmedBytes += job.length
              report()
            }
          } catch (error) {
            failure ??= error
            controller.abort()
          } finally {
            signal.removeEventListener('abort', abortHash)
            hasher.dispose()
          }
        })
        await Promise.allSettled(workers)
        if (failure) throw failure
        throwIfAborted(signal)
      }
      if (existing.length) {
        options.onPhase('preparing', `核对本地文件，复用 ${existing.length} 个已接收分片…`)
        await runChunks(existing, true)
      }
      options.onPhase('uploading', `${concurrency} 路并发上传，失败分片自动重试`)
      await runChunks(missing, false)
    }
    throwIfAborted(signal)
    options.onPhase('processing', '文件已接收，正在校验并生成更新记录…')
    if (session.status !== 'completed' && session.status !== 'processing') {
      session = await retry(() => api.completeArtifactUpload(id!, signal), '提交文件校验失败')
      options.onSnapshot(session)
    }
    while (session.status === 'processing') {
      await delay(1000, signal)
      session = await retry(() => api.getArtifactUpload(id!, signal), '读取校验进度失败')
      options.onSnapshot(session)
    }
    throwIfAborted(signal)
    if (session.status !== 'completed' || !session.result) throw new Error(session.error || '生成更新记录失败，可继续重试，无需重传文件')
    setStored(storage, key)
    return session.result
  } finally {
    controller.abort()
    options.signal.removeEventListener('abort', externalAbort)
    hashers.forEach(hasher => hasher.dispose())
  }
}
