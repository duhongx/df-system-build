import type {QueryResult} from '../api/database-workbench'
export interface ResultRow{index:number;values:Array<string|null>}
export function resultRows(result:QueryResult|undefined,keyword?:string,filter?:boolean,sortIndex?:number,order?:string):ResultRow[]
export function compareDecimal(x:string,y:string):number|undefined
export function delimited(columns:Array<{name:string}>,rows:Array<ResultRow|Array<string|null>>,separator?:string):string
export function textParts(value:string|null,keyword:string):Array<{text:string;match:boolean}>
