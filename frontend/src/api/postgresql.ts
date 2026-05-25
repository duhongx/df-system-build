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
}

export const getPostgreSQLInstance = () =>
  request.get<any, PostgreSQLInstanceInfo>('/postgresql/instance')

export const parseSQLFile = (data: ParseSQLRequest) =>
  request.post<any, { file: SQLChangeFile; statements: SQLChangeStatement[] }>('/postgresql/sql-files/parse', data)

export const executeSQLFile = (id: number) =>
  request.post<any, { file: SQLChangeFile; statements: SQLChangeStatement[] }>(`/postgresql/sql-files/${id}/execute`)

export const executeSQLFileWithOptions = (id: number, options: SQLExecuteOptions) =>
  request.post<any, { file: SQLChangeFile; statements: SQLChangeStatement[] }>(`/postgresql/sql-files/${id}/execute`, { options })

export const executeSQLContent = (data: ParseSQLRequest & { options: SQLExecuteOptions }) =>
  request.post<any, { file: SQLChangeFile; statements: SQLChangeStatement[] }>('/postgresql/sql-files/execute-content', data)

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

export async function exportNotExecutableSQL(id: number) {
  const res = await fetch(exportNotExecutableSQLUrl(id), {
    headers: { Authorization: `Bearer ${getToken()}` },
  })
  if (!res.ok) throw new Error('导出失败')
  return await res.text()
}
