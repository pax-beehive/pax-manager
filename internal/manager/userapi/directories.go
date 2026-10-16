package userapi

import (
	"context"
	"net/http"
	"strings"

	"github.com/pax-beehive/pax-manager/internal/manager/apperr"
	"github.com/pax-beehive/pax-manager/internal/manager/auth"
)

// The daemon, not the manager host, resolves and confines paths to its home.
func (s *Service) ListNodeDaemonDirectories(ctx context.Context, meta auth.RequestMetadata,
	nodeID, path, after string, showHidden bool,
) (int, any, error) {
	if len(path) > 8192 || len(after) > 1024 || strings.ContainsAny(path, "\x00\r\n") ||
		strings.ContainsAny(after, "/\\\x00\r\n") {
		return 0, nil, apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "invalid directory path or cursor",
		}
	}
	return s.queryNodeDaemon(ctx, meta, nodeID, map[string]any{
		"type":             "directories.list",
		"list_directories": map[string]any{"path": path, "after": after, "show_hidden": showHidden},
	})
}
