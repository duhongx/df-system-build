import request from './request'

export interface MatchResult {
  fileName: string
  appName: string
  appType: string
  appId: number
  matched: boolean
  valid: boolean
  matchReason: string
}

export const matchArtifacts = (files: string[], sourceDir?: string) =>
  request.post<any, { results: MatchResult[]; total: number; matched: number; invalid: number }>('/batch-deploy/match', { files, sourceDir })

export const executeBatchDeploy = (sourceDir: string, items: { fileName: string; appId: number }[]) =>
  request.post<any, { pipelines: any[]; errors: string[] }>('/batch-deploy/execute', { sourceDir, items })

export const listLocalDir = (path: string) =>
  request.get<any, { path: string; files: string[]; count: number }>('/batch-deploy/local-dir', { params: { path } })
