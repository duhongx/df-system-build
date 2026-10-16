import request from './request'

export const getK8sOverview = (namespace: string) =>
  request.get<any, any>('/kubernetes/overview', { params: { namespace } })

export const getK8sNodes = () =>
  request.get<any, any[]>('/kubernetes/nodes')

export const getK8sConfigMaps = (namespace: string) =>
  request.get<any, any>('/kubernetes/configmaps', { params: { namespace } })

export const getK8sConfigMap = (name: string, namespace: string) =>
  request.get<any, any>(`/kubernetes/configmaps/${name}`, { params: { namespace } })

export const updateK8sConfigMap = (name: string, namespace: string, data: Record<string, string>) =>
  request.put<any, any>(`/kubernetes/configmaps/${name}`, { data }, { params: { namespace } })

export const getK8sIngresses = (namespace: string) =>
  request.get<any, any>('/kubernetes/ingresses', { params: { namespace } })

export const getK8sDeployments = (namespace: string) =>
  request.get<any, any>('/kubernetes/deployments', { params: { namespace } })

export interface DeploymentRuntimeVersion {
  id: number
  namespace: string
  deploymentName: string
  appId: number
  appName: string
  appCode: string
  appType: string
  vueRole: string
  runtimeVersionPath: string
  image: string
  businessVersionJson: string
  status: string
  errorMessage: string
  syncedAt: string
}

export const syncK8sDeploymentRuntimeVersions = (namespace: string, deployments?: string[]) =>
  request.post<any, { runtimeVersions: DeploymentRuntimeVersion[] }>(
    '/kubernetes/deployments/runtime-versions/sync',
    { deployments: deployments || [] },
    { params: { namespace } },
  )

export const getK8sPods = (namespace: string) =>
  request.get<any, any>('/kubernetes/pods', { params: { namespace } })

export const getK8sServices = (namespace: string) =>
  request.get<any, any>('/kubernetes/services', { params: { namespace } })

export const updateK8sServicePorts = (name: string, namespace: string, data: { type: string; ports: any[] }) =>
  request.post<any, any>(`/kubernetes/services/${name}/update-ports`, data, { params: { namespace } })

export const deleteK8sService = (name: string, namespace: string) =>
  request.delete<any, any>(`/kubernetes/services/${name}`, { params: { namespace } })

export const getK8sPodLogs = (name: string, namespace: string, container?: string, tail?: number) =>
  request.get<any, { logs: string }>(`/kubernetes/pods/${name}/logs`, { params: { namespace, container, tail } })

export const restartK8sDeployment = (name: string, namespace: string) =>
  request.post<any, any>(`/kubernetes/deployments/${name}/restart`, null, { params: { namespace } })

export const scaleK8sDeployment = (name: string, namespace: string, replicas: number) =>
  request.post<any, any>(`/kubernetes/deployments/${name}/scale`, { replicas }, { params: { namespace } })

export const updateK8sImage = (name: string, namespace: string, image: string, container?: string) =>
  request.post<any, any>(`/kubernetes/deployments/${name}/image`, { image, container }, { params: { namespace } })

export const getK8sImageTags = (name: string, namespace: string) =>
  request.get<any, { currentImage: string; repository: string; images: string[] }>(`/kubernetes/deployments/${name}/tags`, { params: { namespace } })

export interface ContainerConfigEdit {
  name: string
  cpuRequest?: string
  cpuLimit?: string
  memoryRequest?: string
  memoryLimit?: string
  startupInitialDelay?: number
  readinessInitialDelay?: number
  livenessInitialDelay?: number
  startupFailureThreshold?: number
}
export interface DeploymentConfigEdit {
  uid: string
  resourceVersion: string
  replicas?: number
  containers: ContainerConfigEdit[]
}
export interface DeploymentConfigResult {
  recordWarning?: string
  deployment?: any
  expectedStrategy?: string
  changes?: { field: string; before: string; after: string }[]
  rolloutRequired?: boolean
  replicasChanged?: boolean
  waitSeconds: number
}
export const getDeploymentConfig = (name: string, namespace: string) =>
  request.get<any, DeploymentConfigResult>(`/kubernetes/deployments/${name}/config`, { params: { namespace } })
export const previewDeploymentConfig = (name: string, namespace: string, data: DeploymentConfigEdit) =>
  request.post<any, DeploymentConfigResult>(`/kubernetes/deployments/${name}/config/preview`, data, { params: { namespace } })
export const saveDeploymentConfig = (name: string, namespace: string, data: DeploymentConfigEdit) =>
  request.put<any, DeploymentConfigResult>(`/kubernetes/deployments/${name}/config`, data, { params: { namespace } })

export const getK8sTopNodes = () =>
  request.get<any, any[]>('/kubernetes/top/nodes')

export const getK8sTopPods = (namespace: string) =>
  request.get<any, any[]>('/kubernetes/top/pods', { params: { namespace } })

export const getK8sResourceYAML = (kind: string, name: string, namespace: string) =>
  request.get<any, { yaml: string }>(`/kubernetes/resource/${kind}/${name}`, { params: { namespace } })
