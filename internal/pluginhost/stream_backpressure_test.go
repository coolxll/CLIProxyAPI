package pluginhost

import (
	"bytes"
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// TestOutputStreamBackpressureBlocksEmit verifies that the 16-chunk output
// stream buffer provides natural backpressure: once full, emit blocks until
// a consumer drains a chunk.
func TestOutputStreamBackpressureBlocksEmit(t *testing.T) {
	bridge := newStreamBridge()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	streamID, chunks, cleanup := bridge.open(ctx)
	defer cleanup()

	// Fill the 16-chunk buffer.
	for i := 0; i < 16; i++ {
		if err := bridge.emit(ctx, streamID, pluginapi.ExecutorStreamChunk{
			Payload: []byte("chunk"),
		}); err != nil {
			t.Fatalf("emit %d failed: %v", i, err)
		}
	}

	// The 17th emit should block because the buffer is full.
	blocked := make(chan error, 1)
	go func() {
		blocked <- bridge.emit(ctx, streamID, pluginapi.ExecutorStreamChunk{
			Payload: []byte("overflow"),
		})
	}()

	select {
	case <-blocked:
		t.Fatal("17th emit should block when buffer is full")
	case <-time.After(100 * time.Millisecond):
		// Expected: emit is blocked.
	}

	// Drain one chunk from the consumer side.
	select {
	case <-chunks:
		// Good, drained one chunk.
	case <-time.After(time.Second):
		t.Fatal("timed out waiting to drain chunk")
	}

	// Now the 17th emit should unblock.
	select {
	case err := <-blocked:
		if err != nil {
			t.Fatalf("emit after drain failed: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("17th emit did not unblock after draining")
	}
}

// TestOutputStreamCancelClosesChannel verifies that canceling the stream's
// parent context closes the chunk channel, signaling completion to consumers.
func TestOutputStreamCancelClosesChannel(t *testing.T) {
	bridge := newStreamBridge()
	ctx, cancel := context.WithCancel(context.Background())

	streamID, chunks, cleanup := bridge.open(ctx)
	defer cleanup()

	// Emit a chunk to verify the stream is working.
	if err := bridge.emit(ctx, streamID, pluginapi.ExecutorStreamChunk{
		Payload: []byte("hello"),
	}); err != nil {
		t.Fatalf("emit failed: %v", err)
	}

	// Cancel the context.
	cancel()

	// The channel should close, allowing consumers to drain and detect completion.
	deadline := time.After(2 * time.Second)
	for {
		select {
		case _, ok := <-chunks:
			if !ok {
				// Channel closed — expected.
				return
			}
			// Drain remaining chunks (including the error chunk from close).
		case <-deadline:
			t.Fatal("chunk channel was not closed after context cancel")
		}
	}
}

// TestHTTPStreamBridgeCancelClosesUpstream verifies that closing an HTTP
// stream via the bridge invokes the stored cancel function, which propagates
// cancellation to the upstream producer.
func TestHTTPStreamBridgeCancelClosesUpstream(t *testing.T) {
	bridge := newHostHTTPStreamBridge()

	cancelled := make(chan struct{})
	cancel := func() { close(cancelled) }

	// Create a producer channel that never sends.
	producer := make(chan pluginapi.HTTPStreamChunk)

	streamID := bridge.open(producer, cancel)

	// Close the stream from the consumer side.
	bridge.close(streamID)

	// The cancel function should have been called.
	select {
	case <-cancelled:
		// Expected.
	case <-time.After(time.Second):
		t.Fatal("cancel function was not called after bridge.close()")
	}
}

// TestHTTPStreamBridgeReadCancelClosesUpstream verifies that canceling the
// read context triggers upstream cancellation.
func TestHTTPStreamBridgeReadCancelClosesUpstream(t *testing.T) {
	bridge := newHostHTTPStreamBridge()

	cancelled := make(chan struct{})
	cancel := func() { close(cancelled) }

	// Producer that never sends.
	producer := make(chan pluginapi.HTTPStreamChunk)

	streamID := bridge.open(producer, cancel)

	// Read with a context that we cancel.
	readCtx, readCancel := context.WithCancel(context.Background())

	readDone := make(chan error, 1)
	go func() {
		_, _, err := bridge.read(readCtx, streamID)
		readDone <- err
	}()

	// Give the read goroutine time to start waiting.
	time.Sleep(50 * time.Millisecond)
	readCancel()

	select {
	case err := <-readDone:
		if err == nil {
			t.Fatal("read should return error after context cancel")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("read did not return after context cancel")
	}

	select {
	case <-cancelled:
		// Expected: upstream cancel was invoked.
	case <-time.After(time.Second):
		t.Fatal("upstream cancel was not called after read context cancel")
	}
}

// TestHTTPStreamMemoryBounded verifies that the HTTP stream producer goroutine
// respects context cancellation even when producing many chunks, ensuring
// memory stays bounded.
func TestHTTPStreamMemoryBounded(t *testing.T) {
	bridge := newHostHTTPStreamBridge()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Producer that sends 1000 chunks as fast as possible.
	var produced atomic.Int64
	producer := make(chan pluginapi.HTTPStreamChunk)
	go func() {
		defer close(producer)
		for i := 0; i < 1000; i++ {
			select {
			case producer <- pluginapi.HTTPStreamChunk{
				Payload: bytes.Repeat([]byte("x"), 1024),
			}:
				produced.Add(1)
			case <-ctx.Done():
				return
			}
		}
	}()

	streamID := bridge.open(producer, cancel)

	// Read slowly — 5 chunks then cancel.
	for i := 0; i < 5; i++ {
		_, done, err := bridge.read(ctx, streamID)
		if done || err != nil {
			break
		}
	}

	// Cancel the context to stop the producer.
	cancel()

	// Wait for the producer to stop.
	time.Sleep(200 * time.Millisecond)

	// The producer should have stopped well before 1000 chunks.
	count := produced.Load()
	if count >= 1000 {
		t.Errorf("producer sent all 1000 chunks despite cancellation (produced=%d)", count)
	}
}

// TestOutputStreamEmitRespectsContextCancel verifies that emit returns an
// error when the context is canceled while the buffer is full.
func TestOutputStreamEmitRespectsContextCancel(t *testing.T) {
	bridge := newStreamBridge()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	streamID, _, cleanup := bridge.open(ctx)
	defer cleanup()

	// Fill the 16-chunk buffer.
	for i := 0; i < 16; i++ {
		if err := bridge.emit(ctx, streamID, pluginapi.ExecutorStreamChunk{
			Payload: []byte("fill"),
		}); err != nil {
			t.Fatalf("emit %d failed: %v", i, err)
		}
	}

	// Start an emit that will block, then cancel the context.
	var wg sync.WaitGroup
	wg.Add(1)
	var emitErr error
	go func() {
		defer wg.Done()
		emitErr = bridge.emit(ctx, streamID, pluginapi.ExecutorStreamChunk{
			Payload: []byte("blocked"),
		})
	}()

	// Give the goroutine time to block on the channel send.
	time.Sleep(50 * time.Millisecond)
	cancel()

	wg.Wait()

	if emitErr == nil {
		t.Fatal("emit should return error after context cancel")
	}
}

func TestOutputStreamConcurrentCloseUnblocksPendingEmit(t *testing.T) {
	bridge := newStreamBridge()
	streamID, _, cleanup := bridge.open(context.Background())
	defer cleanup()

	for i := 0; i < 16; i++ {
		if err := bridge.emit(context.Background(), streamID, pluginapi.ExecutorStreamChunk{
			Payload: []byte("fill"),
		}); err != nil {
			t.Fatalf("emit %d failed: %v", i, err)
		}
	}

	emitDone := make(chan error, 1)
	go func() {
		emitDone <- bridge.emit(context.Background(), streamID, pluginapi.ExecutorStreamChunk{
			Payload: []byte("blocked"),
		})
	}()

	bridge.close(streamID, "upstream failed")
	select {
	case err := <-emitDone:
		if err == nil {
			t.Fatal("pending emit succeeded after the stream closed")
		}
	case <-time.After(time.Second):
		t.Fatal("pending emit remained blocked after the stream closed")
	}
}

// TestHTTPStreamBridgeDoubleCloseSafe verifies that closing a stream twice
// does not panic.
func TestHTTPStreamBridgeDoubleCloseSafe(t *testing.T) {
	bridge := newHostHTTPStreamBridge()

	var cancelCount atomic.Int64
	cancel := func() { cancelCount.Add(1) }

	producer := make(chan pluginapi.HTTPStreamChunk)
	streamID := bridge.open(producer, cancel)

	bridge.close(streamID)
	bridge.close(streamID) // Should not panic.

	// Cancel should only be called once despite double close.
	if count := cancelCount.Load(); count != 1 {
		t.Errorf("cancel called %d times, want 1", count)
	}
}

// TestHTTPStreamBridgeReadDrainsProducer verifies that reading all chunks
// from a producer that closes naturally signals done.
func TestHTTPStreamBridgeReadDrainsProducer(t *testing.T) {
	bridge := newHostHTTPStreamBridge()

	// Producer that sends 3 chunks then closes.
	producer := make(chan pluginapi.HTTPStreamChunk, 3)
	producer <- pluginapi.HTTPStreamChunk{Payload: []byte("a")}
	producer <- pluginapi.HTTPStreamChunk{Payload: []byte("b")}
	producer <- pluginapi.HTTPStreamChunk{Payload: []byte("c")}
	close(producer)

	cancelled := make(chan struct{})
	cancel := func() { close(cancelled) }

	streamID := bridge.open(producer, cancel)
	ctx := context.Background()

	// Read all 3 chunks.
	for i := 0; i < 3; i++ {
		chunk, done, err := bridge.read(ctx, streamID)
		if err != nil {
			t.Fatalf("read %d: %v", i, err)
		}
		if done {
			t.Fatalf("read %d: unexpected done", i)
		}
		if len(chunk.Payload) == 0 {
			t.Fatalf("read %d: empty payload", i)
		}
	}

	// Next read should signal done.
	_, done, err := bridge.read(ctx, streamID)
	if err != nil {
		t.Fatalf("final read error: %v", err)
	}
	if !done {
		t.Fatal("expected done after producer closed")
	}
}
