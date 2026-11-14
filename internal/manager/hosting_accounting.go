package manager

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"regexp"

	"github.com/cloudwego/hertz/pkg/app"

	"github.com/pax-beehive/pax-manager/internal/manager/auth"
	"github.com/pax-beehive/pax-manager/internal/manager/hosting"
)

const hostingAccountingMaxBody = 64 * 1024

type hostingAccountingUsage struct {
	InputTokens       int64           `json:"input_tokens"`
	OutputTokens      int64           `json:"output_tokens"`
	RawUsage          json.RawMessage `json:"raw_usage"`
	UpstreamRequestID string          `json:"upstream_request_id"`
	ErrorCode         string          `json:"error_code,omitempty"`
}

type hostingAccountingRequest struct {
	Action          string                      `json:"action"`
	Token           string                      `json:"token"`
	RequestID       string                      `json:"request_id"`
	ReservationID   string                      `json:"reservation_id"`
	Tokens          int64                       `json:"tokens"`
	Model           string                      `json:"model"`
	MaxOutputTokens int64                       `json:"max_output_tokens"`
	Moderation      *hosting.ModerationDecision `json:"moderation"`
	Usage           *hostingAccountingUsage     `json:"usage"`
	Known           bool                        `json:"known"`
	Policy          *hosting.ModelPolicy        `json:"policy"`
}

// GatewayAccounting accepts only the independent gateway's service credential.
// Tenant/host credentials cannot submit their own verdicts or settlements.
func GatewayAccounting(c context.Context, ctx *app.RequestContext) {
	s := serviceFromContext(ctx)
	if !s.hosting.GatewayAuthorized(auth.BearerToken(string(ctx.GetHeader("Authorization")))) {
		writeError(ctx, http.StatusUnauthorized, "unauthorized")
		return
	}
	ctx.Header("Cache-Control", "no-store")
	if !s.hosting.Enabled() {
		hostingError(ctx, hosting.ErrDisabled)
		return
	}
	if len(ctx.Request.Body()) > hostingAccountingMaxBody {
		writeError(ctx, http.StatusRequestEntityTooLarge, "gateway_accounting_request_too_large")
		return
	}
	var request hostingAccountingRequest
	decoder := json.NewDecoder(bytes.NewReader(ctx.Request.Body()))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeError(ctx, http.StatusBadRequest, "invalid_gateway_accounting_request")
		return
	}
	if decoder.Decode(new(any)) != io.EOF || len(request.Token) > 4096 ||
		len(request.ReservationID) > 256 || len(request.RequestID) > 128 {
		writeError(ctx, http.StatusBadRequest, "invalid_gateway_accounting_request")
		return
	}
	data, err := s.applyGatewayAccounting(c, &request)
	if err != nil {
		hostingError(ctx, err)
		return
	}
	writeData(ctx, http.StatusOK, data)
}

func (s *Service) applyGatewayAccounting(
	c context.Context,
	request *hostingAccountingRequest,
) (any, error) {
	switch request.Action {
	case "authorize":
		return map[string]bool{"ok": true}, s.hosting.Authorize(c, request.Token)
	case "models":
		models, err := s.hosting.RuntimePolicies(c, request.Token)
		return map[string]any{"models": models}, err
	case "reserve":
		reservation, err := s.hosting.ReserveForPolicy(
			c,
			request.Token,
			request.RequestID,
			request.Tokens,
			request.Model,
			request.MaxOutputTokens,
			request.Policy,
		)
		if err != nil {
			return nil, err
		}
		return map[string]string{"reservation_id": reservation.ID}, nil
	case "moderation":
		if !validHostingModerationMetadata(request.Moderation) {
			return nil, hosting.ErrConflict
		}
		return map[string]bool{
				"ok": true,
			}, s.hosting.RecordModeration(
				c,
				request.ReservationID,
				request.Moderation,
			)
	case "begin":
		return map[string]bool{"ok": true}, s.hosting.BeginGeneration(c, request.ReservationID)
	case "cancel":
		return map[string]bool{
				"ok": true,
			}, s.hosting.CancelBeforeGeneration(
				c,
				request.ReservationID,
			)
	case "settle":
		return map[string]bool{"ok": true}, s.settleGatewayRequest(c, request)
	default:
		return nil, hosting.ErrConflict
	}
}

var hostingModerationMetadataModel = regexp.MustCompile(`^omni-moderation-[a-z0-9-]{1,64}$`)
var hostingModerationMetadataID = regexp.MustCompile(`^modr[-_A-Za-z0-9]{1,123}$`)

func validHostingModerationMetadata(decision *hosting.ModerationDecision) bool {
	if decision != nil && decision.Verdict == "skipped_by_policy" {
		return decision.Model == "" && decision.InputCount == 0 && len(decision.Categories) == 0 &&
			decision.ProviderRequestID == "" &&
			decision.PolicyVersion != ""
	}
	if decision == nil || !hostingModerationMetadataModel.MatchString(decision.Model) ||
		decision.InputCount < 0 || decision.InputCount > 64 || len(decision.Categories) > 14 {
		return false
	}
	if decision.ProviderRequestID != "" &&
		!hostingModerationMetadataID.MatchString(decision.ProviderRequestID) {
		return false
	}
	for _, category := range decision.Categories {
		if !validHostingModerationCategory(category) {
			return false
		}
	}
	return true
}

func validHostingModerationCategory(category string) bool {
	switch category {
	case "harassment",
		"harassment/threatening",
		"hate",
		"hate/threatening",
		"illicit",
		"illicit/violent",
		"self-harm",
		"self-harm/intent",
		"self-harm/instructions",
		"sexual",
		"sexual/minors",
		"violence",
		"violence/graphic",
		"other":
		return true
	default:
		return false
	}
}

func (s *Service) settleGatewayRequest(c context.Context, request *hostingAccountingRequest) error {
	usage := request.Usage
	if usage == nil || usage.InputTokens < 0 || usage.OutputTokens < 0 ||
		usage.InputTokens > 1_000_000 || usage.OutputTokens > 1_000_000 ||
		len(usage.UpstreamRequestID) > 256 || len(usage.RawUsage) > 4096 ||
		!hosting.ValidSettlementErrorCode(usage.ErrorCode) {
		return hosting.ErrConflict
	}
	raw, err := boundedHostingUsage(usage.RawUsage)
	if err != nil {
		return hosting.ErrConflict
	}
	return s.hosting.SettleWithError(
		c,
		request.ReservationID,
		usage.InputTokens,
		usage.OutputTokens,
		raw,
		usage.UpstreamRequestID,
		request.Known,
		usage.ErrorCode,
	)
}

// Preserve numeric usage evidence without accepting arbitrary prompt/output
// fields in the trusted service's raw-usage object.
func boundedHostingUsage(raw json.RawMessage) ([]byte, error) {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil, nil
	}
	var source map[string]json.RawMessage
	if err := json.Unmarshal(raw, &source); err != nil {
		return nil, err
	}
	result := map[string]json.RawMessage{}
	for _, key := range []string{"prompt_tokens", "completion_tokens", "total_tokens", "input_tokens", "output_tokens", "cost"} {
		value, exists := source[key]
		if !exists {
			continue
		}
		var number json.Number
		if err := json.Unmarshal(value, &number); err != nil {
			return nil, err
		}
		if _, err := number.Float64(); err != nil {
			return nil, err
		}
		result[key] = value
	}
	return json.Marshal(result)
}
