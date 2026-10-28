import type {RelationInfo,RelationDetail} from '../api/database-workbench'
export function tableQuery(relation:RelationInfo,columns:RelationDetail['columns'],options?:{columns?:string[];filterColumn?:string;operator?:string;value?:string;sortColumn?:string;desc?:boolean;pageSize?:number;page?:number}):{sql:string;parameters:unknown[]}
