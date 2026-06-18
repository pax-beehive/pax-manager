package manager

import (
	"context"
	"testing"
)

type fakeRunServer struct {
	runStarted chan struct{}
	shutdown   chan struct{}
}

func (s *fakeRunServer) Run() error {
	close(s.runStarted)
	<-s.shutdown
	return nil
}

func (s *fakeRunServer) Shutdown(context.Context) error {
	close(s.shutdown)
	return nil
}

func TestRunServerShutsDownWhenContextIsCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	srv := &fakeRunServer{
		runStarted: make(chan struct{}),
		shutdown:   make(chan struct{}),
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- runServer(ctx, srv)
	}()

	<-srv.runStarted
	cancel()

	if err := <-errCh; err != nil {
		t.Fatalf("runServer() error = %v", err)
	}
}
