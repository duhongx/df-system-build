import request, { type PageResult } from './request'
import type { Application } from './application'
import type { BuildConfig } from './build-config'
import type { RemoteServer } from './settings'

export interface Task {
  id: number
  taskName: string
  applicationId: number
  application?: Application
  gitBranch: string
  buildConfigId: number
  buildConfig?: BuildConfig
  uploadPath: string
  targetServers?: RemoteServer[]
  deployMode?: string
  k8sNamespace?: string
  lastStatus?: string
  lastRunTime?: string | null
  lastDurationSeconds?: number | null
  enabled?: boolean
  createdAt?: string
  updatedAt?: string
}

export interface ListTaskParams {
  page?: number
  pageSize?: number
  search?: string
  appType?: string
}

export function listTasks(params?: ListTaskParams) {
  return request.get<any, PageResult<Task>>('/tasks', { params })
}

export function getTask(id: number) {
  return request.get<any, Task>(`/tasks/${id}`)
}

export interface CreateTaskRequest {
  taskName: string
  applicationId: number
  gitBranch: string
  buildConfigId: number
  deployMode?: string
  k8sNamespace?: string
}

export function createTask(data: CreateTaskRequest) {
  return request.post<any, Task>('/tasks', data)
}

export function updateTask(id: number, data: Partial<CreateTaskRequest>) {
  return request.put<any, Task>(`/tasks/${id}`, data)
}

export function deleteTask(id: number) {
  return request.delete<any, null>(`/tasks/${id}`)
}

export function executeTask(id: number, data: { gitBranch: string; autoDeploy?: boolean }) {
  return request.post<any, any>(`/tasks/${id}/execute`, data)
}

export function batchExecuteTasks(data: { taskIds: number[]; gitBranch: string; autoDeploy?: boolean }) {
  return request.post<any, any>('/tasks/batch-execute', data)
}
