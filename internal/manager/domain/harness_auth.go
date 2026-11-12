package domain

type HarnessAuthLogin struct {
	Harness   string `json:"harness"`
	Operation string `json:"operation"`
	Method    string `json:"method,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	Code      string `json:"code,omitempty"`
}

type LoginNodeHarnessRequest struct {
	UserID    string `json:"user_id"`
	NodeID    string `json:"node_id"`
	CommandID string `json:"command_id"`
	Harness   string `json:"harness"`
	Operation string `json:"operation"`
	Method    string `json:"method,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	Code      string `json:"code,omitempty"`
}

type HarnessAuthView struct {
	Harness          string              `json:"harness"`
	SessionID        string              `json:"session_id,omitempty"`
	State            string              `json:"state"`
	AuthorizationURL string              `json:"authorization_url,omitempty"`
	Method           string              `json:"method,omitempty"`
	UserCode         string              `json:"user_code,omitempty"`
	ExpiresAt        string              `json:"expires_at,omitempty"`
	LoggedIn         *bool               `json:"logged_in,omitempty"`
	AuthMethod       string              `json:"auth_method,omitempty"`
	ErrorCode        string              `json:"error_code,omitempty"`
	Account          *HarnessAuthAccount `json:"account,omitempty"`
}

type HarnessAuthAccount struct {
	Email        string `json:"email,omitempty"`
	DisplayName  string `json:"display_name,omitempty"`
	Organization string `json:"organization,omitempty"`
	Plan         string `json:"plan,omitempty"`
}

type HarnessAuthData struct {
	HarnessAuth HarnessAuthView `json:"harness_auth"`
}
