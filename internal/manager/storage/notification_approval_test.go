package storage

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/pax-manager/internal/manager/notifications"
)

type approvalNotificationStore interface {
	notificationTestStore
	CreateApproval(context.Context, Node, CreateApprovalRequest) (AgentApproval, error)
	notifications.ApprovalDeliveryAuthorizer
}

func approvalNotificationFixture(
	t *testing.T,
	backend string,
	now func() time.Time,
) (approvalNotificationStore, string) {
	t.Helper()
	store, owner, _ := completionFixture(t, backend, now)
	if s, ok := store.(*PostgresStore); ok {
		schema, err := os.ReadFile("../../../db/init.sql")
		require.NoError(t, err)
		// The approval creation path uses native ID translation and audit records.
		for _, ddl := range []string{
			`ALTER TABLE agent_sessions ADD COLUMN metadata JSONB DEFAULT '{}', ADD COLUMN native_id TEXT DEFAULT '', ADD COLUMN updated_at TIMESTAMPTZ DEFAULT NOW()`,
			`ALTER TABLE agent_sessions ADD COLUMN created_at TIMESTAMPTZ DEFAULT NOW()`,
		} {
			require.NoError(t, s.gormDB.Exec(ddl).Error)
		}
		for _, name := range []string{"agent_approvals", "agent_audit_events"} {
			start := strings.Index(string(schema), "CREATE TABLE IF NOT EXISTS "+name+" (")
			require.NotEqual(t, -1, start)
			ddl := string(schema)[start:]
			end := strings.Index(ddl, ");")
			require.NoError(t, s.gormDB.Exec(ddl[:end+2]).Error)
		}
		require.NoError(
			t,
			s.gormDB.Exec(
				`CREATE UNIQUE INDEX audit_source ON agent_audit_events(source_type,source_id,event_type,event_key)`,
			).Error,
		)
	}
	return store.(approvalNotificationStore), owner
}

func setApprovalNotificationMode(t *testing.T, store approvalNotificationStore, mode string) {
	t.Helper()
	if s, ok := store.(*MemoryStore); ok {
		session := s.sessions[sessionKey("agent", "session")]
		session.PaxConfig.ApprovalMode = mode
		s.sessions[sessionKey("agent", "session")] = session
	} else {
		require.NoError(t, store.(*PostgresStore).gormDB.Exec(`UPDATE agent_sessions SET metadata=jsonb_build_object('pax_config',jsonb_build_object('approval_mode',?::text))`, mode).Error)
	}
}

func TestApprovalNotificationGivenManualOrAutoSessionThenOnlyNotifiesActionableRequests(
	t *testing.T,
) {
	for _, backend := range []string{"memory", "postgres"} {
		for _, mode := range []string{"", "manual", "auto_approve_all"} {
			t.Run(backend+"/"+mode, func(t *testing.T) {
				now := time.Now().UTC()
				store, owner := approvalNotificationFixture(
					t,
					backend,
					func() time.Time { return now },
				)
				setApprovalNotificationMode(t, store, mode)
				for _, device := range []string{"phone", "desktop"} {
					_, err := store.RegisterNotificationReceiver(
						t.Context(),
						owner,
						notificationRegistration(t, device),
					)
					require.NoError(t, err)
				}
				approval, err := store.CreateApproval(
					t.Context(),
					Node{NodeID: "node", OwnerUserID: owner},
					CreateApprovalRequest{
						AgentID:      "agent",
						SessionID:    "session",
						Title:        "private command",
						Description:  "private details",
						Operation:    "session/request_permission",
						ResourceType: "acp_permission",
					},
				)
				require.NoError(t, err)
				jobs, err := store.ClaimNotificationDeliveries(t.Context(), 10, 30*time.Second)
				require.NoError(t, err)
				if mode == domain.SessionApprovalModeAutoApproveAll {
					assert.Empty(t, jobs)
					return
				}
				require.Len(t, jobs, 2)
				for _, job := range jobs {
					assert.Equal(t, "approval.required", job.Payload.Type)
					assert.Equal(t, approval.ApprovalID, job.Payload.ApprovalID)
					assert.Equal(t, "/?session_id=session", job.Payload.URL)
					assert.NotContains(t, job.Payload.Body, "private")
					allowed, err := store.CanDeliverApprovalNotification(t.Context(), job)
					require.NoError(t, err)
					assert.True(t, allowed)
				}
				setApprovalNotificationMode(t, store, domain.SessionApprovalModeAutoApproveAll)
				allowed, err := store.CanDeliverApprovalNotification(t.Context(), jobs[0])
				require.NoError(t, err)
				assert.False(t, allowed)
			})
		}
	}
}

