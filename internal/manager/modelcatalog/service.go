package modelcatalog

import (
	"context"
	"io"
	"mime"
	"net/http"
	"strconv"
	"sync"
	"time"
)

const catalogURL = "https://openrouter.ai/api/v1/models"
const maxCatalogBytes = 2 * 1024 * 1024

type Catalog struct {
	Source    string  `json:"source"`
	FetchedAt string  `json:"fetched_at"`
	Models    []Model `json:"models"`
}

type Options struct {
	Transport http.RoundTripper
	Now       func() time.Time
	Timeout   time.Duration
	CacheTTL  time.Duration
}

type flight struct {
	done    chan struct{}
	catalog Catalog
	err     error
}

type Service struct {
	mu      sync.Mutex
	client  *http.Client
	now     func() time.Time
	timeout time.Duration
	ttl     time.Duration
	cached  Catalog
	expires time.Time
	loading *flight
}

func New(options Options) *Service {
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.Timeout <= 0 {
		options.Timeout = 10 * time.Second
	}
	if options.CacheTTL <= 0 {
		options.CacheTTL = 5 * time.Minute
	}
	return &Service{
		client: &http.Client{
			Transport: options.Transport, Timeout: options.Timeout,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		now: options.Now, timeout: options.Timeout, ttl: options.CacheTTL,
	}
}

func (s *Service) Read(ctx context.Context) (Catalog, error) {
	if s == nil {
		return Catalog{}, ErrUnavailable
	}
	if ctx.Err() != nil {
		return Catalog{}, ErrUnavailable
	}
	s.mu.Lock()
	if s.now().Before(s.expires) {
		cached := cloneCatalog(s.cached)
		s.mu.Unlock()
		return cached, nil
	}
	if s.loading == nil {
		s.loading = &flight{done: make(chan struct{})}
		go s.load(s.loading)
	}
	loading := s.loading
	s.mu.Unlock()
	select {
	case <-ctx.Done():
		return Catalog{}, ErrUnavailable
	case <-loading.done:
		return cloneCatalog(loading.catalog), loading.err
	}
}

func (s *Service) load(loading *flight) {
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()
	result := make(chan flight, 1)
	go func() {
		catalog, err := s.fetch(ctx)
		result <- flight{catalog: catalog, err: err}
	}()
	var catalog Catalog
	var err error
	select {
	case outcome := <-result:
		catalog, err = outcome.catalog, outcome.err
	case <-ctx.Done():
		err = ErrUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err == nil {
		s.cached, s.expires = catalog, s.now().Add(s.ttl)
	}
	loading.catalog, loading.err = catalog, err
	s.loading = nil
	close(loading.done)
}

func (s *Service) fetch(ctx context.Context) (Catalog, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, catalogURL, nil)
	if err != nil {
		return Catalog{}, ErrUnavailable
	}
	request.Header.Set("Accept", "application/json")
	response, err := s.client.Do(request)
	if err != nil {
		return Catalog{}, ErrUnavailable
	}
	var closeOnce sync.Once
	closeBody := func() { closeOnce.Do(func() { _ = response.Body.Close() }) }
	stopClose := context.AfterFunc(ctx, closeBody)
	defer func() { stopClose(); closeBody() }()
	mediaType, _, contentErr := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if response.StatusCode != http.StatusOK || contentErr != nil ||
		mediaType != "application/json" ||
		!boundedLength(response) {
		return Catalog{}, ErrUnavailable
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxCatalogBytes+1))
	if err != nil || len(raw) > maxCatalogBytes {
		return Catalog{}, ErrUnavailable
	}
	models, err := Normalize(raw)
	if err != nil {
		return Catalog{}, ErrUnavailable
	}
	return Catalog{
		Source:    "openrouter",
		FetchedAt: s.now().UTC().Format("2006-01-02T15:04:05.000Z"),
		Models:    models,
	}, nil
}

func boundedLength(response *http.Response) bool {
	if response.ContentLength > maxCatalogBytes {
		return false
	}
	if raw := response.Header.Get("Content-Length"); raw != "" {
		for _, digit := range raw {
			if digit < '0' || digit > '9' {
				return false
			}
		}
		length, err := strconv.ParseUint(raw, 10, 64)
		return err == nil && length <= maxCatalogBytes
	}
	return true
}

func cloneCatalog(catalog Catalog) Catalog {
	catalog.Models = append([]Model{}, catalog.Models...)
	return catalog
}
