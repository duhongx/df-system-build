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
 executionStartedAt?: string
 resultStatus?: string
 totalStatements?: number
 successStatements?: number
 failedStatements?: number
 pendingStatements?: number
 skippedStatements?: number
 unknownStatements?: number
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

export interface SQLExecutionFileFilter { id: number; fileName: string; reason: string; code?: string }

export interface SQLChangeBatch {
  outcome?: 'COMPLETED' | 'ERROR' | 'CANCELED' | 'INTERRUPTED'
  finishedAt?: string
  stoppedFileId?: number
  stoppedFileName?: string
  deferredFiles?: number
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
 viewDependencyTaskId?: number
  databaseUser?: string
  transactionGroupId?: number
  transactionStartLine?: number
  transactionEndLine?: number
  transactionOutcome?: string
  resultOrigin?: string
  manualFailure?: boolean
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
 sourceFileId?: number
 sourceFileName?: string
 sourceSyncStatus?: string
 databaseTarget?: string
 databaseUser?: string
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
  updatedAt: string
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
  executionStepId?: number
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
  alterBatchCount?: number
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

export const getSQLViewDependencyTask = (id: number, silentError = false) =>
  request.get<any, SQLViewDependencyTaskResult>(`/postgresql/view-dependency-tasks/${id}`, { silentError })

// 多步 DDL 同步等待服务端完成；执行时限由服务端 lock_timeout / statement_timeout 控制。
const viewDependencyOperationOptions = { timeout: 0, silentError: true }

export const analyzeSQLViewDependencyTask = (id: number) =>
  request.post<any, SQLViewDependencyTaskResult>(`/postgresql/view-dependency-tasks/${id}/analyze`, undefined, viewDependencyOperationOptions)

export const precheckSQLViewDependencyTask = (id: number) =>
  request.post<any, SQLViewDependencyTaskResult>(`/postgresql/view-dependency-tasks/${id}/precheck`, undefined, viewDependencyOperationOptions)

export const executeSQLViewDependencyTask = (id: number) =>
  request.post<any, SQLViewDependencyTaskResult>(`/postgresql/view-dependency-tasks/${id}/execute`, undefined, viewDependencyOperationOptions)

export const restoreSQLViewDependencyTask = (id: number) =>
  request.post<any, SQLViewDependencyTaskResult>(`/postgresql/view-dependency-tasks/${id}/restore`, undefined, viewDependencyOperationOptions)

export const restoreSQLViewDependencyItem = (taskId: number, itemId: number) =>
  request.post<any, SQLViewDependencyTaskResult>(`/postgresql/view-dependency-tasks/${taskId}/items/${itemId}/restore`, undefined, viewDependencyOperationOptions)

export const verifySQLViewDependencyItem = (taskId: number, itemId: number) =>
  request.post<any, SQLViewDependencyTaskResult>(`/postgresql/view-dependency-tasks/${taskId}/items/${itemId}/verify`, undefined, viewDependencyOperationOptions)

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

export const listTodoSQLFiles = (page = 1, pageSize = 20, filters: { version?: string; schemaName?: string; fileName?: string; status?: string } = {}) =>
  request.get<any, PageResult<SQLChangeFile>>('/postgresql/sql-files/todo', { params: { page, pageSize, ...filters } })

export const listDoneSQLFiles = (page = 1, pageSize = 20, filters: { version?: string; schemaName?: string; fileName?: string; status?: string } = {}) =>
  request.get<any, PageResult<SQLChangeFile>>('/postgresql/sql-files/done', { params: { page, pageSize, ...filters } })

export const getSQLFile = (id: number) =>
  request.get<any, { file: SQLChangeFile; statements: SQLChangeStatement[] }>(`/postgresql/sql-files/${id}`)

export const skipSQLStatement = (id: number) =>
  request.post<any, SQLChangeStatement>(`/postgresql/sql-statements/${id}/skip`)

export const markSQLStatementFailed = (id: number) =>
  request.post<any, SQLChangeStatement>(`/postgresql/sql-statements/${id}/fail`)

export interface SQLFileFailureResult {
  file: SQLChangeFile
  statements: SQLChangeStatement[]
  markedCount: number
  deferredCount: number
  preservedCount: number
  linkedCount: number
}
export const markSQLFileFailed = (id: number) =>
  request.post<any, SQLFileFailureResult>(`/postgresql/sql-files/${id}/fail`)

export interface SQLRetryExecutionAccount {
  username: string
  passwordSource: 'configured' | 'manual'
  password?: string
}
export interface SQLRetryAccountChoice { username: string; passwordSource: 'admin' | 'common' | 'override' }
export interface SQLRetryTarget {
  statementId: number
  fileId: number
  fileName: string
  lineNumber: number
  schemaName: string
  originalAccount?: string
  transactionLabel?: string
}
export const listSQLRetryAccounts = () => request.get<any, SQLRetryAccountChoice[]>('/postgresql/sql-retry-accounts')
export const executeFailedSQLStatement = (id: number, options: SQLExecuteOptions, account?: SQLRetryExecutionAccount) =>
  request.post<any, { file: SQLChangeFile; statements: SQLChangeStatement[] }>(`/postgresql/sql-statements/${id}/execute`, { options, account }, { timeout: 30000 })

export const editSQLStatement = (id: number, content: string, originalContent: string) =>
  request.put<any, { file: SQLChangeFile; statements: SQLChangeStatement[] }>(`/postgresql/sql-statements/${id}/content`, { content, originalContent })

export interface SQLFileEdit {
  id: number
  fileId: number
  fileName: string
  schemaName: string
  beforeStatus: string
  preservedStatements: number
  newStatements: number
  removedStatements: number
  editor: string
  createdAt: string
  beforeContent?: string
  afterContent?: string
}
export interface SQLFileEditResult { file: SQLChangeFile; statements: SQLChangeStatement[]; edit: SQLFileEdit }
export const editSQLFile = (id: number, content: string, originalContent: string) =>
  request.put<any, SQLFileEditResult>(`/postgresql/sql-files/${id}/content`, {content, originalContent}, {timeout: 60000, silentError: true})
export const listSQLFileEdits = (id: number, page = 1, pageSize = 20) =>
  request.get<any, PageResult<SQLFileEdit>>(`/postgresql/sql-files/${id}/edits`, {params: {page, pageSize}})
export const getSQLFileEdit = (id: number, editId: number) =>
  request.get<any, {edit: SQLFileEdit; beforeStatements: SQLChangeStatement[]}>(`/postgresql/sql-files/${id}/edits/${editId}`)

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
  transactionGroupId?: number; transactionOutcome?: string;
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

export interface SQLCurrentFailure extends SQLChangeStatement {
 fileName: string; schemaName: string; version: string; taskStatus?: string; taskMessage?: string;
}
export const listCurrentFailedSQL = (page=1,pageSize=30,filters: {keyword?:string;fileName?:string;schemaName?:string;handling?:string;fileId?:number}={}) =>
 request.get<any,PageResult<SQLCurrentFailure>>('/postgresql/sql-failures',{params:{page,pageSize,...filters}})
export const transferFailedSQLToViewTask = (id:number) =>
 request.post<any,SQLViewDependencyTask>(`/postgresql/sql-statements/${id}/view-dependency`)

export interface SQLFileViewTransferTarget {
  fileId: number
  all?: boolean
  statementIds?: number[]
}
export interface SQLViewTransferUnit {
  id: number
  startLine: number
  endLine: number
  transaction: boolean
  changeCount: number
  tables: string[]
  targets: string[]
  statements: SQLChangeStatement[]
  contextKey: string
}
export interface SQLFileViewTransferPreview {
  fileId: number
  fileName: string
  units: SQLViewTransferUnit[]
  excluded: {id: number; startLine: number; endLine: number; reason: string; taskIds?: number[]}[]
  previewHash: string
}
export const previewSQLFileViewTransfer = (target: SQLFileViewTransferTarget) =>
  request.post<any, SQLFileViewTransferPreview>(`/postgresql/sql-files/${target.fileId}/view-dependency/preview`, {all: !!target.all, statementIds: target.statementIds}, viewDependencyOperationOptions)
export const transferSQLFileToViewTask = (target: SQLFileViewTransferTarget, selectedIds: number[], previewHash: string) =>
  request.post<any, SQLViewDependencyTask>(`/postgresql/sql-files/${target.fileId}/view-dependency`, {all: !!target.all, statementIds: target.statementIds, selectedIds, previewHash}, viewDependencyOperationOptions)

export const cancelSQLViewTaskTransfer=(id:number)=>request.post<any,null>(`/postgresql/view-dependency-tasks/${id}/cancel-transfer`)
