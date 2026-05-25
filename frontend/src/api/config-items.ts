import request from './request'

export interface ConfigItem {
  id: number
  name: string
  code: string
  category: string // dockerfile / k8s / script
  contentType: string // text / yaml / shell
  content: string
  description: string
  createdAt?: string
  updatedAt?: string
}

export const listConfigItems = (category?: string) =>
  request.get<any, ConfigItem[]>('/config-items', { params: category ? { category } : {} })

export const getConfigItem = (id: number) =>
  request.get<any, ConfigItem>(`/config-items/${id}`)

export const createConfigItem = (data: Partial<ConfigItem>) =>
  request.post<any, ConfigItem>('/config-items', data)

export const updateConfigItem = (id: number, data: Partial<ConfigItem>) =>
  request.put<any, ConfigItem>(`/config-items/${id}`, data)

export const deleteConfigItem = (id: number) =>
  request.delete<any, null>(`/config-items/${id}`)
