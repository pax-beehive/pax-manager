package storage

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestPostgresStoreHelperDecisionTables(t *testing.T) {
	if !isUniqueViolation(errors.New(`pq: duplicate key value violates unique constraint`)) {
		t.Fatal("duplicate key error was not recognized as unique violation")
	}
	if isUniqueViolation(nil) || isUniqueViolation(errors.New("foreign key failed")) {
		t.Fatal("non-unique error was recognized as unique violation")
	}

	if got := jsonOrNil(map[string]any{}); got != nil {
		t.Fatalf("empty JSON object = %v, want nil", got)
	}
	if got := jsonOrNil(map[string]string{"route": "paxl"}); string(
		got.([]byte),
	) != `{"route":"paxl"}` {
		t.Fatalf("jsonOrNil object = %s", got)
	}
	if got := jsonDefault(json.RawMessage(`not-json`), "{}"); string(got) != `{}` {
		t.Fatalf("invalid json default = %s", got)
	}
	if got := jsonOrDefault(nil, "[]"); string(got) != `[]` {
		t.Fatalf("nil json default = %s", got)
	}
}

func TestSecretVersionLookup(t *testing.T) {
	secret := Secret{SecretID: "secret_1", CurrentVersionID: "version_current"}
	cases := []struct {
		name     string
		selector string
		wantArgs []any
	}{
		{"empty selector uses current version", "", []any{"version_current", "secret_1"}},
		{"latest selector uses current version", "latest", []any{"version_current", "secret_1"}},
		{"numeric selector uses version number", "2", []any{"secret_1", int64(2)}},
		{"version prefix selector uses version number", "version:3", []any{"secret_1", int64(3)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			query, args, err := secretVersionLookup(secret, tc.selector)
			if err != nil {
				t.Fatalf("lookup: %v", err)
			}
			if query == "" || !reflect.DeepEqual(args, tc.wantArgs) {
				t.Fatalf("query=%q args=%#v, want args %#v", query, args, tc.wantArgs)
			}
		})
	}

	_, _, err := secretVersionLookup(Secret{SecretID: "secret_1"}, "latest")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing current version err = %v, want not found", err)
	}
	_, _, err = secretVersionLookup(secret, "version:bad")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("bad selector err = %v, want not found", err)
	}
}

