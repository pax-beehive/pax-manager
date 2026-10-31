package userapi

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

type searchReadReceipt struct {
	Owner  string    `json:"owner"`
	Ref    SearchRef `json:"ref"`
	View   string    `json:"view"`
	Offset int       `json:"offset"`
	Limit  int       `json:"limit"`
}

func (s *WorkspaceSearchService) renderRead(
	owner string,
	document domain.SearchDocument,
	request WorkspaceReadRequest,
) (WorkspaceMessageRead, error) {
	view := request.View
	if view == "" {
		view = "full"
		if document.Role == "assistant" {
			view = "last"
		}
	}
	text, err := searchMessageView(document.Body, view)
	if err != nil {
		return WorkspaceMessageRead{}, err
	}
	limit := request.Limit
	if limit == 0 {
		limit = 16 * 1024
	}
	runes := []rune(text)
	start := min(request.Offset, len(runes))
	end := min(start+limit, len(runes))
	ref := documentRef(document)
	read := WorkspaceMessageRead{
		Ref:            ref,
		MessageID:      document.SourceID,
		AgentID:        document.AgentID,
		SessionID:      document.SessionID,
		Role:           document.Role,
		SessionSeq:     document.SessionSeq,
		ConversationID: document.ConversationID,
		CreatedAt:      document.CreatedAt,
		Text:           string(runes[start:end]),
		View:           view,
		AvailableViews: []string{"first", "last", "full"},
		HasMore:        end < len(runes),
	}
	if read.HasMore {
		read.NextOffset = end
	}
	if len(s.receiptKey) < 32 {
		return WorkspaceMessageRead{}, searchHTTPError(
			http.StatusServiceUnavailable,
			"read receipts are unavailable",
		)
	}
	read.ReadReceipt = s.signReceipt(
		searchReadReceipt{Owner: owner, Ref: ref, View: view, Offset: start, Limit: limit},
	)
	return read, nil
}

func searchMessageView(body, view string) (string, error) {
	switch view {
	case "full":
		return body, nil
	case "first":
		text := strings.TrimLeft(body, "\r\n \t")
		if index := strings.Index(text, "\n\n"); index >= 0 {
			text = text[:index]
		}
		return text, nil
	case "last":
		text := strings.TrimRight(body, "\r\n \t")
		if index := strings.LastIndex(text, "\n\n"); index >= 0 {
			text = text[index+2:]
		}
		return text, nil
	default:
		return "", searchHTTPError(http.StatusBadRequest, "invalid message view")
	}
}

func (s *WorkspaceSearchService) signReceipt(receipt searchReadReceipt) string {
	data, _ := json.Marshal(receipt)
	mac := hmac.New(sha256.New, s.receiptKey)
	_, _ = mac.Write(data)
	return base64.RawURLEncoding.EncodeToString(
		data,
	) + "." + base64.RawURLEncoding.EncodeToString(
		mac.Sum(nil),
	)
}

func (s *WorkspaceSearchService) verifyReceipt(
	ctx context.Context,
	owner string,
	ref domain.MessageVersionRef,
	token string,
) (WorkspaceMessageRead, error) {
	invalid := searchHTTPError(
		http.StatusBadRequest,
		"invalid read receipt; read the exact source version again",
	)
	parts := strings.Split(token, ".")
	if len(parts) != 2 || len(token) > 4096 || len(s.receiptKey) < 32 {
		return WorkspaceMessageRead{}, invalid
	}
	data, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return WorkspaceMessageRead{}, invalid
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return WorkspaceMessageRead{}, invalid
	}
	mac := hmac.New(sha256.New, s.receiptKey)
	_, _ = mac.Write(data)
	var receipt searchReadReceipt
	if !hmac.Equal(signature, mac.Sum(nil)) || json.Unmarshal(data, &receipt) != nil {
		return WorkspaceMessageRead{}, invalid
	}
	if receipt.Owner != owner || receipt.Ref.Kind != "message" || receipt.Ref.ID != ref.MessageID ||
		receipt.Ref.ContentHash != ref.ContentHash || receipt.Ref.CanonicalizationVersion != ref.CanonicalizationVersion ||
		receipt.Offset < 0 || receipt.Limit < 1 || receipt.Limit > 16*1024 {
		return WorkspaceMessageRead{}, invalid
	}
	document, err := s.readMessageVersion(ctx, owner, receipt.Ref)
	if err != nil {
		return WorkspaceMessageRead{}, searchSourceError(err)
	}
	return s.renderRead(
		owner,
		document,
		WorkspaceReadRequest{
			Ref:    receipt.Ref,
			View:   receipt.View,
			Offset: receipt.Offset,
			Limit:  receipt.Limit,
		},
	)
}
