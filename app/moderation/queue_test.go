package moderation

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInMemoryQueuePublishAndConsume(t *testing.T) {
	t.Parallel()

	q := NewInMemoryQueue(1)
	defer q.Close()

	event := IncomingEvent{
		EventID:       "evt-1",
		CorrelationID: "corr-1",
		Source:        "telegram.webhook",
		ReceivedAt:    time.Now().UTC(),
	}

	err := q.Publish(context.Background(), event)
	require.NoError(t, err)

	select {
	case got := <-q.Consume():
		assert.Equal(t, event, got)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for event")
	}
}

func TestInMemoryQueueConcurrentPublishAndClose(t *testing.T) {
	q := NewInMemoryQueue(32)
	var publishers sync.WaitGroup
	errs := make(chan error, 100)
	for range 100 {
		publishers.Go(func() {
			errs <- q.Publish(context.Background(), IncomingEvent{EventID: "concurrent"})
		})
	}

	consumerDone := make(chan struct{})
	consumed := 0
	go func() {
		for range q.Consume() {
			consumed++
		}
		close(consumerDone)
	}()
	q.Close()
	publishers.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			require.ErrorIs(t, err, ErrQueueClosed)
		}
	}
	select {
	case <-consumerDone:
		assert.LessOrEqual(t, consumed, 100)
	case <-time.After(time.Second):
		t.Fatal("consumer did not stop")
	}
}

func TestInMemoryQueuePublishCanceledContext(t *testing.T) {
	t.Parallel()

	q := NewInMemoryQueue(0)
	defer q.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := q.Publish(ctx, IncomingEvent{EventID: "evt-2"})
	require.ErrorIs(t, err, context.Canceled)
}

func TestInMemoryQueuePublishAfterClose(t *testing.T) {
	t.Parallel()

	q := NewInMemoryQueue(1)
	q.Close()

	err := q.Publish(context.Background(), IncomingEvent{EventID: "evt-3"})
	require.ErrorIs(t, err, ErrQueueClosed)
}

func TestInMemoryQueueCloseClosesConsumerChannel(t *testing.T) {
	t.Parallel()

	q := NewInMemoryQueue(1)
	ch := q.Consume()

	q.Close()

	select {
	case _, ok := <-ch:
		assert.False(t, ok)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for queue close")
	}
}
