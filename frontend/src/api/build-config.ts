import request from './request'

export interface BuildConfig {
  id: number
  name: string
  category: 'java' | 'vue'
  installCommand?: string
  buildCommand: string
  artifactDir?: string
  envVars?: string
  description?: string
  status?: string
}

export function listBuildConfigs() {
  return request.get<any, BuildConfig[]>('/build-configs')
}

export function createBuildConfig(data: Partial<BuildConfig>) {
  return request.post<any, BuildConfig>('/build-configs', data)
}

export function updateBuildConfig(id: number, data: Partial<BuildConfig>) {
  return request.put<any, BuildConfig>(`/build-configs/${id}`, data)
}

export function deleteBuildConfig(id: number) {
  return request.delete<any, null>(`/build-configs/${id}`)
}

export function getDefaultBuildConfigs() {
  return request.get<any, Partial<Record<'java' | 'vue', BuildConfig>>>('/build-configs/defaults')
}
