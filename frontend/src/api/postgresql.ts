import request, { type PageResult } from './request'

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
  fileName?: string
  content: string
}

export const getPostgreSQLInstance = () =>
  request.get<any, PostgreSQLInstanceInfo>('/postgresql/instance')

export const parseSQLFile = (data: ParseSQLRequest) =>
  request.post<any, { file: SQLChangeFile; statements: SQLChangeStatement[] }>('/postgresql/sql-files/parse', data)

export const executeSQLFile = (id: number) =>
  request.post<any, { file: SQLChangeFile; statements: SQLChangeStatement[] }>(`/postgresql/sql-files/${id}/execute`)

export const listSQLFiles = (page = 1, pageSize = 20) =>
  request.get<any, PageResult<SQLChangeFile>>('/postgresql/sql-files', { params: { page, pageSize } })

export const getSQLFile = (id: number) =>
  request.get<any, { file: SQLChangeFile; statements: SQLChangeStatement[] }>(`/postgresql/sql-files/${id}`)

export const skipSQLStatement = (id: number) =>
  request.post<any, SQLChangeStatement>(`/postgresql/sql-statements/${id}/skip`)
