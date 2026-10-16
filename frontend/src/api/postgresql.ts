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
  schemaConfigured?: boolean
  importMessage?: string
  createdUser?: string
  executionBatchId?: number
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

export interface SQLExecutionFileFilter { id: number; fileName: string; reason: string }

export interface SQLChangeBatch {
  stopRequested?: boolean
  stopUser?: string
  stopTime?: string
  historyIncomplete?: boolean
  filteredFiles?: SQLExecutionFileFilter[]
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
  resultOrigin?: string
  skipKind?: string
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
  executeUser?: string
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
}



export interface ParseSQLBatchFile {
  fileName: string
  content: string
}

export interface SQLViewDependencyTask {
  outcomeUncertain?: boolean
  id: number
  schemaName: string
  tableName: string
  columnName: string
  alterSql: string
  status: string
  executionMode: string
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

export interface SQLViewDependencyChange {
  id: number
  taskId: number
  changeOrder: number
  lineNumber: number
  schemaName: string
  tableName: string
  columnName: string
  alterSql: string
  status: string
  errorMessage: string
  createdAt: string
  updatedAt: string
}

export interface SQLViewDependencyStep {
  id: number
  taskId: number
  stepName: string
  stepOrder: number
  sqlContent: string
  status: string
  errorMessage: string
  startedAt?: string
  finishedAt?: string
  createdAt: string
  updatedAt: string
}

export interface SQLViewDependencyTaskResult {
  task: SQLViewDependencyTask
  items: SQLViewDependencyItem[]
  changes: SQLViewDependencyChange[]
  steps: SQLViewDependencyStep[]
}

export interface SQLViewDependencyTaskRequest {
  schemaName?: string
  tableName?: string
  columnName?: string
  alterSql: string
  executionMode?: string
  lockTimeout?: string
  statementTimeout?: string
}

export const getPostgreSQLInstance = () =>
  request.get<any, PostgreSQLInstanceInfo>('/postgresql/instance')

export const listSQLViewDependencyTasks = (page = 1, pageSize = 20) =>
  request.get<any, PageResult<SQLViewDependencyTask>>('/postgresql/view-dependency-tasks', { params: { page, pageSize } })

export const createSQLViewDependencyTask = (data: SQLViewDependencyTaskRequest) =>
  request.post<any, SQLViewDependencyTask>('/postgresql/view-dependency-tasks', data)

export const getSQLViewDependencyTask = (id: number) =>
  request.get<any, SQLViewDependencyTaskResult>(`/postgresql/view-dependency-tasks/${id}`)

export const analyzeSQLViewDependencyTask = (id: number) =>
  request.post<any, SQLViewDependencyTaskResult>(`/postgresql/view-dependency-tasks/${id}/analyze`)

export const precheckSQLViewDependencyTask = (id: number) =>
  request.post<any, SQLViewDependencyTaskResult>(`/postgresql/view-dependency-tasks/${id}/precheck`)

export const executeSQLViewDependencyTask = (id: number) =>
  request.post<any, SQLViewDependencyTaskResult>(`/postgresql/view-dependency-tasks/${id}/execute`)

export const restoreSQLViewDependencyTask = (id: number) =>
  request.post<any, SQLViewDependencyTaskResult>(`/postgresql/view-dependency-tasks/${id}/restore`)

export const restoreSQLViewDependencyItem = (taskId: number, itemId: number) =>
  request.post<any, SQLViewDependencyTaskResult>(`/postgresql/view-dependency-tasks/${taskId}/items/${itemId}/restore`)

export const verifySQLViewDependencyItem = (taskId: number, itemId: number) =>
  request.post<any, SQLViewDependencyTaskResult>(`/postgresql/view-dependency-tasks/${taskId}/items/${itemId}/verify`)

export const parseSQLFile = (data: ParseSQLRequest) =>
  request.post<any, { file: SQLChangeFile; statements: SQLChangeStatement[] }>('/postgresql/sql-files/parse', data)

export const executeSQLFile = (id: number) =>
  request.post<any, { file: SQLChangeFile; statements: SQLChangeStatement[] }>(`/postgresql/sql-files/${id}/execute`)

export const executeSQLFileWithOptions = (id: number, options: SQLExecuteOptions) =>
  request.post<any, { file: SQLChangeFile; statements: SQLChangeStatement[] }>(`/postgresql/sql-files/${id}/execute`, { options })

export const cancelSQLFile = (id: number) =>
  request.post<any, SQLChangeFile>(`/postgresql/sql-files/${id}/cancel`)

export const parseSQLBatch = (data: { batchName?: string; overwrite?: boolean; files: ParseSQLBatchFile[] }) =>
  request.post<any, { batch: SQLChangeBatch; files: SQLChangeFile[] }>('/postgresql/sql-batches/parse', data)

export const listSQLBatches = (page = 1, pageSize = 20) =>
  request.get<any, PageResult<SQLChangeBatch>>('/postgresql/sql-batches', { params: { page, pageSize } })

export const getSQLBatch = (id: number, current = false) =>
  request.get<any, { batch: SQLChangeBatch; files: SQLChangeFile[] }>(`/postgresql/sql-batches/${id}`, {params: current ? {current: true} : undefined})

export const listSQLFiles = (page = 1, pageSize = 20) =>
  request.get<any, PageResult<SQLChangeFile>>('/postgresql/sql-files', { params: { page, pageSize } })

export const listTodoSQLFiles = (page = 1, pageSize = 20, filters: { version?: string; schemaName?: string; fileName?: string } = {}) =>
  request.get<any, PageResult<SQLChangeFile>>('/postgresql/sql-files/todo', { params: { page, pageSize, ...filters } })

export const listDoneSQLFiles = (page = 1, pageSize = 20, filters: { version?: string; schemaName?: string; fileName?: string } = {}) =>
  request.get<any, PageResult<SQLChangeFile>>('/postgresql/sql-files/done', { params: { page, pageSize, ...filters } })

export const getSQLFile = (id: number) =>
  request.get<any, { file: SQLChangeFile; statements: SQLChangeStatement[] }>(`/postgresql/sql-files/${id}`)

export const skipSQLStatement = (id: number) =>
  request.post<any, SQLChangeStatement>(`/postgresql/sql-statements/${id}/skip`)

export const editSQLStatement = (id: number, content: string, originalContent: string) =>
  request.put<any, { file: SQLChangeFile; statements: SQLChangeStatement[] }>(`/postgresql/sql-statements/${id}/content`, { content, originalContent })

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

export const executeSelectedSQLFiles = (fileIds: number[], options: SQLExecuteOptions) =>
 request.post<any, {batch: SQLChangeBatch; files: SQLChangeFile[]}>('/postgresql/sql-files/execute', {fileIds, options})

export interface SQLStatementRecord {
  id: number; batchId: number; fileId: number; statementId: number; sourceStatementId?: number; fileName: string; schemaName: string;
  databaseHost: string; databasePort: string; databaseName: string; databaseUser: string;
  sqlHash: string; sqlContent: string; lineNumber: number; action: string; skipKind?: string; status: string;
  submitted: boolean | null; message: string; sqlState: string; operator: string; startedAt: string;
  finishedAt?: string; affectedRows: number; durationMs: number; beforeStatus: string; beforeMessage: string;
  beforeSqlState: string; beforeOperator: string; beforeTime?: string;
}
export interface SQLRecordFilter {
 statementId?: number; fileId?: number; batchId?: number; sqlHash?: string; keyword?: string;
 fileName?: string; schemaName?: string; action?: string; status?: string;
}
export const listSQLStatementRecords = (page = 1, pageSize = 30, filters: SQLRecordFilter = {}) =>
 request.get<any, PageResult<SQLStatementRecord>>('/postgresql/sql-records', { params: {page, pageSize, ...filters} })
export const cancelSQLFileSequence = (id: number) => request.post<any, SQLChangeBatch>(`/postgresql/sql-batches/${id}/cancel`)
export const setSQLFileExecutionConfig = (id: number, schemaName: string, originalSchemaName: string) =>
 request.put<any, {file: SQLChangeFile; statements: SQLChangeStatement[]}>(`/postgresql/sql-files/${id}/execution-config`, {schemaName, originalSchemaName})
export const getSQLBatchFile = (batchId: number, fileId: number) =>
 request.get<any, {file: SQLChangeFile; statements: SQLChangeStatement[]}>(`/postgresql/sql-batches/${batchId}/files/${fileId}`)
export const listSQLExecutionBatches = (page = 1, pageSize = 30) =>
 request.get<any, PageResult<SQLChangeBatch>>('/postgresql/sql-batches', {params:{page,pageSize,executionOnly:true}})
