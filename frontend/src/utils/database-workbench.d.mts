import type { OpsDatabase } from '../api/operations'
import type { RelationInfo } from '../api/database-workbench'
export function quoteIdentifier(value:string):string
export function relationQuery(relation:RelationInfo):string
export function formatDuration(ms:number|null|undefined):string
export function formatDate(text:string|null|undefined):string
export function targetLabel(db:OpsDatabase|undefined):string
export function sampleTarget(source:OpsDatabase,database:string,connections:OpsDatabase[]):OpsDatabase|undefined
export function planRows(plan:Record<string,any>|undefined):Array<{node:Record<string,any>;depth:number;id:number}>
