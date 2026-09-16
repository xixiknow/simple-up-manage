package main

import (
	"net/http"
	"sync"
)

// drainingHandler tracks handler cleanup, which http.Server.Close does not wait
// for. StopAccepting must precede Wait so no new work is added during the wait.
type drainingHandler struct {
	next    http.Handler
	mu      sync.Mutex
	closing bool
	active  sync.WaitGroup
}

func (h *drainingHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	if h.closing {
		h.mu.Unlock()
		http.Error(w, "server is shutting down", http.StatusServiceUnavailable)
		return
	}
	h.active.Add(1)
	h.mu.Unlock()
	defer h.active.Done()
	h.next.ServeHTTP(w, r)
}

func (h *drainingHandler) StopAccepting() {
	h.mu.Lock()
	h.closing = true
	h.mu.Unlock()
}

func (h *drainingHandler) Wait() { h.active.Wait() }
