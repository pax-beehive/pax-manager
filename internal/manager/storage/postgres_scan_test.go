package storage

import (
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

type fakeRow []any

func (r fakeRow) Scan(dest ...any) error {
	for i := range dest {
		switch d := dest[i].(type) {
		case *int:
			*d = r[i].(int)
		case *int64:
			*d = r[i].(int64)
		case *float64:
			*d = r[i].(float64)
		case *string:
			*d = r[i].(string)
		case **time.Time:
			if v, ok := r[i].(*time.Time); ok {
				*d = v
			}
		case *time.Time:
			*d = r[i].(time.Time)
		case *[]byte:
			*d = r[i].([]byte)
		case *json.RawMessage:
			*d = append(json.RawMessage(nil), r[i].([]byte)...)
		}
	}
	return nil
}

func TestScanAgentUsesComputedStatusForLifecycleAndOnline(t *testing.T) {
	now := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	agent, err := scanAgent(fakeRow{
		"agent_1",
		"node_1",
		"user_1",
		"codex",
		"editor agent",
		[]byte(`{"skills":["code"]}`),
		"workstation",
		"codex",
		"server",
		"linux",
		"0.1.0",
		"http://localhost:8642",
		"pending",
		"online",
		&now,
		now,
		[]byte(`{"label":"primary"}`),
		[]byte(`{"ok":true}`),
	})
	if err != nil {
		t.Fatalf("scan agent: %v", err)
	}
	if agent.Status != "online" {
		t.Fatalf("status = %q, want online", agent.Status)
	}
	if !agent.Online {
		t.Fatal("online = false, want true")
	}
}

func TestScanAgentKeepsExplicitOfflineWithFreshHeartbeat(t *testing.T) {
	now := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	agent, err := scanAgent(fakeRow{
		"agent_1",
		"node_1",
		"user_1",
		"codex",
		"editor agent",
		[]byte(`{"skills":["code"]}`),
		"workstation",
		"codex",
		"server",
		"linux",
		"0.1.0",
		"http://localhost:8642",
		"offline",
		"online",
		&now,
		now,
		[]byte(`{"label":"primary"}`),
		[]byte(`{"ok":true}`),
	})
	if err != nil {
		t.Fatalf("scan agent: %v", err)
	}
	if agent.Status != "offline" {
		t.Fatalf("status = %q, want offline", agent.Status)
	}
	if agent.Online {
		t.Fatal("online = true, want false")
	}
}

func TestEffectiveAgentStatusDecisionTable(t *testing.T) {
	cases := []struct {
		name      string
		stored    string
		heartbeat string
		want      string
	}{
		{"empty stored uses heartbeat", "", "online", "online"},
		{"empty stored without heartbeat is offline", "", "", "offline"},
		{"pending uses heartbeat", "pending", "offline", "offline"},
		{"online uses heartbeat", "online", "offline", "offline"},
		{"explicit offline wins over heartbeat", "offline", "online", "offline"},
		{"explicit failed wins over heartbeat", "failed", "online", "failed"},
		{"unknown lifecycle is preserved", "maintenance", "online", "maintenance"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := effectiveAgentStatus(tc.stored, tc.heartbeat)
			if got != tc.want {
				t.Fatalf("effective status = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRuntimeStateFromMetadata(t *testing.T) {
	now := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	state := runtimeStateFromMetadata([]byte(`{
		"runtime_state": {
			"agent_id": "agent_1",
			"session_id": "sess_1",
			"lifecycle": "waiting_approval",
			"active_tool_calls": [{"tool_call_id": "tool_1", "title": "Review diff"}],
			"updated_at": "` + now.Format(time.RFC3339Nano) + `"
		}
	}`))
	if state == nil {
		t.Fatal("runtime state = nil")
	}
	if state.AgentID != "agent_1" ||
		state.SessionID != "sess_1" ||
		state.Lifecycle != domain.RuntimeLifecycleWaitingApproval ||
		len(state.ActiveToolCalls) != 1 {
		t.Fatalf("runtime state = %+v", state)
	}
	if runtimeStateFromMetadata([]byte(`not-json`)) != nil {
		t.Fatal("invalid metadata returned runtime state")
	}
	if runtimeStateFromMetadata([]byte(`{"other":true}`)) != nil {
		t.Fatal("metadata without runtime_state returned state")
	}
}

func TestScanSessionHydratesRuntimeMetadataAndTokenAliases(t *testing.T) {
	now := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	lastMessageAt := now.Add(time.Minute)
	session, err := scanSession(fakeRow{
		int64(7),
		"node_1",
		"agent_1",
		"sess_manager",
		"Ship tests",
		"codex",
		"native_1",
		"project_1",
		"preview",
		[]byte(`["/workspace","/tmp/project"]`),
		"runtime",
		"running",
		"test task",
		&lastMessageAt,
		3,
		int64(10),
		int64(20),
		int64(30),
		int64(4),
		int64(5),
		int64(6),
		int64(7),
		0.12,
		0.34,
		0.46,
		"gpt-5",
		"run_1",
		"in_progress",
		now,
		now.Add(2 * time.Minute),
		[]byte(
			`{"runtime_state":{"agent_id":"agent_1","session_id":"sess_manager","lifecycle":"running"}}`,
		),
	})
	if err != nil {
		t.Fatalf("scan session: %v", err)
	}
	if session.TokenInput != 10 || session.TokenOutput != 20 || session.TokenTotal != 30 {
		t.Fatalf(
			"token aliases = %d/%d/%d",
			session.TokenInput,
			session.TokenOutput,
			session.TokenTotal,
		)
	}
	if !reflect.DeepEqual(session.WorkspaceRoots, []string{"/workspace", "/tmp/project"}) {
		t.Fatalf("workspace roots = %#v", session.WorkspaceRoots)
	}
	if session.RuntimeState == nil ||
		session.RuntimeState.Lifecycle != domain.RuntimeLifecycleRunning {
		t.Fatalf("runtime state = %+v", session.RuntimeState)
	}
}

func TestScanMailboxDecodesPayloads(t *testing.T) {
	now := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	deliveredAt := now.Add(time.Minute)
	completedAt := now.Add(2 * time.Minute)
	expiresAt := now.Add(time.Hour)
	msg, err := scanMailbox(fakeRow{
		int64(9),
		"msg_1",
		"usr_actor",
		"usr_owner",
		"node_1",
		"agent_1",
		"sess_1",
		"hello",
		"text",
		[]byte(`{"content":"hello"}`),
		"completed",
		&deliveredAt,
		&completedAt,
		"ok",
		"",
		now,
		&expiresAt,
		"node_to_user",
		"parent_1",
		"turn_1",
		"resp_1",
		[]byte(`[{"event":"done"}]`),
		[]byte(`[{"path":"main.go","tool":"edit"}]`),
		[]byte(`{"input_tokens":11,"output_tokens":12,"total_tokens":23}`),
	})
	if err != nil {
		t.Fatalf("scan mailbox: %v", err)
	}
	if string(msg.Payload) != `{"content":"hello"}` || string(msg.Events) != `[{"event":"done"}]` {
		t.Fatalf("payloads = %s %s", msg.Payload, msg.Events)
	}
	if len(msg.FileChanges) != 1 || msg.FileChanges[0].Path != "main.go" {
		t.Fatalf("file changes = %+v", msg.FileChanges)
	}
	if msg.TokenUsage.Total != 23 {
		t.Fatalf("token usage = %+v", msg.TokenUsage)
	}
}

func TestScanPaxdArtifactRejectsInvalidTagsJSON(t *testing.T) {
	now := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	_, err := scanPaxdArtifact(fakeRow{
		"artifact_1",
		"paxd",
		"darwin-arm64",
		[]byte(`{not-json}`),
		"v1",
		"build_1",
		"bucket",
		"object",
		int64(123),
		"sha",
		int64(456),
		"application/octet-stream",
		"tester",
		now,
		(*time.Time)(nil),
	})
	if err == nil {
		t.Fatal("scan artifact err = nil, want invalid JSON error")
	}
}

func TestScanNodeAndRegistrationSessions(t *testing.T) {
	now := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	approvedAt := now.Add(time.Minute)
	consumedAt := now.Add(2 * time.Minute)
	node, err := scanNode(fakeRow{
		"node_1",
		"usr_1",
		"workstation",
		"Studio",
		"Local node",
		"studio.local",
		"desktop",
		"darwin",
		"arm64",
		"1.2.3",
		"http://localhost:8642",
		"online",
		&now,
		now,
		[]byte(`{"label":"primary"}`),
		[]byte(`{"region":"local"}`),
	})
	if err != nil {
		t.Fatalf("scan node: %v", err)
	}
	if !node.Online || string(node.Metadata) != `{"region":"local"}` {
		t.Fatalf("node = %+v", node)
	}

	registration, err := scanNodeRegistrationSession(fakeRow{
		"reg_1",
		"PAIR-1",
		"poll_hash",
		domain.NodeRegistrationStatusApproved,
		"usr_1",
		"node_1",
		"Studio",
		"studio.local",
		"desktop",
		"darwin",
		"arm64",
		"1.2.3",
		"http://localhost:8642",
		[]byte(`{"kind":"dev"}`),
		"127.0.0.1",
		"San Francisco",
		"US",
		now.Add(time.Hour),
		now,
		&approvedAt,
		&consumedAt,
	})
	if err != nil {
		t.Fatalf("scan node registration: %v", err)
	}
	if registration.Request.Metadata == nil ||
		string(registration.Request.Metadata) != `{"kind":"dev"}` ||
		registration.ApprovedAt == nil ||
		registration.ConsumedAt == nil {
		t.Fatalf("registration = %+v", registration)
	}

	login, err := scanPaxlDeviceLoginSession(fakeRow{
		"login_1",
		"ABCD-EFGH",
		"poll_hash",
		domain.PaxlDeviceLoginStatusApproved,
		"paxl",
		"usr_1",
		"key_1",
		"node_1",
		"paxu_raw",
		now.Add(time.Hour),
		now,
		&approvedAt,
		&consumedAt,
	})
	if err != nil {
		t.Fatalf("scan paxl login: %v", err)
	}
	if login.APIKey != "paxu_raw" || login.ApprovedAt == nil || login.ConsumedAt == nil {
		t.Fatalf("login = %+v", login)
	}
}

func TestScanApprovalSecretAndArtifactRows(t *testing.T) {
	now := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	decidedAt := now.Add(time.Minute)
	revokedAt := now.Add(2 * time.Minute)
	expiresAt := now.Add(time.Hour)

	approval, err := scanApproval(fakeRow{
		"appr_1",
		"usr_1",
		"node_1",
		"agent_1",
		"sess_1",
		"msg_1",
		"node_1",
		"agent_1",
		"*",
		"agent_action",
		"tool.run",
		"tool",
		"shell",
		"Run shell",
		"Execute shell command",
		"high",
		"fingerprint",
		[]byte(`{"command":"go test"}`),
		[]byte(`[{"kind":"exec"}]`),
		[]byte(`[{"option_id":"allow","decision":"allow","scope":"agent"}]`),
		"decided",
		"allow",
		"allow_for_this_agent",
		"agent",
		[]byte(`{"reason":"approved"}`),
		"usr_approver",
		&revokedAt,
		"usr_revoke",
		"cleanup",
		now,
		&expiresAt,
		&decidedAt,
		[]byte(`{"raw":true}`),
	})
	if err != nil {
		t.Fatalf("scan approval: %v", err)
	}
	if len(approval.Options) != 1 ||
		approval.Options[0].OptionID != "allow" ||
		string(approval.GrantBody) != `{"reason":"approved"}` {
		t.Fatalf("approval = %+v", approval)
	}

	secret, err := scanSecret(fakeRow{
		"secret_1",
		"usr_1",
		"OPENAI_API_KEY",
		"token",
		"API key",
		[]byte(`{"scope":"agent"}`),
		"version_2",
		int64(2),
		now,
		now.Add(time.Minute),
		(*time.Time)(nil),
	})
	if err != nil {
		t.Fatalf("scan secret: %v", err)
	}
	if secret.CurrentVersion != 2 || string(secret.Metadata) != `{"scope":"agent"}` {
		t.Fatalf("secret = %+v", secret)
	}

	version, err := scanSecretVersion(fakeRow{
		"version_2",
		"secret_1",
		int64(2),
		[]byte("cipher"),
		[]byte("nonce"),
		"key_1",
		"active",
		now,
		"usr_1",
		"node_1",
		"agent_1",
		"idem_1",
	})
	if err != nil {
		t.Fatalf("scan secret version: %v", err)
	}
	if version.VersionNumber != 2 ||
		string(version.Ciphertext) != "cipher" ||
		version.IdempotencyKey != "idem_1" {
		t.Fatalf("version = %+v", version)
	}

	artifact, err := scanPaxdArtifact(fakeRow{
		"artifact_1",
		"paxd",
		"darwin-arm64",
		[]byte(`["stable","latest"]`),
		"v1",
		"build_1",
		"bucket",
		"object",
		int64(123),
		"sha",
		int64(456),
		"application/octet-stream",
		"tester",
		now,
		(*time.Time)(nil),
	})
	if err != nil {
		t.Fatalf("scan artifact: %v", err)
	}
	if !reflect.DeepEqual(artifact.Tags, []string{"stable", "latest"}) ||
		artifact.Generation != 123 {
		t.Fatalf("artifact = %+v", artifact)
	}
}

func TestMapSQLError(t *testing.T) {
	if !errors.Is(mapSQLError(sql.ErrNoRows), ErrNotFound) {
		t.Fatal("sql.ErrNoRows did not map to ErrNotFound")
	}
	err := errors.New("driver failed")
	if !errors.Is(mapSQLError(err), err) {
		t.Fatal("non-not-found error was not preserved")
	}
}
