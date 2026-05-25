import request, { type PageResult } from './request'

export interface IngressConfig {
  name: string
  host: string
}

export interface Application {
  id: number
  appName: string
  appType: 'java' | 'vue'
  vueRole?: 'main' | 'sub' | 'standalone' | ''
  isGateway?: boolean
  appCode?: string
  appDesc?: string
  gitRepo: string
  defaultBranch?: string
  nodePort?: number
  ingressHost?: string
  ingresses?: string
  configMapContent?: string
  envTags?: string
  artifactName?: string
  buildCommand?: string
  installCommand?: string
  builderImage?: string
  enabled?: boolean
  lastBuildStatus?: string | null
  lastBuildTime?: string | null
  createdAt?: string
  updatedAt?: string
}

export interface ListAppParams {
  page?: number
  pageSize?: number
  search?: string
  appType?: string
}

export function listApps(params?: ListAppParams) {
  return request.get<any, PageResult<Application>>('/applications', { params })
}

export function listAllApps() {
  return request.get<any, Application[]>('/applications/all')
}

export function getApp(id: number) {
  return request.get<any, Application>(`/applications/${id}`)
}

export interface CreateAppRequest {
  appName: string
  appType: 'java' | 'vue'
  gitRepo: string
  vueRole?: string
  isGateway?: boolean
  appCode?: string
  appDesc?: string
  nodePort?: number
  ingressHost?: string
  ingresses?: string
  configMapContent?: string
  envTags?: string
}

export function createApp(data: CreateAppRequest) {
  return request.post<any, Application>('/applications', data)
}

export function updateApp(id: number, data: Partial<CreateAppRequest>) {
  return request.put<any, Application>(`/applications/${id}`, data)
}

export function deleteApp(id: number) {
  return request.delete<any, null>(`/applications/${id}`)
}
