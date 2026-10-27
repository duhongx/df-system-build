package main

import (
	"context"
	"sync"
)

// afterCancel closes connections/listeners when their context ends, without
// requiring context.AfterFunc (introduced after the last Go supporting Win7).
func afterCancel(ctx context.Context, callback func()) func() {
	var mu sync.Mutex
	active := true
	stopped := make(chan struct{})
	go func() {
		select {
		case <-stopped:
			return
		case <-ctx.Done():
		}
		mu.Lock()
		if !active {
			mu.Unlock()
			return
		}
		active = false
		mu.Unlock()
		callback()
	}()
	return func() {
		mu.Lock()
		defer mu.Unlock()
		if active {
			active = false
			close(stopped)
		}
	}
}
