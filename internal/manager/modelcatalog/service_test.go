package modelcatalog_test

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/modelcatalog"
)

type catalogHTTP func(*http.Request) (*http.Response, error)

func (f catalogHTTP) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func publicCatalogResponse(raw string, status int) *http.Response {
	return &http.Response{StatusCode: status,
		Header: http.Header{"Content-Type": []string{"application/json"}},
		Body:   io.NopCloser(strings.NewReader(raw)),
	}
}

const catalogModelJSON = `{"data":[{"id":"vendor/model","name":"Fixture model","architecture":{"input_modalities":["text"],"output_modalities":["text"]},"pricing":{"prompt":"0","completion":"0"}}]}`

func TestServiceSharesInflightCachesAndProtectsCachedSlices(t *testing.T) {
	var clock atomic.Int64
	clock.Store(time.Date(2026, 11, 17, 3, 0, 0, 0, time.UTC).UnixNano())
	var calls atomic.Int32
	entered, release := make(chan struct{}, 1), make(chan struct{})
	requests := make(chan *http.Request, 4)
	service := modelcatalog.New(modelcatalog.Options{
		Now: func() time.Time { return time.Unix(0, clock.Load()) },
		Transport: catalogHTTP(func(request *http.Request) (*http.Response, error) {
			if calls.Add(1) == 1 {
				entered <- struct{}{}
				<-release
			}
			requests <- request
			return publicCatalogResponse(catalogModelJSON, 200), nil
		}),
	})
	results := make(chan error, 12)
	for range 12 {
		go func() {
			_, err := service.Read(context.Background())
			results <- err
		}()
	}
	<-entered
	close(release)
	for range 12 {
		require.NoError(t, <-results)
	}
	require.EqualValues(t, 1, calls.Load())
	request := <-requests
	require.Equal(t, "https://openrouter.ai/api/v1/models", request.URL.String())
	require.Equal(t, "GET", request.Method)
	require.Equal(t, http.Header{"Accept": []string{"application/json"}}, request.Header)
	first, err := service.Read(t.Context())
	require.NoError(t, err)
	require.Equal(t, "2026-11-17T03:00:00.000Z", first.FetchedAt)
	first.Models[0].ID = "changed-by-caller"
	second, err := service.Read(t.Context())
	require.NoError(t, err)
	require.Equal(t, "vendor/model", second.Models[0].ID)
	clock.Add(int64(5 * time.Minute))
	_, err = service.Read(t.Context())
	require.NoError(t, err)
	require.EqualValues(t, 2, calls.Load())
}

func TestServiceDoesNotCacheFailuresOrServeExpiredCatalog(t *testing.T) {
	var clock atomic.Int64
	clock.Store(time.Now().UnixNano())
	var calls atomic.Int32
	service := modelcatalog.New(modelcatalog.Options{
		Now: func() time.Time { return time.Unix(0, clock.Load()) },
		Transport: catalogHTTP(func(*http.Request) (*http.Response, error) {
			switch calls.Add(1) {
			case 1:
				return publicCatalogResponse("private upstream failure", 503), nil
			case 2:
				return publicCatalogResponse(catalogModelJSON, 200), nil
			case 3:
				return publicCatalogResponse("private rate failure", 429), nil
			default:
				return publicCatalogResponse(`{"data":[]}`, 200), nil
			}
		}),
	})
	_, err := service.Read(t.Context())
	require.ErrorIs(t, err, modelcatalog.ErrUnavailable)
	_, err = service.Read(t.Context())
	require.NoError(t, err)
	clock.Add(int64(5*time.Minute + time.Millisecond))
	_, err = service.Read(t.Context())
	require.ErrorIs(t, err, modelcatalog.ErrUnavailable)
	latest, err := service.Read(t.Context())
	require.NoError(t, err)
	require.Empty(t, latest.Models)
	require.EqualValues(t, 4, calls.Load())
}

func TestServiceRejectsRedirectsInvalidHeadersAndOversizedBodies(t *testing.T) {
	for _, test := range []struct {
		name     string
		response func() *http.Response
	}{
		{"redirect", func() *http.Response {
			r := publicCatalogResponse("", 302)
			r.Header.Set("Location", "https://never-follow.example/catalog")
			return r
		}},
		{"wrong content type", func() *http.Response {
			r := publicCatalogResponse(`{"data":[]}`, 200)
			r.Header.Set("Content-Type", "text/html")
			return r
		}},
		{"invalid length", func() *http.Response {
			r := publicCatalogResponse(`{"data":[]}`, 200)
			r.Header.Set("Content-Length", "-1")
			return r
		}},
		{"declared oversized", func() *http.Response {
			r := publicCatalogResponse(`{"data":[]}`, 200)
			r.Header.Set("Content-Length", "2097153")
			return r
		}},
		{"streamed oversized", func() *http.Response {
			return publicCatalogResponse(strings.Repeat(" ", 2*1024*1024+1), 200)
		}},
		{"invalid json", func() *http.Response { return publicCatalogResponse(`{`, 200) }},
		{"invalid utf8", func() *http.Response { return publicCatalogResponse("\xff", 200) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int32
			service := modelcatalog.New(modelcatalog.Options{Transport: catalogHTTP(
				func(*http.Request) (*http.Response, error) {
					calls.Add(1)
					return test.response(), nil
				},
			)})
			_, err := service.Read(t.Context())
			require.ErrorIs(t, err, modelcatalog.ErrUnavailable)
			require.EqualValues(t, 1, calls.Load())
		})
	}
}

type blockedCatalogBody struct {
	closed chan struct{}
	once   sync.Once
}

func (b *blockedCatalogBody) Read([]byte) (int, error) { <-b.closed; return 0, io.EOF }

func (b *blockedCatalogBody) Close() error { b.once.Do(func() { close(b.closed) }); return nil }

func TestServiceDeadlineBoundsHeadersAndBodyAndAllowsImmediateRetry(t *testing.T) {
	var calls atomic.Int32
	body := &blockedCatalogBody{closed: make(chan struct{})}
	service := modelcatalog.New(modelcatalog.Options{
		Timeout: 250 * time.Millisecond,
		Transport: catalogHTTP(func(request *http.Request) (*http.Response, error) {
			switch calls.Add(1) {
			case 1:
				<-request.Context().Done()
				return nil, request.Context().Err()
			case 2:
				response := publicCatalogResponse("", 200)
				response.Body = body
				return response, nil
			default:
				return publicCatalogResponse(`{"data":[]}`, 200), nil
			}
		}),
	})
	for range 2 {
		_, err := service.Read(t.Context())
		require.ErrorIs(t, err, modelcatalog.ErrUnavailable)
	}
	select {
	case <-body.closed:
	case <-time.After(time.Second):
		t.Fatal("timed-out catalog body was not closed")
	}
	_, err := service.Read(t.Context())
	require.NoError(t, err)
	require.EqualValues(t, 3, calls.Load())
}
