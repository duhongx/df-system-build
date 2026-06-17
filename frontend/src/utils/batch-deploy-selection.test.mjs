import assert from 'node:assert/strict'
import { selectedDeployableFileNames } from './compiled/batch-deploy-selection.js'

const results = [
  { fileName: 'his-gateway.jar', matched: true, valid: true, skipped: false },
  { fileName: 'broken.jar', matched: true, valid: false, skipped: false },
  { fileName: 'unknown.jar', matched: false, valid: true, skipped: false },
  { fileName: '15.1-sql.zip', matched: false, valid: true, skipped: true },
  { fileName: 'df-dataforge.jar', matched: true, valid: true, skipped: false },
]

assert.deepEqual(
  selectedDeployableFileNames(results),
  ['his-gateway.jar', 'df-dataforge.jar'],
)
