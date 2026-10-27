export interface ReleaseVersionSource {
  appVersion?: string
  releaseTime?: string
  releaseReadme?: string
  releaseManifestJson?: string
  releaseManifestError?: string
}

export function releaseValueText(value: unknown): string {
  if (value == null) return ''
  return typeof value === 'object' ? JSON.stringify(value, null, 2) : String(value)
}

export function releaseVersionContent(version: ReleaseVersionSource) {
  let raw: Record<string, unknown> = {}
  try {
    const parsed = JSON.parse(version.releaseManifestJson || '{}')
    if (parsed && typeof parsed === 'object' && !Array.isArray(parsed)) raw = parsed
  } catch { /* The persisted parser error is displayed alongside the original text. */ }
  const features = Array.isArray(raw.featureList) ? raw.featureList.map(releaseValueText) : []
  const parameters = Array.isArray(raw.appParamList) ? raw.appParamList.map(item => {
    if (!item || typeof item !== 'object' || Array.isArray(item)) return { name: '', description: releaseValueText(item), value: '' }
    return {
      name: releaseValueText(item.paramName ?? item.name),
      description: releaseValueText(item.paramDesc ?? item.description),
      value: releaseValueText(item.paramValue ?? item.value),
    }
  }) : []
  const known = new Set(['appVersion', 'releaseTime', 'readme', 'featureList', 'appParamList', 'fileList'])
  const businessFields = Object.entries(raw).filter(([key]) => !known.has(key)).map(([name, value]) => ({ name, value: releaseValueText(value) }))
  return { features, parameters, businessFields }
}

export function matchesReleaseVersion(version: ReleaseVersionSource & { versionNo: string }, search: string): boolean {
  const term = search.trim().toLocaleLowerCase()
  return !term || [version.appVersion, version.versionNo, version.releaseReadme].some(value => value?.toLocaleLowerCase().includes(term))
}
