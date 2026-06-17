import request from './request'
import type { FileItem } from './server-mgmt'

export interface MatchResult {
  fileName: string
  appName: string
  appType: string
  appId: number
  matched: boolean
  valid: boolean
  skipped: boolean
  matchReason: string
}

export const matchArtifacts = (files: string[], sourceDir?: string, batchId?: string) =>
  request.post<any, { results: MatchResult[]; total: number; matched: number; invalid: number; skipped: number; sourceDir: string; batchId: string }>('/batch-deploy/match', { files, sourceDir, batchId })

export const executeBatchDeploy = (
  sourceDir: string,
  items: { fileName: string; appId: number }[],
  namespace?: string,
  batchId?: string,
  deployMode?: 'immediate' | 'cutover',
) =>
  request.post<any, { pipelines: any[]; errors: string[] }>('/batch-deploy/execute', { sourceDir, items, namespace, batchId, deployMode })

export const listLocalDir = (path: string, batchId?: string) =>
  request.get<any, { path: string; files: string[]; count: number }>('/batch-deploy/local-dir', { params: { path, batchId } })

export const importRemoteDir = (serverId: number, path: string) =>
  request.post<any, { batchId: string; uploadDir: string; files: string[]; success: string[]; count: number }>('/batch-deploy/remote-dir', { serverId, path })

export const listLocalBrowser = (path?: string) =>
  request.get<any, { root: string; path: string; files: FileItem[] }>('/batch-deploy/local-browser', { params: path ? { path } : {} })

export interface DownloadJob {
  id: string
  status: 'pending' | 'running' | 'success' | 'failed'
  remotePath: string
  localDir: string
  targetPath: string
  batchId: string
  files: string[]
  count: number
  totalFiles: number
  completedFiles: number
  currentPath: string
  error: string
  hasPartial: boolean
}

export interface ArtifactSourceBatch {
  batchId: string
  sourceType: 'upload' | 'download' | 'artifact'
  sourceLabel: string
  status: string
  localDir: string
  targetPath: string
  files: string[]
  count: number
  error: string
  hasPartial: boolean
  updatedAt: string
}

export interface ArtifactVersion {
  id: number
  versionNo: string
  sourceType: 'upload' | 'download' | 'artifact' | string
  sourceLabel: string
  status: string
  localDir: string
  targetPath: string
  remotePath: string
  count: number
  deployableCount: number
  matchedCount: number
  validCount: number
  invalidCount: number
  skippedCount: number
  unmatchedCount: number
  error: string
  createdAt: string
  updatedAt: string
}

export interface ArtifactVersionItem {
  id: number
  versionNo: string
  fileName: string
  fileType: string
  fileSizeBytes: number
  sha256: string
  appId: number
  appName: string
  appType: string
  matchStatus: 'matched' | 'unmatched' | 'skipped' | string
  validateStatus: 'valid' | 'invalid' | string
  deployable: boolean
  packageVersionJson: string
  statusReason: string
  createdAt: string
  updatedAt: string
}

export interface ArtifactDeployBatch {
  id: number
  deployBatchNo: string
  versionNo: string
  namespace: string
  deployMode: 'immediate' | 'cutover' | string
  status: string
  triggerUser: string
  totalCount: number
  successCount: number
  failedCount: number
  rolledBackCount: number
  errorMessage: string
  startedAt?: string
  finishedAt?: string
  createdAt: string
  updatedAt: string
}

export interface ArtifactDeployRecord {
  id: number
  deployBatchNo: string
  versionNo: string
  artifactVersionItemId: number
  pipelineId: number
  appId: number
  appName: string
  appCode: string
  appType: string
  vueRole: string
  fileName: string
  namespace: string
  deploymentName: string
  runtimeVersionPath: string
  beforeImage: string
  afterImage: string
  packageVersionJson: string
  beforeBusinessVersionJson: string
  afterBusinessVersionJson: string
  restoredBusinessVersionJson: string
  buildStatus: string
  deployStatus: string
  verifyStatus: string
  rollbackStatus: string
  status: string
  errorMessage: string
  deployedAt?: string
  rolledBackAt?: string
  createdAt: string
  updatedAt: string
}

