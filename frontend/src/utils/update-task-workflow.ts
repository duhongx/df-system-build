export const updateTaskSteps = ['上传制品', '应用匹配', '制作镜像', '滚动更新']

export interface UpdatePipeline {
  status: string
  deploymentProgress?: string
  errorMessage?: string
  stages?: { stageCode: string; status: string }[]
}

export function imageState(pipeline: UpdatePipeline) {
  if (['IMAGE_READY', 'DEPLOYING', 'SUCCESS'].includes(pipeline.status) || pipeline.stages?.some(stage => stage.stageCode === 'PUSH_IMAGE' && stage.status === 'SUCCESS')) return 'ready'
  if (['FAILED', 'CANCELED'].includes(pipeline.status) || pipeline.stages?.some(stage => ['BUILD_IMAGE', 'PUSH_IMAGE'].includes(stage.stageCode) && stage.status === 'FAILED')) return 'failed'
  return 'pending'
}

export function imagesSettled(pipelines: UpdatePipeline[]) {
  return pipelines.length > 0 && pipelines.every(pipeline => imageState(pipeline) !== 'pending')
}

export function rolloutState(pipeline: UpdatePipeline) {
  if (pipeline.deploymentProgress === 'completed' && (pipeline.status === 'FAILED' || pipeline.errorMessage)) return 'record_error'
  if (pipeline.status === 'SUCCESS') return 'success'
  if (pipeline.status === 'CANCELED') return 'canceled'
  if (pipeline.status === 'FAILED') return imageState(pipeline) === 'ready' ? 'failed' : 'image_failed'
  if (pipeline.status === 'IMAGE_READY') return 'ready'
  if (pipeline.status === 'DEPLOYING' || pipeline.stages?.some(stage => stage.stageCode === 'K8S_DEPLOY' && stage.status === 'RUNNING')) return 'running'
  return 'pending'
}

export function needsExecutionPolling(pipelines: UpdatePipeline[]) {
  return pipelines.some(pipeline => !['SUCCESS', 'FAILED', 'CANCELED', 'IMAGE_READY'].includes(pipeline.status))
}

export function parseSubmittedPipelineIds(value: unknown) {
  if (typeof value !== 'string') return []
  return [...new Set(value.split(',').filter(id => /^[1-9]\d*$/.test(id)).map(Number).filter(Number.isSafeInteger))].slice(0, 300)
}
