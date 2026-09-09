package k8s

import (
	"context"
	"testing"
	"time"
)

func TestDeploymentLockSerializesSameTargetAndHonorsCancellation(t *testing.T) {
	release, err := LockDeployment(context.Background(), "test-lock", "web-main")
	if err != nil {
		t.Fatal(err)
	}
	other, err := LockDeployment(context.Background(), "other-lock", "web-main")
	if err != nil {
		t.Fatal(err)
	}
	other()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if unlock, err := LockDeployment(ctx, "test-lock", "web-main"); err == nil {
		unlock()
		t.Fatal("same target overlapped")
	}
	release()
	again, err := LockDeployment(context.Background(), "test-lock", "web-main")
	if err != nil {
		t.Fatal(err)
	}
	again()
}
