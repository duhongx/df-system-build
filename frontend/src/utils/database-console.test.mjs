import test from 'node:test'
import assert from 'node:assert/strict'
import {resultRows,compareDecimal,delimited,textParts} from './database-results.mjs'
import {tableQuery} from './database-table-query.mjs'
import {format} from 'sql-formatter'
test('large PostgreSQL numerics sort without JavaScript precision loss',()=>{
 const result={columns:[{name:'id',type:'int8'}],rows:[['9007199254740993'],['9007199254740992'],[null],['-20'],['-3']]}
 assert.deepEqual(resultRows(result,'',false,0).map(r=>r.values[0]),['-20','-3','9007199254740992','9007199254740993',null])
 assert.equal(compareDecimal('1.200000','1.2'),0);assert.ok(compareDecimal('1e-5','0.000011')<0);assert.ok(compareDecimal('-0.00001','0')<0)
 assert.deepEqual(resultRows(result,'',false,0,'descending').at(-1).values,[null])
})
test('result filters keep row identity and exports preserve nulls, quotes, newlines and duplicate names',()=>{
 const result={columns:[{name:'同名',type:'text'},{name:'同名',type:'text'}],rows:[['普通','x'],['含,逗号','两行\n"引号"'],[null,''] ]}
 assert.equal(resultRows(result,'逗号',true)[0].index,2)
 assert.equal(delimited(result.columns,result.rows),'同名,同名\r\n普通,x\r\n"含,逗号","两行\n""引号"""\r\n\\N,\r\n')
 assert.equal(textParts('<script>alert(1)</script>','script').map(p=>p.text).join(''),'<script>alert(1)</script>')
})
test('table browsing quotes metadata identifiers, binds filter values and orders by a primary key tie breaker',()=>{
 const relation={schema:'业务"库',name:'odd table'},columns=[{name:'key"id',primaryKey:true},{name:'value',primaryKey:false}]
 const q=tableQuery(relation,columns,{filterColumn:'value',operator:'=',value:"x' OR 1=1 --",sortColumn:'value',desc:true,page:2,pageSize:50})
 assert.ok(q.sql.includes('"业务""库"."odd table"'));assert.ok(q.sql.includes('WHERE "value" = $1'));assert.ok(q.sql.includes('ORDER BY "value" DESC, "key""id" ASC'));assert.ok(q.sql.endsWith('LIMIT 51 OFFSET 50;'));assert.deepEqual(q.parameters,["x' OR 1=1 --"]);assert.ok(!q.sql.includes('OR 1=1'))
 assert.throws(()=>tableQuery(relation,columns,{filterColumn:'value',operator:'= $1; DROP TABLE x'}),/操作符/)
 const contains=tableQuery(relation,columns,{filterColumn:'value',operator:'contains',value:'20%_\\'});assert.deepEqual(contains.parameters,['%20\\%\\_\\\\%'])
 assert.throws(()=>tableQuery(relation,columns,{page:-1}),/分页/)
})
test('PostgreSQL formatting preserves quoted strings, comments and dollar quoted function bodies',()=>{
 const sql=`select 'a;  b' as "Odd Name", $1::text -- keep this comment\nfrom "odd table" where id=$2;`
 const formatted=format(sql,{language:'postgresql',keywordCase:'upper'});assert.ok(formatted.includes("'a;  b'"));assert.ok(formatted.includes('"Odd Name"'));assert.ok(formatted.includes('-- keep this comment'));assert.ok(formatted.includes('$1'));assert.ok(formatted.includes('$2'))
 const body=`CREATE FUNCTION f() RETURNS text LANGUAGE SQL AS $$ SELECT 'x; y'; $$;`;assert.ok(format(body,{language:'postgresql'}).includes("SELECT 'x; y';"))
})