func TestApprovalDecisionGrant(t *testing.T) {
	approval := AgentApproval{
		RequestNodeID:    "node_1",
		RequestAgentID:   "agent_1",
		RequestSessionID: "sess_1",
	}
	cases := []struct {
		name   string
		req    ApprovalDecisionRequest
		grant  [5]string
		hasErr bool
	}{
		{
			name:  "deny scopes to original request",
			req:   ApprovalDecisionRequest{DecisionOption: "deny"},
			grant: [5]string{"deny", "once", "node_1", "agent_1", "sess_1"},
		},
		{
			name:  "allow once scopes to original request",
			req:   ApprovalDecisionRequest{DecisionOption: "allow_once"},
			grant: [5]string{"allow", "once", "node_1", "agent_1", "sess_1"},
		},
		{
			name:  "allow agent wildcards session",
			req:   ApprovalDecisionRequest{DecisionOption: "allow_for_this_agent"},
			grant: [5]string{"allow", "agent", "node_1", "agent_1", "*"},
		},
		{
			name:  "allow node wildcards agent and session",
			req:   ApprovalDecisionRequest{DecisionOption: "allow_for_this_node"},
			grant: [5]string{"allow", "node", "node_1", "*", "*"},
		},
		{
			name:  "allow all wildcards every route component",
			req:   ApprovalDecisionRequest{DecisionOption: "allow_always_on_all_agents"},
			grant: [5]string{"allow", "across_all_agents", "*", "*", "*"},
		},
		{
			name: "custom scope uses explicit grant fields",
			req: ApprovalDecisionRequest{
				GrantNodeID:    "node_custom",
				GrantAgentID:   "agent_custom",
				GrantSessionID: "sess_custom",
			},
			grant: [5]string{"allow", "custom", "node_custom", "agent_custom", "sess_custom"},
		},
		{
			name:   "custom scope requires at least one explicit grant field",
			req:    ApprovalDecisionRequest{},
			hasErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			decision, scope, nodeID, agentID, sessionID, err := approvalDecisionGrant(
				approval,
				tc.req,
			)
			if tc.hasErr {
				if !errors.Is(err, ErrConflict) {
					t.Fatalf("err = %v, want conflict", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("approval decision grant: %v", err)
			}
			got := [5]string{decision, scope, nodeID, agentID, sessionID}
			if got != tc.grant {
				t.Fatalf("grant = %#v, want %#v", got, tc.grant)
			}
		})
	}
}

func TestPostgresApprovalDefaults(t *testing.T) {
	if defaultApprovalDomain(" ") != "agent_action" {
		t.Fatal("blank approval domain did not default")
	}
	if defaultApprovalDomain("secret_vault") != "secret_vault" {
		t.Fatal("explicit approval domain was not preserved")
	}
	if defaultApprovalRiskLevel(" ") != "unknown" {
		t.Fatal("blank risk level did not default")
	}
	if defaultApprovalRiskLevel("high") != "high" {
		t.Fatal("explicit risk level was not preserved")
	}
}

func TestApprovalListQueryBuildsFiltersAndLimit(t *testing.T) {
	query, args := approvalListQuery(ApprovalFilter{
		Principal: UserPrincipal{User: User{UserID: "usr_1"}},
		Status:    "pending",
		Decision:  "allow",
		Domain:    "agent_action",
		Operation: "tool.run",
		Limit:     200,
	})
	for _, fragment := range []string{
		"owner_user_id = $1",
		"status = $2",
		"decision = $3",
		"domain = $4",
		"operation = $5",
		"grant_revoked_at IS NULL",
		"LIMIT $6",
	} {
		if !strings.Contains(query, fragment) {
			t.Fatalf("query missing %q:\n%s", fragment, query)
		}
	}
	wantArgs := []any{"usr_1", "pending", "allow", "agent_action", "tool.run", 50}
	if !reflect.DeepEqual(args, wantArgs) {
		t.Fatalf("args = %#v, want %#v", args, wantArgs)
	}

	query, args = approvalListQuery(ApprovalFilter{
		Principal:      UserPrincipal{User: User{UserID: "usr_1"}},
		RequestNodeID:  "node_1",
		RequestAgentID: "agent_1",
		IncludeRevoked: true,
		Limit:          25,
	})
	if strings.Contains(query, "grant_revoked_at IS NULL") {
		t.Fatalf("include revoked query filtered revoked grants:\n%s", query)
	}
	wantArgs = []any{"usr_1", "node_1", "agent_1", 25}
	if !reflect.DeepEqual(args, wantArgs) {
		t.Fatalf("include revoked args = %#v, want %#v", args, wantArgs)
	}
}

func TestApprovalGrantListQueryScopesActiveGrants(t *testing.T) {
	query, args := approvalGrantListQuery(ApprovalGrantFilter{
		Principal:      UserPrincipal{User: User{UserID: "usr_1"}},
		Domain:         "agent_action",
		ResourceType:   "tool",
		DecisionScope:  "agent",
		GrantNodeID:    "node_1",
		GrantAgentID:   "agent_1",
		GrantSessionID: "*",
		ActiveOnly:     true,
		Limit:          0,
	})
	for _, fragment := range []string{
		"status = 'decided'",
		"decision = 'allow'",
		"decision_scope <> 'once'",
		"owner_user_id = $1",
		"domain = $2",
		"resource_type = $3",
		"decision_scope = $4",
		"grant_node_id = $5",
		"grant_agent_id = $6",
		"grant_session_id = $7",
		"grant_revoked_at IS NULL",
		"LIMIT $8",
	} {
		if !strings.Contains(query, fragment) {
			t.Fatalf("query missing %q:\n%s", fragment, query)
		}
	}
	wantArgs := []any{
		"usr_1",
		"agent_action",
		"tool",
		"agent",
		"node_1",
		"agent_1",
		"*",
		50,
	}
	if !reflect.DeepEqual(args, wantArgs) {
		t.Fatalf("args = %#v, want %#v", args, wantArgs)
	}

	query, args = approvalGrantListQuery(ApprovalGrantFilter{
		Principal:  UserPrincipal{User: User{UserID: "usr_1"}},
		Operation:  "tool.run",
		ActiveOnly: false,
		Limit:      10,
	})
	if strings.Contains(query, "grant_revoked_at IS NULL") {
		t.Fatalf("inactive query filtered revoked grants:\n%s", query)
	}
	wantArgs = []any{"usr_1", "tool.run", 10}
	if !reflect.DeepEqual(args, wantArgs) {
		t.Fatalf("inactive args = %#v, want %#v", args, wantArgs)
	}
}

func TestApprovalGrantOrderingHelpers(t *testing.T) {
	allAgents := AgentApproval{GrantNodeID: "*", GrantAgentID: "*", GrantSessionID: "*"}
	agentScoped := AgentApproval{
		GrantNodeID:    "node_1",
		GrantAgentID:   "agent_1",
		GrantSessionID: "*",
	}
	sessionScoped := AgentApproval{
		GrantNodeID:    "node_1",
		GrantAgentID:   "agent_1",
		GrantSessionID: "sess_1",
	}
	if approvalSpecificity(allAgents) != 0 ||
		approvalSpecificity(agentScoped) != 2 ||
		approvalSpecificity(sessionScoped) != 3 {
		t.Fatalf(
			"specificity = %d/%d/%d",
			approvalSpecificity(allAgents),
			approvalSpecificity(agentScoped),
			approvalSpecificity(sessionScoped),
		)
	}

	now := time.Date(2026, 6, 28, 10, 0, 0, 0, time.UTC)
	later := now.Add(time.Minute)
	if !timeAfter(&later, &now) {
		t.Fatal("later time did not sort after earlier time")
	}
	if !timeAfter(&now, nil) {
		t.Fatal("non-nil time did not sort after nil")
	}
	if timeAfter(nil, &now) {
		t.Fatal("nil time sorted after non-nil")
	}
}
