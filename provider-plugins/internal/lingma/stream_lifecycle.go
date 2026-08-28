package lingma

import "fmt"

type activePluginStream struct {
	host       hostRPC
	upstreamID string
}

func (p *Plugin) beginStream(outputID string, host hostRPC, upstreamID string) bool {
	if p == nil {
		return false
	}
	p.streamMu.Lock()
	defer p.streamMu.Unlock()
	if p.shuttingDown {
		return false
	}
	if p.activeStreams == nil {
		p.activeStreams = make(map[string]activePluginStream)
	}
	p.streamWG.Add(1)
	p.activeStreams[outputID] = activePluginStream{host: host, upstreamID: upstreamID}
	return true
}

func (p *Plugin) updateStreamUpstream(outputID, upstreamID string) bool {
	if p == nil {
		return false
	}
	p.streamMu.Lock()
	defer p.streamMu.Unlock()
	if p.shuttingDown {
		return false
	}
	stream, ok := p.activeStreams[outputID]
	if ok {
		stream.upstreamID = upstreamID
		p.activeStreams[outputID] = stream
	}
	return ok
}

func (p *Plugin) isShuttingDown() bool {
	if p == nil {
		return true
	}
	p.streamMu.Lock()
	defer p.streamMu.Unlock()
	return p.shuttingDown
}

func (p *Plugin) endStream(outputID string) {
	if p == nil {
		return
	}
	p.streamMu.Lock()
	delete(p.activeStreams, outputID)
	p.streamMu.Unlock()
	p.streamWG.Done()
}

// Shutdown cancels active streams and waits until plugin goroutines stop using
// the host callback table before the dynamic library is unloaded.
func (p *Plugin) Shutdown() {
	if p == nil {
		return
	}
	p.shutdownOnce.Do(func() {
		p.streamMu.Lock()
		p.shuttingDown = true
		active := make(map[string]activePluginStream, len(p.activeStreams))
		for outputID, stream := range p.activeStreams {
			active[outputID] = stream
		}
		p.streamMu.Unlock()

		for outputID, stream := range active {
			stream.host.closeHTTPStream(stream.upstreamID)
			stream.host.closeOutputStream(outputID, fmt.Errorf("Lingma plugin is shutting down"))
		}
		p.streamWG.Wait()
	})
}
