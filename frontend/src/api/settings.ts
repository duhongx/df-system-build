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

export interface PostgreSQLBusinessAccount {
  username: string
  hasOverride: boolean
  discovered: boolean
}
export interface PostgreSQLAccountTestResult {
  username: string
  passwordSource: 'admin' | 'common' | 'override' | ''
  success: boolean
  message: string
}
export interface PostgreSQLConnectionTestReport {
  success: boolean
  message: string
  results: PostgreSQLAccountTestResult[]
  accounts: PostgreSQLBusinessAccount[]
}
export const listPostgreSQLPasswordOverrides = () => request.get<any, { username: string; updatedAt: string }[]>('/settings/postgresql-accounts')
export const discoverPostgreSQLAccounts = (config: Record<string, string>) => request.post<any, PostgreSQLBusinessAccount[]>('/settings/postgresql-accounts/discover', { config })
export const setPostgreSQLAccountPassword = (username: string, password: string) => request.put<any, null>(`/settings/postgresql-accounts/${encodeURIComponent(username)}/password`, { password })
export const savePostgreSQLAccountPasswords = (items: { username: string; password: string }[], config?: Record<string, string>) => request.post<any, { updatedCount: number }>('/settings/postgresql-accounts/passwords/batch', { items, config })
export const clearPostgreSQLAccountPassword = (username: string) => request.delete<any, null>(`/settings/postgresql-accounts/${encodeURIComponent(username)}/password`)
export const testPostgreSQLAccount = (username: string, config: Record<string, string>) => request.post<any, PostgreSQLAccountTestResult>(`/settings/postgresql-accounts/${encodeURIComponent(username)}/test`, { config })
export const testPostgreSQLConnections = (config: Record<string, string>) => request.post<any, PostgreSQLConnectionTestReport>('/settings/test-connection', { type: 'postgresql', config }, { timeout: 90000 })

// ---------- Registry Test ----------
export const testRegistry = () => request.post<any, null>('/settings/test-registry')

// ---------- K8s Namespaces ----------
export const getK8sNamespaces = () => request.get<any, string[]>('/settings/k8s-namespaces')
