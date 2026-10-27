import { uploadSHA256 } from './upload-sha256'

export function createUploadChunkHasher() {
  const worker = typeof Worker === 'undefined' ? undefined : new Worker(new URL('./upload-hash.worker.ts', import.meta.url), { type: 'module' })
  let rejectPending: ((error: Error) => void) | undefined
  return {
    async hash(blob: Blob): Promise<string> {
      if (!worker) return uploadSHA256(new Uint8Array(await blob.arrayBuffer()))
      return new Promise((resolve, reject) => {
        rejectPending = reject
        worker.onmessage = (event: MessageEvent<{ digest: string; error?: string }>) => {
          rejectPending = undefined
          if (event.data.error) reject(new Error(event.data.error))
          else resolve(event.data.digest)
        }
        worker.onerror = () => { rejectPending = undefined; reject(new Error('文件校验线程失败，请重试')) }
        worker.postMessage(blob)
      })
    },
    dispose() {
      worker?.terminate()
      rejectPending?.(new Error('上传已暂停'))
      rejectPending = undefined
    },
  }
}
