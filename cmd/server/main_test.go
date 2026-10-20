package main

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestShutdownWaitsForActiveRequest(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{})
	release := make(chan struct{})
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- serve(ctx, listener, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			close(started)
			<-release
			w.WriteHeader(http.StatusNoContent)
		}))
	}()
	responseDone := make(chan error, 1)
	go func() {
		response, err := http.Get("http://" + listener.Addr().String())
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode != http.StatusNoContent {
				err = &unexpectedStatus{response.StatusCode}
			}
		}
		responseDone <- err
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("request did not start")
	}
	cancel()
	select {
	case err := <-serverDone:
		t.Fatalf("server returned before active request finished: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	select {
	case err := <-responseDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("active request did not finish")
	}
	select {
	case err := <-serverDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not finish shutdown")
	}
}

type unexpectedStatus struct{ code int }

func (e *unexpectedStatus) Error() string { return "unexpected response status" }
