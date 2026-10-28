import test from 'node:test'
import assert from 'node:assert/strict'
import { relationQuery, sampleTarget, planRows, formatDuration } from './database-workbench.mjs'
test('qualified object SQL quotes identifiers correctly',()=>assert.equal(relationQuery({schema:'odd schema',name:'a"b'}),'SELECT *\nFROM "odd schema"."a""b";'))
test('log samples only open on the same host, port and actual database',()=>{
 const source={host:'10.0.0.1',port:5432,database:'monitor'}
 const right={...source,id:2,database:'app'}
 assert.equal(sampleTarget(source,'app',[source,{...right,host:'10.0.0.2'},right]),right)
 assert.equal(sampleTarget(source,'unknown',[source,right]),undefined)
 assert.equal(sampleTarget(source,'',[source,right]),undefined)
})
test('plan rows preserve hierarchy and estimated values without inventing metrics',()=>{
 const tree={'Node Type':'Aggregate','Total Cost':10,Plans:[{'Node Type':'Seq Scan','Plan Rows':42}]}
 assert.deepEqual(planRows(tree).map(x=>[x.depth,x.node['Node Type']]),[[0,'Aggregate'],[1,'Seq Scan']])
 assert.equal(planRows(tree)[1].node['Actual Total Time'],undefined)
 assert.equal(formatDuration(null),'—')
})
