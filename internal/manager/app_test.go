package manager

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGivenDatabasePoolThenConcurrentConnectionsAreBounded(t *testing.T) {
	db, err := sql.Open("pgx", "postgres://unused:unused@localhost/unused?sslmode=disable")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	configurePostgresPool(db)
	assert.Equal(t, 16, db.Stats().MaxOpenConnections)
}

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
