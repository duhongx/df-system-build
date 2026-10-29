import request, { getToken, type PageResult } from './request'

export interface OpsDatabase {source?:string;backupServerId?:number;backupDirectory?:string;archiveDirectory?:string; id:number; name:string; host:string; port:number; database:string; username:string; sslMode:string; serverId:number; logEnabled:boolean; logDirectory:string; logFormat:string; logTimezone:string; logPrefix:string; slowThresholdMs:number; lookbackHours:number; lastLogSyncAt:string|null; logError:string; logStartAt:string|null; revision:number; password?:string }
export interface DatabaseActivity { pid:number; dbName:string; username:string; application:string; client:string; state:string; query:string; queryStart:string|null; transactionStart:string|null; elapsedSeconds:number; transactionSeconds:number; waitEventType:string; waitEvent:string; blockingPids:number[] }
export interface DatabaseLock {pid:number;lockType:string;mode:string;granted:boolean;object:string;waitStart:string|null;waitSeconds:number}
export interface DatabaseDiagnostic { checkedAt:string;version:string;database:string;username:string;role:string;connections:number;maxConnections:number;deadlocks:number;statsReset:string|null;activities:DatabaseActivity[];locks:DatabaseLock[];truncated:boolean;limit:number;warnings:string[] }
export interface DatabaseEvent {id:number;databaseId:number;type:string;eventTime:string;durationMs:number;phase:string;dbName:string;username:string;pid:number;client:string;application:string;sqlState:string;sql:string;message:string;detail:string;hint:string;context:string;raw?:string;sourcePath:string;sourceOffset:number;fingerprint:string}
export interface HostDiskIO {device:string;kind?:string;mappedName?:string;parentDevice?:string;backingDevices?:string[];mountpoints?:string[];sizeBytes?:number;readPerSecond:number;writePerSecond:number;readIOPS:number;writeIOPS:number;utilization:number;awaitMs:number;readAwaitMs?:number|null;writeAwaitMs?:number|null;averageQueue?:number;inFlight?:number}
export interface HostMetric {source?:string;sourceURL?:string;sampleSeconds?:number;exporterVersion?:string;collectorVersion?:string;collectedAt:string;hostname:string;os:string;kernel:string;cpuUsagePercent:number;cpuCores:number;cpuWaitPercent:number;cpuStealPercent?:number;cpuUserPercent?:number;cpuSystemPercent?:number;runnableTasks?:number|null;blockedTasks?:number|null;cpuPressure?:{avg10:number;avg60:number;avg300:number}|null;cpuPressureError?:string;cpuIntervalPressurePercent?:number|null;cpuSchedulingWaitMs?:number|null;cpuCoreIDs?:string[];cpuPerCore:number[];load1:number;load5:number;load15:number;memTotalBytes:number;memAvailableBytes:number;memUsedBytes:number;memUsagePercent:number;memCachedBytes:number;swapTotalBytes:number;swapUsedBytes:number;uptimeSeconds:number;disks:Array<{device:string;mountpoint:string;fsType:string;totalBytes:number;availableBytes:number;usedPercent:number;inodes:number;inodeUsedPercent:number}>;networks:Array<{name:string;receiveBytes:number;transmitBytes:number;receivePerSecond:number;transmitPerSecond:number;errors:number;drops:number}>;diskIO:HostDiskIO[];warnings:string[]}
export interface HostMonitor { config:{serverId:number;metricsURL?:string;enabled:boolean;intervalSeconds:number;lastAttemptAt:string|null;lastSampleAt:string|null;lastError:string;sshFingerprint:string};metrics:HostMetric|null;collecting:boolean }
export interface InspectionCommandOptions {profiles:string[];namespace?:string;logDirectory?:string;nacosPod?:string;nacosNamespace?:string;nacosLogDirectory?:string;archiveDirectory?:string;postgresBackupDirectory?:string;minioBackupDirectory?:string;minioAlias?:string;minioDailyFile?:string;patroniConfig?:string}
export interface InspectionExecution {kind:string;command:string;stdout:string;stderr:string;exitCode:number;startedAt:string;finishedAt:string;truncated:boolean;error:string}
export interface InspectionCommandResult {evidence?:string;tables?:Array<{title:string;columns:string[];rows:string[][];note?:string}>;code:string;name:string;status:string;value:string;reason:string;gaps:string[];executions:InspectionExecution[]}
export interface InspectionRef {type:'host'|'database'|'component';id:number}
export interface InspectionCheck {code:string;name:string;status:string;value:string;evidence:string;advice:string;error:string}
export interface InspectionResult {coverage?:InspectionSummary;expectedChecks?:string[];version:string;name:string;type:string;hostname:string;startedAt:string;finishedAt:string;checks:InspectionCheck[];error:string;metrics?:HostMetric;database?:DatabaseDiagnostic;reportChecks?:InspectionCommandResult[]}
export interface InspectionSummary {planned:number;completed:number;unknown:number;notRun:number;notApplicable:number;recorded:number;normal:number;warning:number;critical:number}
export interface InspectionCoverage {summary:InspectionSummary|null;legacy:boolean;executionLabel:string;failedTargets:number;uncertainTargets:number;issues:Array<{code:string;name:string;object:string;status:string;reason:string}>}
export interface InspectionRun {executionLabel?:string;id:number;planId:number;name:string;status:string;trigger:string;operator:string;totalTargets:number;completedTargets:number;warningCount:number;criticalCount:number;errorCount:number;cancelRequested:boolean;message:string;htmlStatus:string;pdfStatus:string;reportError:string;reportSummary?:InspectionSummary;startedAt:string|null;finishedAt:string|null;createdAt:string}
export interface InspectionTarget {id:number;runId:number;type:string;targetId:number;serverId:number;serverEndpoint:string;name:string;endpoint:string;status:string;stage:string;message:string;resultJSON:string;startedAt:string|null;finishedAt:string|null;heartbeatAt:string|null}
export interface InspectionPlan {commandsJSON?:string;id:number;name:string;enabled:boolean;dailyTime:string;timezone:string;targetsJSON:string;servicesJSON:string;nextRunAt:string|null;createdBy:string;lastError:string}
const base='/operations'
export const listOpsDatabases=()=>request.get<any,OpsDatabase[]>(`${base}/databases`)
export const saveOpsDatabase=(row:Partial<OpsDatabase>)=>row.id?request.put<any,OpsDatabase>(`${base}/databases/${row.id}`,row):request.post<any,OpsDatabase>(`${base}/databases`,row)
export const importOpsDatabase=()=>request.post<any,OpsDatabase>(`${base}/databases/import-current`)
export const deleteOpsDatabase=(id:number)=>request.delete(`${base}/databases/${id}`)
export const diagnoseDatabase=(id:number)=>request.get<any,DatabaseDiagnostic>(`${base}/databases/${id}/diagnostics`,{silentError:true})
export const detectDatabaseLogs=(id:number)=>request.post<any,Record<string,string>>(`${base}/databases/${id}/detect-logs`)
export const syncDatabaseLogs=(id:number)=>request.post(`${base}/databases/${id}/sync-logs`)
export const listDatabaseEvents=(id:number,params:Record<string,unknown>)=>request.get<any,PageResult<DatabaseEvent>>(`${base}/databases/${id}/events`,{params})
export const getDatabaseEvent=(id:number,event:number)=>request.get<any,DatabaseEvent>(`${base}/databases/${id}/events/${event}`)
export const getLogCursors=(id:number)=>request.get<any,Array<{id:number;path:string;offset:number;lastSize:number;lastError:string;updatedAt:string}>>(`${base}/databases/${id}/log-cursors`)
export const getHostMonitor=(id:number)=>request.get<any,HostMonitor>(`${base}/hosts/${id}/monitor`,{silentError:true})
export const configureHostMonitor=(id:number,enabled:boolean,intervalSeconds:number,metricsURL?:string)=>request.put(`${base}/hosts/${id}/monitor`,{enabled,intervalSeconds,...(metricsURL!==undefined?{metricsURL}:{})})
export const collectHostMetric=(id:number)=>request.post(`${base}/hosts/${id}/collect`)
export const getHostHistory=(id:number,hours:number)=>request.get<any,{samples:HostMetric[];stepSeconds:number;aggregation:string}>(`${base}/hosts/${id}/history`,{params:{hours},silentError:true})
export const listInspectionRuns=(page=1,pageSize=20,status='')=>request.get<any,PageResult<InspectionRun>>(`${base}/inspection-runs`,{params:{page,pageSize,status},silentError:true})
export const createInspection=(name:string,targets:InspectionRef[],services:string[]=[],commands?:Record<string,InspectionCommandOptions>)=>request.post<any,InspectionRun>(`${base}/inspection-runs`,{name,targets,services,commands})
export const getInspection=(id:number)=>request.get<any,{run:InspectionRun;targets:InspectionTarget[];coverage:InspectionCoverage}>(`${base}/inspection-runs/${id}`,{silentError:true})
export const getInspectionTarget=(run:number,id:number)=>request.get<any,InspectionTarget>(`${base}/inspection-runs/${run}/targets/${id}`)
export const cancelInspection=(id:number)=>request.post(`${base}/inspection-runs/${id}/cancel`)
export const reconcileInspection=(id:number)=>request.post(`${base}/inspection-runs/${id}/reconcile`)
export const regenerateInspectionReports=(id:number)=>request.post(`${base}/inspection-runs/${id}/reports`)
export const deleteInspection=(id:number)=>request.delete(`${base}/inspection-runs/${id}`)
export const listInspectionPlans=()=>request.get<any,InspectionPlan[]>(`${base}/inspection-plans`)
export const saveInspectionPlan=(row:{id?:number;name:string;enabled:boolean;dailyTime:string;timezone:string;targets:InspectionRef[];services:string[];commands?:Record<string,InspectionCommandOptions>})=>row.id?request.put(`${base}/inspection-plans/${row.id}`,row):request.post(`${base}/inspection-plans`,row)
export const deleteInspectionPlan=(id:number)=>request.delete(`${base}/inspection-plans/${id}`)
async function inspectionReportResponse(id:number,format:'html'|'pdf',signal?:AbortSignal){
 const response=await fetch(`/api${base}/inspection-runs/${id}/report/${format}`,{headers:{Authorization:`Bearer ${getToken()}`},signal})
 if(!response.ok||response.headers.get('content-type')?.includes('application/json')){let message='获取报告失败';try{message=(await response.json()).message||message}catch{};throw new Error(message)}
 return response
}
export async function getInspectionReportHTML(id:number,signal?:AbortSignal){
 return (await inspectionReportResponse(id,'html',signal)).text()
}
export async function downloadInspection(id:number,format:'pdf'='pdf'){
 const response=await inspectionReportResponse(id,format)
 const url=URL.createObjectURL(await response.blob());const a=document.createElement('a');a.href=url;a.download=`inspection-${id}.pdf`;a.click();setTimeout(()=>URL.revokeObjectURL(url),60000)
}
