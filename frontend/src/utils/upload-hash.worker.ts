import { uploadSHA256 } from './upload-sha256'

self.onmessage = async (event: MessageEvent<Blob>) => {
  try {
    const bytes = await event.data.arrayBuffer()
    const digest = self.crypto?.subtle
      ? Array.from(new Uint8Array(await self.crypto.subtle.digest('SHA-256', bytes)), value => value.toString(16).padStart(2, '0')).join('')
      : uploadSHA256(new Uint8Array(bytes))
    self.postMessage({ digest })
  } catch (error) {
    self.postMessage({ error: error instanceof Error ? error.message : '文件校验失败' })
  }
}
