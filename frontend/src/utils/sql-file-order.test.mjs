import assert from 'node:assert/strict'
import {compareSQLVersions, compareSQLFiles} from './sql-file-order.ts'
for (const [a,b] of [['V2','V10'],['15.3_SP2_Patch9','15.3_SP2_Patch10'],['V1.9','V1.10'],['V99999999999999999999','V100000000000000000000']]) {
 assert.ok(compareSQLVersions(a,b)<0)
 assert.ok(compareSQLVersions(b,a)>0)
}
assert.equal(compareSQLVersions('V002','V2'),0)
const files=[{version:'V10',groupSortNo:1,fileName:'ten',id:1},{version:'V2',groupSortNo:12,fileName:'second',id:2},{version:'V2',groupSortNo:2,fileName:'first',id:3}]
assert.deepEqual(files.sort(compareSQLFiles).map(f=>f.id),[3,2,1])
console.log('SQL natural version order checks passed')
