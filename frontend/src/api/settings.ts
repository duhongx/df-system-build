import request from './request'

// ---------- Template ----------
export interface TemplateDefault {
  id?: number
  key: string
  value: string
}

export interface Template {
  id: number
  name: string
  code: string
  category: string
  description: string
  defaults?: TemplateDefault[]
}

export const listTemplates = () => request.get<any, Template[]>('/templates')
export const createTemplate = (data: Partial<Template>) => request.post<any, Template>('/templates', data)
export const updateTemplate = (id: number, data: Partial<Template>) =>
  request.put<any, Template>(`/templates/${id}`, data)
export const deleteTemplate = (id: number) => request.delete<any, null>(`/templates/${id}`)

// ---------- Executor ----------
export interface Executor {
  id: number
  name: string
  dockerImage: string
  type: string
  cpuLimit: string
  memoryLimit: string
  cacheMounts?: string  // JSON string: [{"hostPath":"...","containerPath":"..."}]
  status?: string
}

export const listExecutors = () => request.get<any, Executor[]>('/executors')
export const createExecutor = (data: Partial<Executor>) => request.post<any, Executor>('/executors', data)
export const updateExecutor = (id: number, data: Partial<Executor>) =>
  request.put<any, Executor>(`/executors/${id}`, data)
export const deleteExecutor = (id: number) => request.delete<any, null>(`/executors/${id}`)

// ---------- Remote Server ----------
export interface RemoteServer {
  id: number
  name: string
  host: string
  port: number
  username: string
  authType: string
  status?: string
}

export interface ServerCreateRequest extends Partial<RemoteServer> {
  credential?: string
}

export const listServers = () => request.get<any, RemoteServer[]>('/servers')
export const createServer = (data: ServerCreateRequest) => request.post<any, RemoteServer>('/servers', data)
export const updateServer = (id: number, data: ServerCreateRequest) =>
  request.put<any, RemoteServer>(`/servers/${id}`, data)
export const deleteServer = (id: number) => request.delete<any, null>(`/servers/${id}`)
export const testServer = (id: number) => request.post<any, null>(`/servers/${id}/test`)
export const readServerKey = () => request.get<any, { path: string; content: string }>('/servers/read-key')
export interface NotificationWebhook {
  id: number
  name: string
  type: 'dingtalk' | 'wecom'
  webhookUrl: string
  secret?: string
  notifyOnSuccess: boolean
  notifyOnFailure: boolean
  enabled: boolean
}

export const listNotifications = () => request.get<any, NotificationWebhook[]>('/notifications')
export const createNotification = (data: Partial<NotificationWebhook>) =>
  request.post<any, NotificationWebhook>('/notifications', data)
export const updateNotification = (id: number, data: Partial<NotificationWebhook>) =>
  request.put<any, NotificationWebhook>(`/notifications/${id}`, data)
export const deleteNotification = (id: number) => request.delete<any, null>(`/notifications/${id}`)
export const testNotification = (id: number) => request.post<any, null>(`/notifications/${id}/test`)

// ---------- Global Settings ----------
export const getSettings = () => request.get<any, Record<string, string>>('/settings')
export const updateSettings = (data: Record<string, string>) => request.put<any, null>('/settings', data)

// ---------- Registry Test ----------
export const testRegistry = () => request.post<any, null>('/settings/test-registry')

// ---------- K8s Namespaces ----------
export const getK8sNamespaces = () => request.get<any, string[]>('/settings/k8s-namespaces')
