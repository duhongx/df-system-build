import request, { getToken, type PageResult } from './request'
import { ElMessage } from 'element-plus'

export interface LogSource {
  id: number; name: string; category: string; serverId: number
  directory: string; pattern: string; recursive: boolean
}
export interface LogFile { path: string; size: number; modifiedAt: string }
export interface CollectionItem {
  source: LogSource; host: string; path: string; archivePath: string
  status: string; size: number; bytes: number; modifiedAt: string; error: string
}
export interface CollectionJob {
  id: number; status: string; operator: string; message: string; items: CollectionItem[]
  totalFiles: number; successFiles: number; failedFiles: number; archiveSize: number
  createdAt: string; finishedAt: string | null
}
export const listLogSources = () => request.get<any, LogSource[]>('/log-collection/sources')
export const saveLogSource = (source: LogSource) => source.id
  ? request.put<any, LogSource>(`/log-collection/sources/${source.id}`, source)
  : request.post<any, LogSource>('/log-collection/sources', source)
export const deleteLogSource = (id: number) => request.delete(`/log-collection/sources/${id}`)
export const listLogFiles = (id: number, signal?: AbortSignal) => request.get<any, LogFile[]>(`/log-collection/sources/${id}/files`, { timeout: 120000, signal })
export const startLogCollection = (selections: { sourceId: number; files: string[] }[]) => request.post<any, CollectionJob>('/log-collection/jobs', { selections })
export const listLogCollections = (page: number) => request.get<any, PageResult<CollectionJob>>('/log-collection/jobs', { params: { page } })
export const getLogCollection = (id: number) => request.get<any, CollectionJob>(`/log-collection/jobs/${id}`)
export const cancelLogCollection = (id: number) => request.post(`/log-collection/jobs/${id}/cancel`)
export const deleteLogCollection = (id: number) => request.delete(`/log-collection/jobs/${id}`)

// Native download: no entire-ZIP Blob and no login token in a URL.
export function downloadLogCollection(id: number) {
  let frame = document.getElementById('log-collection-download') as HTMLIFrameElement | null
  if (!frame) {
    frame = document.createElement('iframe')
    frame.name = frame.id = 'log-collection-download'
    frame.hidden = true
    frame.onload = () => {
      try {
        const text = frame?.contentDocument?.body?.textContent
        if (text) ElMessage.error(JSON.parse(text).message || '下载失败')
      } catch { /* A ZIP response is handled by the browser download manager. */ }
    }
    document.body.appendChild(frame)
  }
  const form = document.createElement('form')
  form.method = 'POST'; form.action = `/api/log-collection/jobs/${id}/download`; form.target = frame.name
  const token = document.createElement('input')
  token.type = 'hidden'; token.name = 'token'; token.value = getToken()
  form.appendChild(token); document.body.appendChild(form)
  form.submit(); form.remove()
}
