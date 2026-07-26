package pluginhost

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type streamBridge struct {
	next    atomic.Uint64
	mu      sync.Mutex
	streams map[string]*outputStreamEntry
}

type outputStreamEntry struct {
	ctx       context.Context
	chunks    chan pluginapi.ExecutorStreamChunk
	done      chan struct{}
	closeOnce sync.Once
	mu        sync.Mutex
	cond      *sync.Cond
	active    int
	closed    bool
}

type rpcStreamEmitRequest struct {
	StreamID string                 `json:"stream_id"`
	Payload  []byte                 `json:"payload,omitempty"`
	Error    string                 `json:"error,omitempty"`
	Usage    *pluginapi.UsageDetail `json:"usage,omitempty"`
}

type rpcStreamCloseRequest struct {
	StreamID string `json:"stream_id"`
	Error    string `json:"error,omitempty"`
}

func newStreamBridge() *streamBridge {
	return &streamBridge{streams: make(map[string]*outputStreamEntry)}
}

func (b *streamBridge) open(ctx context.Context) (string, <-chan pluginapi.ExecutorStreamChunk, func()) {
	if b == nil {
		chunks := make(chan pluginapi.ExecutorStreamChunk)
		close(chunks)
		return "", chunks, func() {}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	id := strconv.FormatUint(b.next.Add(1), 10)
	entry := &outputStreamEntry{
		ctx:    ctx,
		chunks: make(chan pluginapi.ExecutorStreamChunk, 16),
		done:   make(chan struct{}),
	}
	entry.cond = sync.NewCond(&entry.mu)
	b.mu.Lock()
	b.streams[id] = entry
	b.mu.Unlock()
	cleanup := func() {
		b.close(id, "")
	}
	if ctx.Done() != nil {
		go func() {
			<-ctx.Done()
			b.close(id, ctx.Err().Error())
		}()
	}
	return id, entry.chunks, cleanup
}

func (b *streamBridge) emit(ctx context.Context, id string, chunk pluginapi.ExecutorStreamChunk) error {
	if b == nil || id == "" {
		return fmt.Errorf("stream id is required")
	}
	b.mu.Lock()
	entry := b.streams[id]
	b.mu.Unlock()
	if entry == nil || !entry.beginEmit() {
		return fmt.Errorf("stream %s is not open", id)
	}
	defer entry.endEmit()
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-entry.ctx.Done():
		return entry.ctx.Err()
	case <-entry.done:
		return fmt.Errorf("stream %s is closed", id)
	case entry.chunks <- chunk:
		return nil
	}
}

func (b *streamBridge) close(id string, errorMessage string) {
	if b == nil || id == "" {
		return
	}
	b.mu.Lock()
	entry := b.streams[id]
	delete(b.streams, id)
	b.mu.Unlock()
	if entry == nil {
		return
	}
	entry.finish(errorMessage)
}

func (e *outputStreamEntry) beginEmit() bool {
	if e == nil {
		return false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return false
	}
	e.active++
	return true
}

func (e *outputStreamEntry) endEmit() {
	if e == nil {
		return
	}
	e.mu.Lock()
	e.active--
	if e.active == 0 {
		e.cond.Broadcast()
	}
	e.mu.Unlock()
}

func (e *outputStreamEntry) finish(errorMessage string) {
	if e == nil {
		return
	}
	e.closeOnce.Do(func() {
		e.mu.Lock()
		e.closed = true
		close(e.done)
		e.mu.Unlock()

		go func() {
			e.mu.Lock()
			for e.active > 0 {
				e.cond.Wait()
			}
			e.mu.Unlock()

			if errorMessage != "" {
				select {
				case e.chunks <- pluginapi.ExecutorStreamChunk{Err: fmt.Errorf("%s", errorMessage)}:
				case <-e.ctx.Done():
				}
			}
			close(e.chunks)
		}()
	})
}
