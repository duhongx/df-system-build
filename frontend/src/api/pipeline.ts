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
  builderImage?: string
  artifactName?: string
  uploadPath?: string
  uploadTargets?: string
  batchId?: string
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

export function listPipelines(params?: { page?: number; pageSize?: number; app?: string; status?: string }) {
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
  return request.get<any, { running: Pipeline[]; pending: Pipeline[] }>('/build-queue')
}

export function cancelQueued(id: number) {
  return request.delete<any, null>(`/build-queue/${id}`)
}

export function deployPipeline(id: number) {
  return request.post<any, null>(`/pipelines/${id}/deploy`)
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

// ---------- Artifact ----------
export interface Artifact {
  id: number
  pipelineId: number
  pipelineNo: string
  appName: string
  artifactName: string
  gitBranch: string
  gitCommitHash?: string
  uploadPath: string
  uploadTargets: string
  sourceType?: string
  sourcePath?: string
  storagePath?: string
  sha256?: string
  batchId?: string
  isLatest?: boolean
  fileSizeBytes?: number
  durationSeconds: number
  createdAt: string
}

export function listArtifacts(params?: { page?: number; pageSize?: number; search?: string }) {
  return request.get<any, PageResult<Artifact>>('/artifacts', { params })
}

// ---------- Remote Tools ----------
export function remoteSyncUrl() {
  return '/api/remote/sync'
}

export function remotePackageUrl() {
  return '/api/remote/package'
}
