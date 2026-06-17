export interface DeployableMatchLike {
  fileName: string
  matched: boolean
  valid: boolean
  skipped: boolean
}

export function isDeployableMatch(result: DeployableMatchLike) {
  return result.matched && result.valid && !result.skipped
}

export function selectedDeployableFileNames(results: DeployableMatchLike[]) {
  return results.filter(isDeployableMatch).map(result => result.fileName)
}
