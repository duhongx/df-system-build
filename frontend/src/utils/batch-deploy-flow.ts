export interface DeployFlowStep {
  key: string
  title: string
  description: string
  state: 'source' | 'check' | 'build' | 'deploy' | 'record'
}

export interface ImageReadyPipelineLike {
  id: number
  pipelineNo: string
  appName: string
  batchId?: string
  deployMode?: string
  triggerUser?: string
  createdAt?: string
}

export interface ImageReadyBatch {
  batchId: string
  source: string
  appCount: number
  readyCount: number
  failedCount: number
  triggerUser?: string
  createdAt?: string
  statusText: string
  pipelines: ImageReadyPipelineLike[]
}

export function buildDeployFlow(): DeployFlowStep[] {
  return [
    { key: 'build', title: '上传并制作镜像', description: '上传、校验并匹配制品，组装镜像后推送仓库；本步骤完成后不更新服务。', state: 'build' },
    { key: 'rollout', title: '滚动更新', description: '使用第一步已制作的镜像更新 Deployment，记录更新前镜像并等待滚动完成。', state: 'deploy' },
  ]
}

export function groupImageReadyPipelines(pipelines: ImageReadyPipelineLike[]): ImageReadyBatch[] {
  const groups = new Map<string, ImageReadyPipelineLike[]>()

  for (const pipeline of pipelines) {
    const batchId = pipeline.batchId?.trim()
    if (!batchId) {
      continue
    }
    const group = groups.get(batchId) || []
    group.push(pipeline)
    groups.set(batchId, group)
  }

  return Array.from(groups.entries())
    .map(([batchId, group]) => {
      const sorted = [...group].sort((a, b) => a.id - b.id)
      return {
        batchId,
        source: '批量滚动更新',
        appCount: sorted.length,
        readyCount: sorted.length,
        failedCount: 0,
        triggerUser: sorted[0]?.triggerUser,
        createdAt: sorted[0]?.createdAt,
        statusText: '镜像已就绪',
        pipelines: sorted,
      }
    })
    .sort((a, b) => {
      const aTime = a.createdAt ? new Date(a.createdAt).getTime() : 0
      const bTime = b.createdAt ? new Date(b.createdAt).getTime() : 0
      return aTime - bTime
    })
}
