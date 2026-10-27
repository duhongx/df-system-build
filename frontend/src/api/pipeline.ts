import request, { type PageResult } from './request'

export interface PipelineStage {
  id: number
  pipelineId: number
  stageCode: string
  stageName: string
  stageOrder: number
  status: string
  command?: string
  startTime?: string | null
  endTime?: string | null
  durationSeconds?: number | null
  exitCode?: number | null
  errorMessage?: string
}

export interface Pipeline {
  id: number
  pipelineNo: string
  taskId: number
  applicationId: number
  appName: string
  appType: string
  gitRepo: string
  gitBranch: string
  gitCommitHash?: string
  gitCommitAuthor?: string
  gitCommitMessage?: string
  status: string
  triggerUser: string
  artifactName?: string
  uploadPath?: string
  uploadTargets?: string
  batchId?: string
  imageName?: string
  deploymentProgress?: string
  frontendRelease?: {
    deploymentName: string
    namespace: string
    preparationImage?: string
    newImage: string
    assetsVerified?: boolean
    regions: { appName: string; role: string; appCode?: string; path: string; current: Record<string, unknown>; retained?: Record<string, unknown> }[]
  }
  deployMode?: string
  startTime?: string | null
  endTime?: string | null
  durationSeconds?: number | null
  errorStage?: string
  errorMessage?: string
  stages?: PipelineStage[]
  createdAt?: string
}

export interface StageLog {
  id: number
  pipelineId: number
  stageId: number
  lineNumber: number
  content: string
  stream: string
  timestamp: string
}

export function listPipelines(params?: { page?: number; pageSize?: number; app?: string; status?: string; search?: string; appType?: string }) {
  return request.get<any, PageResult<Pipeline>>('/pipelines', { params })
}

export function getPipeline(id: number) {
  return request.get<any, Pipeline>(`/pipelines/${id}`)
}

export function cancelPipeline(id: number) {
  return request.post<any, null>(`/pipelines/${id}/cancel`)
}

export function getStageLogs(pipelineId: number, stageId: number) {
  return request.get<any, StageLog[]>(`/pipelines/${pipelineId}/stages/${stageId}/logs`)
}

export function getBuildQueue() {
  return request.get<any, { running: Pipeline[]; pending: Pipeline[] }>('/build-queue', { silentError: true })
}

export function cancelQueued(id: number) {
  return request.delete<any, null>(`/build-queue/${id}`)
}

export function rolloutPipeline(id: number) {
  return request.post<any, null>(`/pipelines/${id}/rollout`)
}

export function streamLogsUrl(pipelineId: number): string {
  // SSE EventSource doesn't send Authorization header by default
  // Backend needs to accept token via query param for SSE
  return `/api/pipelines/${pipelineId}/logs/stream`
}

// ---------- Dashboard ----------
export interface DashboardStats {
  totalApps: number
  todayBuilds: number
  successCount: number
  failedCount: number
}

export const getDashboardStats = () => request.get<any, DashboardStats>('/dashboard/stats')
export const getRecentBuilds = () => request.get<any, Pipeline[]>('/dashboard/recent-builds')

// ---------- Remote Tools ----------
export function remoteSyncUrl() {
  return '/api/remote/sync'
}

export function remotePackageUrl() {
  return '/api/remote/package'
}
