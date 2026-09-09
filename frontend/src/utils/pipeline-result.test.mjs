import assert from 'node:assert/strict'
import { hasCompletedDeploymentError } from './compiled/pipeline-result.js'
assert.equal(hasCompletedDeploymentError({ deploymentProgress: 'completed', status: 'FAILED' }), true)
assert.equal(hasCompletedDeploymentError({ deploymentProgress: 'submitted', status: 'FAILED' }), false)
assert.equal(hasCompletedDeploymentError({ deploymentProgress: 'completed', status: 'SUCCESS' }), false)
assert.equal(hasCompletedDeploymentError({ deploymentProgress: 'completed', status: 'RUNNING', errorMessage: 'save failed' }), true)
