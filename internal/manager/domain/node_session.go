package domain

type SessionOrigin struct {
	Kind           string `json:"kind"`
	RegistrationID string `json:"registration_id"`
	CronjobID      string `json:"cronjob_id"`
	RunID          string `json:"run_id"`
	Name           string `json:"name"`
}

type RegisterNodeSessionRequest struct {
	NativeID string        `json:"native_id"`
	CWD      string        `json:"cwd"`
	Origin   SessionOrigin `json:"origin"`
}
