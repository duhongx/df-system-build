import request, { getToken, type PageResult } from './request'

export interface PostgreSQLInstanceInfo {
  config: {
    host: string
    port: string
    database: string
    user: string
    password: string
  }
  status: string
  message: string
  version: string
  currentDb: string
  currentUser: string
  role: string
  serverAddr: string
  serverPort: number
  settings: Record<string, string>
  replications: {
    clientAddr: string
    state: string
    syncState: string
    writeLag: string
    flushLag: string
    replayLag: string
  }[]
  checkedAt: string
}

export interface SQLChangeFile {
  id: number
  batchId: number
  batchSortNo: number
  systemCode: string
  environment: string
  schemaName: string
  version: string
  groupSortNo: number
  fileName: string
  fileContent?: string
  executeStatus: string
  executeMessage: string
  executeUser: string
  executeTime?: string
  createdAt: string
}

export interface SQLChangeBatch {
  id: number
  batchName: string
  executeStatus: string
  executeMessage: string
  executeUser: string
  executeTime?: string
  totalFiles: number
  successFiles: number
  failedFiles: number
  skippedFiles: number
  createdAt: string
}

export interface SQLChangeStatement {
  id: number
  fileId: number
  lineNumber: number
  sqlContent: string
  sqlType: string
  riskLevel: string
  riskReason: string
  canRunInTransaction: boolean
  executionStrategy: string
  executeStatus: string
  executeMessage: string
  sqlState: string
  estimatedRows: number
  affectedRows: number
  durationMs: number
  executeTime?: string
}

export interface ParseSQLRequest {
  systemCode?: string
  environment?: string
  schemaName?: string
  version?: string
  groupSortNo?: number
  fileName?: string
  content: string
  overwrite?: boolean
}

export interface SQLExecuteOptions {
  skipExistsColumn: boolean
  skipExistsTable: boolean
  skipUniqueConstraint: boolean
  requireRiskConfirmation: boolean
  confirmWarnRisk: boolean
  forceBlockedSql: boolean
}

export interface SQLForceWhitelistOption {
  sqlType: string
  label: string
}

export interface SQLForceWhitelistConfig {
  available: SQLForceWhitelistOption[]
  enabled: string[]
}

export interface ParseSQLBatchFile {
  fileName: string
  content: string
}

export interface SQLViewDependencyTask {
  id: number
  schemaName: string
  tableName: string
  columnName: string
  alterSql: string
  status: string
  riskLevel: string
  riskReason: string
  lockTimeout: string
  statementTimeout: string
  operator: string
  executeMessage: string
  analyzedAt?: string
  executedAt?: string
  createdAt: string
}

export interface SQLViewDependencyItem {
  id: number
  taskId: number
  objectSchema: string
  objectName: string
  objectKind: string
  depth: number
  dropOrder: number
  restoreOrder: number
  definition: string
  ownerName: string
  grantsJson: string
  commentsJson: string
  indexesJson: string
  rulesJson: string
  triggersJson: string
  optionsJson: string
  backupHash: string
  dropSql: string
  createSql: string
  restoreRefreshSql: string
  restoreOwnerSql: string
  restoreGrantsSql: string
  restoreCommentsSql: string
  restoreIndexesSql: string
  restoreRulesSql: string
  restoreTriggersSql: string
  verifySql: string
  status: string
  errorMessage: string
}

export interface SQLViewDependencyTaskRequest {
  schemaName: string
  tableName: string
  columnName: string
  alterSql: string
  lockTimeout?: string
  statementTimeout?: string
}

export const getPostgreSQLInstance = () =>
  request.get<any, PostgreSQLInstanceInfo>('/postgresql/instance')

export const getSQLForceWhitelist = () =>
  request.get<any, SQLForceWhitelistConfig>('/postgresql/sql-force-whitelist')

export const saveSQLForceWhitelist = (enabled: string[]) =>
  request.put<any, SQLForceWhitelistConfig>('/postgresql/sql-force-whitelist', { enabled })

export const listSQLViewDependencyTasks = (page = 1, pageSize = 20) =>
  request.get<any, PageResult<SQLViewDependencyTask>>('/postgresql/view-dependency-tasks', { params: { page, pageSize } })

export const createSQLViewDependencyTask = (data: SQLViewDependencyTaskRequest) =>
  request.post<any, SQLViewDependencyTask>('/postgresql/view-dependency-tasks', data)

export const getSQLViewDependencyTask = (id: number) =>
  request.get<any, { task: SQLViewDependencyTask; items: SQLViewDependencyItem[] }>(`/postgresql/view-dependency-tasks/${id}`)

export const analyzeSQLViewDependencyTask = (id: number) =>
  request.post<any, { task: SQLViewDependencyTask; items: SQLViewDependencyItem[] }>(`/postgresql/view-dependency-tasks/${id}/analyze`)

export const precheckSQLViewDependencyTask = (id: number) =>
  request.post<any, { task: SQLViewDependencyTask; items: SQLViewDependencyItem[] }>(`/postgresql/view-dependency-tasks/${id}/precheck`)

