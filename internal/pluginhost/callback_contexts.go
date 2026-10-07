package pluginhost

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

type callbackContextRegistry struct {
	next        atomic.Uint64
	nextCleanup atomic.Uint64
	mu          sync.RWMutex
	contexts    map[string]callbackContextEntry
}

type callbackContextEntry struct {
	ctx      context.Context
	pluginID string
	provider string
	auth     *coreauth.Auth
	instance *hostCallbackInstance
	cleanup  []callbackContextCleanup
}

type callbackContextCleanup struct {
	id uint64
	fn func()
}

func newCallbackContextRegistry() *callbackContextRegistry {
	return &callbackContextRegistry{contexts: make(map[string]callbackContextEntry)}
}

func (r *callbackContextRegistry) open(ctx context.Context, pluginID string, instance *hostCallbackInstance, auth *coreauth.Auth, providers ...string) (string, func()) {
	if r == nil {
		return "", func() {}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	pluginID = strings.TrimSpace(pluginID)
	provider := ""
	if len(providers) > 0 {
		provider = strings.TrimSpace(providers[0])
	}
	ctx = withHostCallbackIdentity(ctx, pluginID, instance)
	id := strconv.FormatUint(r.next.Add(1), 10)
	r.mu.Lock()
	r.contexts[id] = callbackContextEntry{ctx: ctx, pluginID: pluginID, provider: provider, auth: auth, instance: instance}
	r.mu.Unlock()

	var once sync.Once
	return id, func() {
		once.Do(func() {
			var cleanup []callbackContextCleanup
			r.mu.Lock()
			entry := r.contexts[id]
			delete(r.contexts, id)
			r.mu.Unlock()
			cleanup = entry.cleanup
			for _, item := range cleanup {
				if item.fn != nil {
					item.fn()
				}
			}
		})
	}
}

func (r *callbackContextRegistry) providerAndAuth(id string) (string, *coreauth.Auth) {
	if r == nil || id == "" {
		return "", nil
	}
	r.mu.RLock()
	entry := r.contexts[id]
	r.mu.RUnlock()
	return entry.provider, entry.auth
}

func (r *callbackContextRegistry) lookup(id string) (context.Context, string, *hostCallbackInstance, bool) {
	if r == nil {
		return nil, "", nil, false
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, "", nil, false
	}
	r.mu.RLock()
	entry, exists := r.contexts[id]
	r.mu.RUnlock()
	if !exists || entry.ctx == nil {
		return nil, "", nil, false
	}
	return entry.ctx, strings.TrimSpace(entry.pluginID), entry.instance, true
}

func (r *callbackContextRegistry) pluginID(id string) string {
	_, pluginID, _, _ := r.lookup(id)
	return pluginID
}

func (r *callbackContextRegistry) addCleanup(id string, cleanup func()) bool {
	_, ok := r.addCleanupHandle(id, cleanup)
	return ok
}

func (r *callbackContextRegistry) addCleanupHandle(id string, cleanup func()) (func(), bool) {
	if r == nil || cleanup == nil {
		return func() {}, false
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return func() {}, false
	}
	cleanupID := r.nextCleanup.Add(1)
	r.mu.Lock()
	entry, exists := r.contexts[id]
	if exists {
		entry.cleanup = append(entry.cleanup, callbackContextCleanup{id: cleanupID, fn: cleanup})
		r.contexts[id] = entry
	}
	r.mu.Unlock()
	if !exists {
		cleanup()
		return func() {}, false
	}

	var once sync.Once
	remove := func() {
		once.Do(func() {
			r.mu.Lock()
			entry, exists := r.contexts[id]
			if exists {
				for index, item := range entry.cleanup {
					if item.id == cleanupID {
						entry.cleanup = append(entry.cleanup[:index], entry.cleanup[index+1:]...)
						r.contexts[id] = entry
						break
					}
				}
			}
			r.mu.Unlock()
		})
	}
	return remove, true
}

func (r *callbackContextRegistry) resolve(id string, fallback context.Context) context.Context {
	if fallback == nil {
		fallback = context.Background()
	}
	if r == nil || id == "" {
		return fallback
	}
	r.mu.RLock()
	ctx := r.contexts[id].ctx
	r.mu.RUnlock()
	if ctx == nil {
		return fallback
	}
	return ctx
}

func (h *Host) openCallbackContext(ctx context.Context) (string, func()) {
	return h.openCallbackContextForPlugin(ctx, "")
}

func (h *Host) openCallbackContextForPlugin(ctx context.Context, pluginID string) (string, func()) {
	return h.openCallbackContextForPluginInstance(ctx, pluginID, nil)
}

func (h *Host) openCallbackContextForPluginInstance(ctx context.Context, pluginID string, instance *hostCallbackInstance) (string, func()) {
	if h == nil || h.callbackContexts == nil {
		return "", func() {}
	}
	if strings.TrimSpace(pluginID) == "" {
		pluginID = hostCallbackPluginIDFromContext(ctx)
	}
	if instance == nil {
		instance = hostCallbackInstanceFromContext(ctx)
	}
	return h.callbackContexts.open(ctx, pluginID, instance, nil)
}

func (h *Host) openCallbackContextForPluginHTTP(ctx context.Context, pluginID string, auth *coreauth.Auth, provider string) (string, func()) {
	if h == nil || h.callbackContexts == nil {
		return "", func() {}
	}
	return h.callbackContexts.open(ctx, pluginID, nil, auth, provider)
}

func (h *Host) addCallbackCleanup(id string, cleanup func()) bool {
	if h == nil || h.callbackContexts == nil {
		if id != "" && cleanup != nil {
			cleanup()
		}
		return false
	}
	return h.callbackContexts.addCleanup(id, cleanup)
}

func (h *Host) addCallbackCleanupHandle(id string, cleanup func()) (func(), bool) {
	if h == nil || h.callbackContexts == nil {
		if id != "" && cleanup != nil {
			cleanup()
		}
		return func() {}, false
	}
	return h.callbackContexts.addCleanupHandle(id, cleanup)
}

func (h *Host) lookupCallbackContext(id string) (context.Context, string, *hostCallbackInstance, bool) {
	if h == nil || h.callbackContexts == nil {
		return nil, "", nil, false
	}
	return h.callbackContexts.lookup(id)
}

func (h *Host) resolveCallbackContext(id string, fallback context.Context) context.Context {
	if h == nil || h.callbackContexts == nil {
		if fallback == nil {
			return context.Background()
		}
		return fallback
	}
	return h.callbackContexts.resolve(id, fallback)
}

func (h *Host) callbackContextPluginID(id string) string {
	if h == nil || h.callbackContexts == nil {
		return ""
	}
	return h.callbackContexts.pluginID(id)
}

func (h *Host) callbackContextProviderAndAuth(id string) (string, *coreauth.Auth) {
	if h == nil || h.callbackContexts == nil {
		return "", nil
	}
	return h.callbackContexts.providerAndAuth(id)
}
