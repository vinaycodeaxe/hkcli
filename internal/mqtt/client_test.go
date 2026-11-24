package mqtt

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestAQueuedWriteGivesUpWhenItsCallerDoes(t *testing.T) {
	c := &Client{sendSlot: make(chan struct{}, 1)}
	c.sendSlot <- struct{}{} // another write is stuck
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := c.acquireSendSlot(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("acquireSendSlot = %v; want the caller's deadline", err)
	}
	if waited := time.Since(start); waited > 2*time.Second {
		t.Errorf("waited %v behind a stuck write", waited)
	}
	<-c.sendSlot // the stuck write ends
	if err := c.acquireSendSlot(ctx); err == nil {
		t.Error("a caller that gave up still got the write slot")
	}
	if err := c.acquireSendSlot(context.Background()); err != nil {
		t.Fatalf("acquireSendSlot = %v; want the free slot", err)
	}
}