export const executeSQLViewDependencyTask = (id: number) =>
  request.post<any, { task: SQLViewDependencyTask; items: SQLViewDependencyItem[] }>(`/postgresql/view-dependency-tasks/${id}/execute`)

export const restoreSQLViewDependencyTask = (id: number) =>
  request.post<any, { task: SQLViewDependencyTask; items: SQLViewDependencyItem[] }>(`/postgresql/view-dependency-tasks/${id}/restore`)

export const parseSQLFile = (data: ParseSQLRequest) =>
  request.post<any, { file: SQLChangeFile; statements: SQLChangeStatement[] }>('/postgresql/sql-files/parse', data)

export const executeSQLFile = (id: number) =>
  request.post<any, { file: SQLChangeFile; statements: SQLChangeStatement[] }>(`/postgresql/sql-files/${id}/execute`)

export const executeSQLFileWithOptions = (id: number, options: SQLExecuteOptions) =>
  request.post<any, { file: SQLChangeFile; statements: SQLChangeStatement[] }>(`/postgresql/sql-files/${id}/execute`, { options })

export const executeSQLContent = (data: ParseSQLRequest & { options: SQLExecuteOptions }) =>
  request.post<any, { file: SQLChangeFile; statements: SQLChangeStatement[] }>('/postgresql/sql-files/execute-content', data)

export const cancelSQLFile = (id: number) =>
  request.post<any, SQLChangeFile>(`/postgresql/sql-files/${id}/cancel`)

export const parseSQLBatch = (data: { batchName?: string; overwrite?: boolean; files: ParseSQLBatchFile[] }) =>
  request.post<any, { batch: SQLChangeBatch; files: SQLChangeFile[] }>('/postgresql/sql-batches/parse', data)

export const executeSQLBatch = (id: number, options: SQLExecuteOptions) =>
  request.post<any, { batch: SQLChangeBatch; files: SQLChangeFile[] }>(`/postgresql/sql-batches/${id}/execute`, { options })

export const cancelSQLBatch = (id: number) =>
  request.post<any, SQLChangeBatch>(`/postgresql/sql-batches/${id}/cancel`)

export const listSQLBatches = (page = 1, pageSize = 20) =>
  request.get<any, PageResult<SQLChangeBatch>>('/postgresql/sql-batches', { params: { page, pageSize } })

export const getSQLBatch = (id: number) =>
  request.get<any, { batch: SQLChangeBatch; files: SQLChangeFile[] }>(`/postgresql/sql-batches/${id}`)

export const listSQLFiles = (page = 1, pageSize = 20) =>
  request.get<any, PageResult<SQLChangeFile>>('/postgresql/sql-files', { params: { page, pageSize } })

export const listTodoSQLFiles = (page = 1, pageSize = 20) =>
  request.get<any, PageResult<SQLChangeFile>>('/postgresql/sql-files/todo', { params: { page, pageSize } })

export const listDoneSQLFiles = (page = 1, pageSize = 20) =>
  request.get<any, PageResult<SQLChangeFile>>('/postgresql/sql-files/done', { params: { page, pageSize } })

export const getSQLFile = (id: number) =>
  request.get<any, { file: SQLChangeFile; statements: SQLChangeStatement[] }>(`/postgresql/sql-files/${id}`)

export const skipSQLStatement = (id: number) =>
  request.post<any, SQLChangeStatement>(`/postgresql/sql-statements/${id}/skip`)

export const skipSQLFile = (id: number) =>
  request.post<any, SQLChangeFile>(`/postgresql/sql-files/${id}/skip`)

export const deleteSQLFile = (id: number) =>
  request.delete<any, { deleted: boolean }>(`/postgresql/sql-files/${id}`)

export const importServerSQL = (filePath: string, overwrite = true) =>
  request.post<any, { count: number }>('/postgresql/sql-files/import-server', { filePath, overwrite })

export const exportNotExecutableSQLUrl = (id: number) =>
  `/api/postgresql/sql-files/${id}/not-executable.sql`

export const exportSQLViewDependencyPlanUrl = (id: number) =>
  `/api/postgresql/view-dependency-tasks/${id}/plan.sql`

export const exportSQLViewDependencyRestorePlanUrl = (id: number) =>
  `/api/postgresql/view-dependency-tasks/${id}/restore.sql`

export async function exportSQLViewDependencyRestorePlan(id: number) {
  const res = await fetch(exportSQLViewDependencyRestorePlanUrl(id), {
    headers: { Authorization: `Bearer ${getToken()}` },
  })
  if (!res.ok) throw new Error('导出失败')
  return await res.text()
}

export async function exportSQLViewDependencyPlan(id: number) {
  const res = await fetch(exportSQLViewDependencyPlanUrl(id), {
    headers: { Authorization: `Bearer ${getToken()}` },
  })
  if (!res.ok) throw new Error('导出失败')
  return await res.text()
}

export async function exportNotExecutableSQL(id: number) {
  const res = await fetch(exportNotExecutableSQLUrl(id), {
    headers: { Authorization: `Bearer ${getToken()}` },
  })
  if (!res.ok) throw new Error('导出失败')
  return await res.text()
}