export const startDownloadRemoteDir = (remotePath: string, localPath?: string) =>
  request.post<any, DownloadJob>('/batch-deploy/download-remote-dir/start', { remotePath, localPath })

export const getDownloadProgress = (jobId: string) =>
  request.get<any, DownloadJob>(`/batch-deploy/download-remote-dir/progress/${jobId}`)

export const getActiveDownloadJob = () =>
  request.get<any, DownloadJob | null>('/batch-deploy/download-remote-dir/active')

export const getLatestDownloadJob = () =>
  request.get<any, DownloadJob | null>('/batch-deploy/download-remote-dir/latest')

export const listDownloadBatches = () =>
  request.get<any, { batches: DownloadJob[] }>('/batch-deploy/download-remote-dir/batches')

export const listArtifactSourceBatches = () =>
  request.get<any, { batches: ArtifactSourceBatch[] }>('/batch-deploy/source-batches')

export const listArtifactVersions = (deployable = false) =>
  request.get<any, { versions: ArtifactVersion[] }>('/batch-deploy/artifact-versions', { params: deployable ? { deployable: 'true' } : {} })

export const getArtifactVersion = (versionNo: string) =>
  request.get<any, { version: ArtifactVersion; items: ArtifactVersionItem[] }>(`/batch-deploy/artifact-versions/${encodeURIComponent(versionNo)}`)

export const deleteArtifactVersionItem = (versionNo: string, itemId: number) =>
  request.delete<any, { version: ArtifactVersion; items: ArtifactVersionItem[] }>(`/batch-deploy/artifact-versions/${encodeURIComponent(versionNo)}/items/${itemId}`)

export const replaceArtifactVersionItem = (versionNo: string, itemId: number, file: File) => {
  const formData = new FormData()
  formData.append('file', file)
  return request.post<any, { version: ArtifactVersion; items: ArtifactVersionItem[] }>(
    `/batch-deploy/artifact-versions/${encodeURIComponent(versionNo)}/items/${itemId}/replace`,
    formData,
    { headers: { 'Content-Type': 'multipart/form-data' } },
  )
}

export const redownloadArtifactVersionItem = (versionNo: string, itemId: number) =>
  request.post<any, { version: ArtifactVersion; items: ArtifactVersionItem[] }>(`/batch-deploy/artifact-versions/${encodeURIComponent(versionNo)}/items/${itemId}/redownload`)

export const listArtifactDeployBatches = (status?: string) =>
  request.get<any, { batches: ArtifactDeployBatch[] }>('/batch-deploy/deploy-batches', { params: status ? { status } : {} })

export const getArtifactDeployBatch = (batchNo: string) =>
  request.get<any, { batch: ArtifactDeployBatch; records: ArtifactDeployRecord[] }>(`/batch-deploy/deploy-batches/${encodeURIComponent(batchNo)}`)

export const rollbackArtifactDeployBatch = (batchNo: string) =>
  request.post<any, { batch: ArtifactDeployBatch; records: ArtifactDeployRecord[] }>(`/batch-deploy/deploy-batches/${encodeURIComponent(batchNo)}/rollback`)

export const retryDownloadBatch = (batchId: string) =>
  request.post<any, DownloadJob>(`/batch-deploy/download-remote-dir/batches/${batchId}/retry`)

export const clearDownloadBatch = (batchId: string) =>
  request.delete<any, { batchId: string }>(`/batch-deploy/download-remote-dir/batches/${batchId}`)

export const downloadRemoteDir = (remotePath: string, localPath: string) =>
  request.post<any, { targetPath: string; batchId: string; files: string[]; count: number }>('/batch-deploy/download-remote-dir', { remotePath, localPath })

export const listPackageServerDir = (serverId: number, path: string) =>
  request.get<any, { path: string; files: FileItem[] }>(`/batch-deploy/package-server/${serverId}/list`, { params: { path } })

export const listPackageDownloadDir = (path?: string) =>
  request.get<any, { path: string; basePath: string; files: FileItem[] }>('/batch-deploy/package-download/list', { params: path ? { path } : {} })
