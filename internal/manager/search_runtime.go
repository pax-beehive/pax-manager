package manager

import (
	"context"
	"log/slog"
	"sync"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/pax-manager/internal/manager/logging"
	"github.com/pax-beehive/pax-manager/internal/manager/runtimeconfig"
	"github.com/pax-beehive/pax-manager/internal/manager/search"
)

// runtimeSearchBackend resolves the search settings on every call and reuses
// one client per configuration fingerprint. A new target is initialized
// (index ensured) before the first document is written to it.
type runtimeSearchBackend struct {
	config func() runtimeconfig.SearchConfig

	mu          sync.Mutex
	fingerprint string
	client      *search.Client
	clientErr   error
	ensured     bool
}

func newRuntimeSearchBackend(s *Service) *runtimeSearchBackend {
	return &runtimeSearchBackend{config: func() runtimeconfig.SearchConfig {
		return s.runtimeConfig.Search()
	}}
}

// current returns the client for the effective configuration, or
// domain.ErrSearchUnavailable when search is not configured.
func (b *runtimeSearchBackend) current() (*search.Client, bool, error) {
	cfg := b.config()
	if cfg.URL == "" {
		return nil, false, domain.ErrSearchUnavailable
	}
	key := cfg.Fingerprint()
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.fingerprint != key {
		b.fingerprint, b.ensured = key, false
		b.client, b.clientErr = search.NewClient(search.Config{
			URL: cfg.URL, Index: cfg.Index, APIKey: cfg.APIKey,
		})
		if b.clientErr != nil {
			logging.Error(context.Background(), "workspace search configuration failed",
				slog.String("error", b.clientErr.Error()))
		}
	}
	if b.clientErr != nil {
		return nil, false, domain.ErrSearchUnavailable
	}
	return b.client, b.ensured, nil
}

func (b *runtimeSearchBackend) markEnsured(client *search.Client) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.client == client {
		b.ensured = true
	}
}

func (b *runtimeSearchBackend) EnsureIndex(ctx context.Context) error {
	client, _, err := b.current()
	if err != nil {
		return err
	}
	if err = client.EnsureIndex(ctx); err != nil {
		return err
	}
	b.markEnsured(client)
	return nil
}

func (b *runtimeSearchBackend) Index(ctx context.Context, doc domain.SearchDocument) error {
	client, ensured, err := b.current()
	if err != nil {
		return err
	}
	if !ensured {
		if err = client.EnsureIndex(ctx); err != nil {
			return err
		}
		b.markEnsured(client)
	}
	return client.Index(ctx, doc)
}

func (b *runtimeSearchBackend) Search(
	ctx context.Context,
	query domain.SearchQuery,
) (domain.SearchPage, error) {
	client, _, err := b.current()
	if err != nil {
		return domain.SearchPage{}, err
	}
	return client.Search(ctx, query)
}

func (b *runtimeSearchBackend) SearchGroups(
	ctx context.Context,
	query domain.SearchQuery,
) (domain.SearchGroupPage, error) {
	client, _, err := b.current()
	if err != nil {
		return domain.SearchGroupPage{}, err
	}
	return client.SearchGroups(ctx, query)
}
