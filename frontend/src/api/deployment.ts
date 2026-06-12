import request, { getToken, type PageResult } from './request'

export interface DeploymentComponent {
  name: string
  displayName: string
  description: string
  category: string // k8s | business | other
  order: number
  enabled: boolean
  requireHostSelection: boolean
  autoBindNote: string
  hostIds: number[]
  pipelineComponents: string[]
  deployState: string // not_deployed | deployed | failed
  minUserHosts: number
  maxUserHosts: number
}

export interface DeploymentSettings {
  ssh_user: string
  ssh_private_key_path: string
  ssh_port: number
  remote_root: string
  retain_deployments: number
  default_timeout_seconds: number
}

export interface NetworkSettings {
  vip: string
  service_cidr: string
  cluster_cidr: string
  node_cidr_mask_size: number
}

export interface EnvEntry {
  key: string
  value: string
}

export interface GlobalConfig {
  deployment: DeploymentSettings
  network: NetworkSettings
  env: EnvEntry[]
}

export interface ComponentTargets {
  component_name: string
  host_ids: number[]
}

export interface ComponentOverride {
  component_name: string
  params: Record<string, any>
}

export interface ConflictReport {
  serverId: number
  components: string[]
  reason: string
}

export interface DeploymentRun {
  id: number
  task_type: string
  target_component: string
  target_host: string
  dry_run: boolean
  status: string
  started_at?: string
  ended_at?: string
  error_summary: string
  scope_kind: string
  phase: string
  duration_ms: number
  created_at: string
}

export interface DeploymentLog {
  sequence: number
  timestamp: string
  component: string
  host: string
  phase: string
  action_name: string
  action_type: string
  status: string
  detail: string
  is_error: boolean
}

// ---- components ----
export const listComponents = () =>
  request.get<any, DeploymentComponent[]>('/deployment/components')
export const getEnabled = () =>
  request.get<any, string[]>('/deployment/components/enabled')
export const putEnabled = (components: string[]) =>
  request.put<any, any>('/deployment/components/enabled', { components })
export const getComponentDefaults = () =>
  request.get<any, Record<string, Record<string, any>>>('/deployment/components/defaults')
export interface ComponentTask {
  component: string
  phase: string
  id: string
  name: string
  actions: { type: string; name: string; summary: string }[]
}
export const getComponentTasks = (name: string) =>
  request.get<any, { name: string; pipelineComponents: string[]; items: ComponentTask[] }>(`/deployment/components/${name}/tasks`)

// ---- global config ----
export const getGlobalConfig = () =>
  request.get<any, GlobalConfig>('/deployment/global-config')
export const putGlobalConfig = (body: Partial<GlobalConfig> & { envReplace?: boolean }) =>
  request.put<any, any>('/deployment/global-config', body)

// ---- overrides ----
export const listOverrides = () =>
  request.get<any, ComponentOverride[]>('/deployment/overrides')
export const getOverride = (component: string) =>
  request.get<any, ComponentOverride>(`/deployment/overrides/${component}`)
export const putOverride = (component: string, params: Record<string, any>) =>
  request.put<any, any>(`/deployment/overrides/${component}`, { params })
export const deleteOverride = (component: string) =>
  request.delete<any, any>(`/deployment/overrides/${component}`)

// ---- targets ----
export const listTargets = () =>
  request.get<any, ComponentTargets[]>('/deployment/targets')
export const getTargets = (component: string) =>
  request.get<any, ComponentTargets>(`/deployment/targets/${component}`)
export const putTargets = (component: string, serverIds: number[]) =>
  request.put<any, any>(`/deployment/targets/${component}`, { serverIds })
export const runHostChecks = () =>
  request.post<any, { conflicts: ConflictReport[] }>('/deployment/host-checks')

// ---- runs ----
export const listRuns = (page = 1, pageSize = 20) =>
  request.get<any, PageResult<DeploymentRun>>('/deployment/runs', { params: { page, pageSize } })
export const runningRuns = () =>
  request.get<any, any[]>('/deployment/runs/running')
export const getRun = (id: number) =>
  request.get<any, DeploymentRun>(`/deployment/runs/${id}`)
export const createRun = (body: { mode?: string; component?: string; phase?: string; host_ids?: number[]; dry_run?: boolean }) =>
  request.post<any, { id: number }>('/deployment/runs', body)
export const previewRun = (body: { mode?: string; component?: string; phase?: string; host_ids?: number[]; dry_run?: boolean }) =>
  request.post<any, { task_count: number; action_count: number; plan: any[] }>('/deployment/runs/preview', body)
export const cancelRun = (id: number) =>
  request.post<any, { canceled: boolean }>(`/deployment/runs/${id}/cancel`)
export const getRunLogs = (id: number, afterSeq = 0, limit = 500) =>
  request.get<any, DeploymentLog[]>(`/deployment/runs/${id}/logs`, { params: { afterSeq, limit } })

// SSE: EventSource cannot set headers, so the JWT goes in the query string.
export const runEventSource = (id: number) =>
  new EventSource(`/api/deployment/runs/${id}/events?token=${encodeURIComponent(getToken())}`)

// ---- offline bundle ----
export interface OfflineStatus {
  current: { bundleVersion: string; fileCount: number; installedAt: string; installedBy: string } | null
  resourceDir: string
  scan: Record<string, number>
}
export const getOfflineStatus = () =>
  request.get<any, OfflineStatus>('/deployment/offline/status')
export const installOffline = (body: { path: string; bundleVersion?: string; clean?: boolean }) =>
  request.post<any, { bundleVersion: string; fileCount: number }>('/deployment/offline/install', body)
export const verifyOffline = () =>
  request.post<any, { ok: boolean; missing: string[]; manifest: string }>('/deployment/offline/verify')
export const offlineUploadUrl = '/api/deployment/offline/upload'
