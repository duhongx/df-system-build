package k8s

import (
	"context"
	"sync"
)

// Serializes local operations on a shared Deployment. This is deliberately a
// process-local guard, not a distributed scheduler or a multi-instance lease.
var deploymentLocks sync.Map

func LockDeployment(ctx context.Context, namespace, name string) (func(), error) {
	value, _ := deploymentLocks.LoadOrStore(namespace+"/"+name, make(chan struct{}, 1))
	token := value.(chan struct{})
	select {
	case token <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-token
			return nil, err
		}
		return func() { <-token }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
