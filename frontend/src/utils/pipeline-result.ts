export function hasCompletedDeploymentError(task: { deploymentProgress?: string; status: string; errorMessage?: string }) {
  return task.deploymentProgress === 'completed' && (task.status === 'FAILED' || !!task.errorMessage)
}