func TestApprovalNotificationGivenResolvedExpiredOrInaccessibleRequestThenSuppressesIt(
	t *testing.T,
) {
	for _, backend := range []string{"memory", "postgres"} {
		for _, scenario := range []string{"resolved", "expired", "foreign", "missing", "deleted_session"} {
			t.Run(fmt.Sprintf("%s/%s", backend, scenario), func(t *testing.T) {
				now := time.Now().UTC()
				store, owner := approvalNotificationFixture(
					t,
					backend,
					func() time.Time { return now },
				)
				_, err := store.RegisterNotificationReceiver(
					t.Context(),
					owner,
					notificationRegistration(t, "phone"),
				)
				require.NoError(t, err)
				expires := now.Add(time.Minute)
				approval, err := store.CreateApproval(
					t.Context(),
					Node{NodeID: "node", OwnerUserID: owner},
					CreateApprovalRequest{
						AgentID:   "agent",
						SessionID: "session",
						ExpiresAt: &expires,
					},
				)
				require.NoError(t, err)
				jobs, err := store.ClaimNotificationDeliveries(t.Context(), 10, 30*time.Second)
				require.NoError(t, err)
				require.Len(t, jobs, 1)
				job := jobs[0]
				switch scenario {
				case "expired":
					now = expires
				case "foreign":
					job.UserID = "other"
				case "missing":
					job.Payload.ApprovalID = "missing"
				case "resolved":
					if s, ok := store.(*MemoryStore); ok {
						a := s.approvals[approval.ApprovalID]
						a.Status = "approved"
						s.approvals[a.ApprovalID] = a
					} else {
						require.NoError(t, store.(*PostgresStore).gormDB.Exec(`UPDATE agent_approvals SET status='approved'`).Error)
					}
				case "deleted_session":
					if s, ok := store.(*MemoryStore); ok {
						delete(s.sessions, sessionKey("agent", "session"))
					} else {
						require.NoError(t, store.(*PostgresStore).gormDB.Exec(`DELETE FROM agent_sessions`).Error)
					}
				}
				allowed, err := store.CanDeliverApprovalNotification(t.Context(), job)
				require.NoError(t, err)
				assert.False(t, allowed)
			})
		}
	}
}

func TestApprovalNotificationGivenNoActionableSessionThenSkipsFanout(t *testing.T) {
	for _, backend := range []string{"memory", "postgres"} {
		for _, scenario := range []string{"no_session", "encrypted", "expired"} {
			t.Run(backend+"/"+scenario, func(t *testing.T) {
				now := time.Now().UTC()
				store, owner := approvalNotificationFixture(
					t,
					backend,
					func() time.Time { return now },
				)
				_, err := store.RegisterNotificationReceiver(
					t.Context(),
					owner,
					notificationRegistration(t, "phone"),
				)
				require.NoError(t, err)
				req := CreateApprovalRequest{AgentID: "agent", SessionID: "session"}
				switch scenario {
				case "no_session":
					req.SessionID = ""
				case "expired":
					expired := now.Add(-time.Second)
					req.ExpiresAt = &expired
				case "encrypted":
					if s, ok := store.(*MemoryStore); ok {
						session := s.sessions[sessionKey("agent", "session")]
						session.Transport = "e2ee"
						s.sessions[sessionKey("agent", "session")] = session
					} else {
						require.NoError(t, store.(*PostgresStore).gormDB.Exec(`UPDATE agent_sessions SET transport='e2ee'`).Error)
					}
				}
				_, err = store.CreateApproval(
					t.Context(),
					Node{NodeID: "node", OwnerUserID: owner},
					req,
				)
				require.NoError(t, err)
				jobs, err := store.ClaimNotificationDeliveries(t.Context(), 10, 30*time.Second)
				require.NoError(t, err)
				assert.Empty(t, jobs)
			})
		}
	}
}

func TestApprovalNotificationGivenFanoutFailureThenRollsBackApprovalAndDelivery(t *testing.T) {
	for _, table := range []string{"notification_receivers", "notification_deliveries"} {
		t.Run(table, func(t *testing.T) {
			now := time.Now().UTC()
			store, owner := approvalNotificationFixture(
				t,
				"postgres",
				func() time.Time { return now },
			)
			s := store.(*PostgresStore)
			_, err := s.RegisterNotificationReceiver(
				t.Context(),
				owner,
				notificationRegistration(t, "phone"),
			)
			require.NoError(t, err)
			require.NoError(t, s.gormDB.Exec("ALTER TABLE "+table+" RENAME TO paused_"+table).Error)
			req := CreateApprovalRequest{AgentID: "agent", SessionID: "session"}
			_, err = s.CreateApproval(t.Context(), Node{NodeID: "node", OwnerUserID: owner}, req)
			require.Error(t, err)
			var count int64
			require.NoError(t, s.gormDB.Table("agent_approvals").Count(&count).Error)
			assert.Zero(t, count)
			require.NoError(t, s.gormDB.Exec("ALTER TABLE paused_"+table+" RENAME TO "+table).Error)
			_, err = s.CreateApproval(t.Context(), Node{NodeID: "node", OwnerUserID: owner}, req)
			require.NoError(t, err)
			jobs, err := s.ClaimNotificationDeliveries(t.Context(), 10, 30*time.Second)
			require.NoError(t, err)
			assert.Len(t, jobs, 1)
		})
	}
}
