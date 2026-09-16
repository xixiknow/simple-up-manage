package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestForcedCloseWaitsForHandlerCleanup(t *testing.T) {
	entered := make(chan struct{})
	cleaning := make(chan struct{})
	release := make(chan struct{})
	h := &drainingHandler{next: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-r.Context().Done()
		close(cleaning)
		<-release // Simulates deferred attempt persistence after socket closure.
	})}
	server := httptest.NewServer(h)
	t.Cleanup(server.Close)
	t.Cleanup(func() { close(release) })
	clientDone := make(chan struct{})
	go func() {
		defer close(clientDone)
		res, err := server.Client().Get(server.URL)
		if err == nil {
			_ = res.Body.Close()
		}
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("request did not start")
	}
	h.StopAccepting()
	rejected := httptest.NewRecorder()
	h.ServeHTTP(rejected, httptest.NewRequest(http.MethodGet, "/", nil))
	if rejected.Code != http.StatusServiceUnavailable {
		t.Fatalf("new request status=%d", rejected.Code)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	if err := server.Config.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("shutdown error=%v", err)
	}
	if err := server.Config.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-cleaning:
	case <-time.After(5 * time.Second):
		t.Fatal("handler did not begin cleanup")
	}
	waited := make(chan struct{})
	go func() { h.Wait(); close(waited) }()
	select {
	case <-waited:
		t.Fatal("wait finished before handler cleanup")
	case <-time.After(20 * time.Millisecond):
	}
	release <- struct{}{}
	select {
	case <-waited:
	case <-time.After(5 * time.Second):
		t.Fatal("wait did not finish after cleanup")
	}
	select {
	case <-clientDone:
	case <-time.After(5 * time.Second):
		t.Fatal("client still connected")
	}
}
