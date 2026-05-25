import request, { type PageResult } from './request'

export interface ManagedServer {
  id: number
  host: string
  remark: string
  port: number
  username: string
  authType: string // password / certificate
  connTimeout: number
  forbiddenCommands: string
  sortOrder: number
  status: string // online / offline / unknown
  lastConnTime: string | null
  createdBy: string
  createdAt: string
}

export interface ServerLog {
  id: number
  serverId: number
  type: string // ssh / sftp
  operator: string
  content: string
  clientIp: string
  createdAt: string
}

export interface FileItem {
  name: string
  path: string
  size: number
  isDir: boolean
  mode: string
  modTime: string
}

// Server CRUD
export const listManagedServers = (search?: string) =>
  request.get<any, ManagedServer[]>('/server-mgmt', { params: search ? { search } : {} })

export const getManagedServer = (id: number) =>
  request.get<any, ManagedServer>(`/server-mgmt/${id}`)

export const createManagedServer = (data: any) =>
  request.post<any, ManagedServer>('/server-mgmt', data)

export const updateManagedServer = (id: number, data: any) =>
  request.put<any, ManagedServer>(`/server-mgmt/${id}`, data)

export const deleteManagedServer = (id: number) =>
  request.delete<any, null>(`/server-mgmt/${id}`)

export const testManagedServer = (id: number) =>
  request.post<any, null>(`/server-mgmt/${id}/test`)

// Server Logs
export const getServerLogs = (id: number, params?: { type?: string; page?: number; pageSize?: number }) =>
  request.get<any, PageResult<ServerLog>>(`/server-mgmt/${id}/logs`, { params })

// SFTP
export const listSftpFiles = (id: number, path: string) =>
  request.get<any, { path: string; files: FileItem[] }>(`/server-mgmt/${id}/sftp/list`, { params: { path } })

export const mkdirSftp = (id: number, path: string) =>
  request.post<any, null>(`/server-mgmt/${id}/sftp/mkdir`, { path })

export const deleteSftp = (id: number, path: string) =>
  request.post<any, null>(`/server-mgmt/${id}/sftp/delete`, { path })

export const renameSftp = (id: number, oldPath: string, newPath: string) =>
  request.post<any, null>(`/server-mgmt/${id}/sftp/rename`, { oldPath, newPath })

// Upload uses FormData (handled separately in component)
// Download URL: /api/server-mgmt/:id/sftp/download?path=xxx

// Server Monitor
export const getServerMetrics = (id: number) =>
  request.get<any, any>(`/server-mgmt/${id}/metrics`)
export const getTerminalWsUrl = (id: number) => {
  const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
  const token = localStorage.getItem('df-token') || ''
  return `${protocol}//${window.location.host}/api/server-mgmt/${id}/terminal?token=${encodeURIComponent(token)}`
}
