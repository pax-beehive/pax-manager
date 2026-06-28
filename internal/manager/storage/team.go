package storage

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

type teamRow struct {
	TeamID      string     `gorm:"column:team_id;primaryKey"`
	OwnerUserID string     `gorm:"column:owner_user_id"`
	Name        string     `gorm:"column:name"`
	Description string     `gorm:"column:description"`
	Status      string     `gorm:"column:status"`
	CreatedAt   time.Time  `gorm:"column:created_at"`
	ArchivedAt  *time.Time `gorm:"column:archived_at"`
}

func (teamRow) TableName() string {
	return "teams"
}

type teamMemberRow struct {
	TeamID          string     `gorm:"column:team_id;primaryKey"`
	UserID          string     `gorm:"column:user_id;primaryKey"`
	Role            string     `gorm:"column:role"`
	Status          string     `gorm:"column:status"`
	InvitedByUserID *string    `gorm:"column:invited_by_user_id"`
	JoinedAt        time.Time  `gorm:"column:joined_at"`
	RemovedAt       *time.Time `gorm:"column:removed_at"`
	RemovedByUserID *string    `gorm:"column:removed_by_user_id"`
}

func (teamMemberRow) TableName() string {
	return "team_members"
}

type teamInviteRow struct {
	InviteID        string     `gorm:"column:invite_id;primaryKey"`
	TeamID          string     `gorm:"column:team_id"`
	Email           string     `gorm:"column:email"`
	RecipientUserID *string    `gorm:"column:recipient_user_id"`
	Role            string     `gorm:"column:role"`
	Status          string     `gorm:"column:status"`
	InvitedByUserID string     `gorm:"column:invited_by_user_id"`
	CreatedAt       time.Time  `gorm:"column:created_at"`
	AcceptedAt      *time.Time `gorm:"column:accepted_at"`
	DeclinedAt      *time.Time `gorm:"column:declined_at"`
	CanceledAt      *time.Time `gorm:"column:canceled_at"`
}

func (teamInviteRow) TableName() string {
	return "team_invites"
}

type teamAgentRow struct {
	TeamID           string     `gorm:"column:team_id;primaryKey"`
	AgentID          string     `gorm:"column:agent_id;primaryKey"`
	AgentOwnerUserID string     `gorm:"column:agent_owner_user_id"`
	AddedByUserID    string     `gorm:"column:added_by_user_id"`
	AddedAt          time.Time  `gorm:"column:added_at"`
	RemovedAt        *time.Time `gorm:"column:removed_at"`
	RemovedByUserID  *string    `gorm:"column:removed_by_user_id"`
}

func (teamAgentRow) TableName() string {
	return "team_agents"
}

type teamAuditEventRow struct {
	EventID        string          `gorm:"column:event_id;primaryKey"`
	TeamID         string          `gorm:"column:team_id"`
	ActorUserID    string          `gorm:"column:actor_user_id"`
	Action         string          `gorm:"column:action"`
	TargetUserID   *string         `gorm:"column:target_user_id"`
	TargetAgentID  *string         `gorm:"column:target_agent_id"`
	TargetInviteID *string         `gorm:"column:target_invite_id"`
	Metadata       json.RawMessage `gorm:"column:metadata"`
	CreatedAt      time.Time       `gorm:"column:created_at"`
}

func (teamAuditEventRow) TableName() string {
	return "team_audit_events"
}

func teamModel(team Team) *teamRow {
	return &teamRow{
		TeamID:      team.TeamID,
		OwnerUserID: team.OwnerUserID,
		Name:        team.Name,
		Description: team.Description,
		Status:      team.Status,
		CreatedAt:   team.CreatedAt,
		ArchivedAt:  team.ArchivedAt,
	}
}

func teamFromModel(row *teamRow) Team {
	if row == nil {
		return Team{}
	}
	return Team{
		TeamID:      row.TeamID,
		OwnerUserID: row.OwnerUserID,
		Name:        row.Name,
		Description: row.Description,
		Status:      row.Status,
		CreatedAt:   row.CreatedAt,
		ArchivedAt:  row.ArchivedAt,
	}
}

func teamMemberModel(member TeamMember) *teamMemberRow {
	invitedBy := stringPtrOrNil(member.InvitedByUser)
	removedBy := stringPtrOrNil(member.RemovedByUser)
	return &teamMemberRow{
		TeamID:          member.TeamID,
		UserID:          member.UserID,
		Role:            member.Role,
		Status:          member.Status,
		InvitedByUserID: invitedBy,
		JoinedAt:        member.JoinedAt,
		RemovedAt:       member.RemovedAt,
		RemovedByUserID: removedBy,
	}
}

func teamMemberFromModel(row *teamMemberRow) TeamMember {
	if row == nil {
		return TeamMember{}
	}
	return TeamMember{
		TeamID:        row.TeamID,
		UserID:        row.UserID,
		Role:          row.Role,
		Status:        row.Status,
		InvitedByUser: stringFromPtr(row.InvitedByUserID),
		JoinedAt:      row.JoinedAt,
		RemovedAt:     row.RemovedAt,
		RemovedByUser: stringFromPtr(row.RemovedByUserID),
	}
}

func teamInviteModel(invite TeamInvite) *teamInviteRow {
	recipientUserID := stringPtrOrNil(invite.RecipientUserID)
	return &teamInviteRow{
		InviteID:        invite.InviteID,
		TeamID:          invite.TeamID,
		Email:           invite.Email,
		RecipientUserID: recipientUserID,
		Role:            invite.Role,
		Status:          invite.Status,
		InvitedByUserID: invite.InvitedByUserID,
		CreatedAt:       invite.CreatedAt,
		AcceptedAt:      invite.AcceptedAt,
		DeclinedAt:      invite.DeclinedAt,
		CanceledAt:      invite.CanceledAt,
	}
}

func teamInviteFromModel(row *teamInviteRow) TeamInvite {
	if row == nil {
		return TeamInvite{}
	}
	return TeamInvite{
		InviteID:        row.InviteID,
		TeamID:          row.TeamID,
		Email:           row.Email,
		RecipientUserID: stringFromPtr(row.RecipientUserID),
		Role:            row.Role,
		Status:          row.Status,
		InvitedByUserID: row.InvitedByUserID,
		CreatedAt:       row.CreatedAt,
		AcceptedAt:      row.AcceptedAt,
		DeclinedAt:      row.DeclinedAt,
		CanceledAt:      row.CanceledAt,
	}
}

func teamAgentFromModel(row *teamAgentRow) TeamAgent {
	if row == nil {
		return TeamAgent{}
	}
	return TeamAgent{
		TeamID:           row.TeamID,
		AgentID:          row.AgentID,
		AgentOwnerUserID: row.AgentOwnerUserID,
		AddedByUserID:    row.AddedByUserID,
		AddedAt:          row.AddedAt,
		RemovedAt:        row.RemovedAt,
		RemovedByUserID:  stringFromPtr(row.RemovedByUserID),
	}
}

func teamAuditEventModel(event TeamAuditEvent) *teamAuditEventRow {
	return &teamAuditEventRow{
		EventID:        event.EventID,
		TeamID:         event.TeamID,
		ActorUserID:    event.ActorUserID,
		Action:         event.Action,
		TargetUserID:   stringPtrOrNil(event.TargetUserID),
		TargetAgentID:  stringPtrOrNil(event.TargetAgentID),
		TargetInviteID: stringPtrOrNil(event.TargetInviteID),
		Metadata:       jsonDefault(event.Metadata, "{}"),
		CreatedAt:      event.CreatedAt,
	}
}

func teamAuditEventFromModel(row *teamAuditEventRow) TeamAuditEvent {
	if row == nil {
		return TeamAuditEvent{}
	}
	return TeamAuditEvent{
		EventID:        row.EventID,
		TeamID:         row.TeamID,
		ActorUserID:    row.ActorUserID,
		Action:         row.Action,
		TargetUserID:   stringFromPtr(row.TargetUserID),
		TargetAgentID:  stringFromPtr(row.TargetAgentID),
		TargetInviteID: stringFromPtr(row.TargetInviteID),
		Metadata:       jsonDefault(row.Metadata, "{}"),
		CreatedAt:      row.CreatedAt,
	}
}

func stringPtrOrNil(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func stringFromPtr(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func (s *PostgresStore) createTeamAuditEventTx(
	ctx context.Context,
	tx *gorm.DB,
	event TeamAuditEvent,
) error {
	if event.TeamID == "" || event.ActorUserID == "" || event.Action == "" {
		return ErrConflict
	}
	if event.EventID == "" {
		eventID, err := newSecret("taud")
		if err != nil {
			return err
		}
		event.EventID = eventID
	}
	if event.CreatedAt.IsZero() {
		event.CreatedAt = s.now().UTC()
	}
	if len(event.Metadata) == 0 {
		event.Metadata = json.RawMessage(`{}`)
	}
	return tx.WithContext(ctx).Create(teamAuditEventModel(event)).Error
}

func (s *PostgresStore) CreateTeam(ctx context.Context, team Team, owner TeamMember) (Team, error) {
	err := s.gormDB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(teamModel(team)).Error; err != nil {
			if isUniqueViolation(err) {
				return ErrConflict
			}
			return err
		}
		if err := tx.Create(teamMemberModel(owner)).Error; err != nil {
			if isUniqueViolation(err) {
				return ErrConflict
			}
			return err
		}
		if err := s.createTeamAuditEventTx(ctx, tx, TeamAuditEvent{
			TeamID:      team.TeamID,
			ActorUserID: team.OwnerUserID,
			Action:      domain.TeamAuditActionTeamCreated,
			CreatedAt:   team.CreatedAt,
		}); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return Team{}, err
	}
	return team, nil
}

func (s *PostgresStore) ListTeams(
	ctx context.Context,
	principal UserPrincipal,
) ([]TeamSummary, error) {
	var rows []struct {
		TeamID      string
		OwnerUserID string
		Name        string
		Description string
		Status      string
		CreatedAt   time.Time
		ArchivedAt  *time.Time
		MyRole      string
		MemberCount int
		AgentCount  int
	}
	err := s.gormDB.WithContext(ctx).Raw(`
		SELECT
			t.team_id,
			t.owner_user_id,
			t.name,
			t.description,
			t.status,
			t.created_at,
			t.archived_at,
			tm.role AS my_role,
			(
				SELECT COUNT(*)
				FROM team_members count_tm
				WHERE count_tm.team_id = t.team_id AND count_tm.status = ?
			) AS member_count,
			(
				SELECT COUNT(*)
				FROM team_agents count_ta
				WHERE count_ta.team_id = t.team_id AND count_ta.removed_at IS NULL
			) AS agent_count
		FROM teams t
		JOIN team_members tm ON tm.team_id = t.team_id
		WHERE tm.user_id = ? AND tm.status = ? AND t.status = ?
		ORDER BY t.created_at DESC
	`, domain.TeamMemberStatusActive, principal.User.UserID,
		domain.TeamMemberStatusActive, domain.TeamStatusActive).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make([]TeamSummary, 0, len(rows))
	for _, row := range rows {
		out = append(out, TeamSummary{
			Team: Team{
				TeamID:      row.TeamID,
				OwnerUserID: row.OwnerUserID,
				Name:        row.Name,
				Description: row.Description,
				Status:      row.Status,
				CreatedAt:   row.CreatedAt,
				ArchivedAt:  row.ArchivedAt,
			},
			MyRole:      row.MyRole,
			MemberCount: row.MemberCount,
			AgentCount:  row.AgentCount,
		})
	}
	return out, nil
}

func (s *PostgresStore) GetTeam(
	ctx context.Context,
	principal UserPrincipal,
	teamID string,
) (Team, error) {
	var row teamRow
	err := s.gormDB.WithContext(ctx).
		Model(&teamRow{}).
		Joins("JOIN team_members ON team_members.team_id = teams.team_id").
		Where("teams.team_id = ?", teamID).
		Where("teams.status = ?", domain.TeamStatusActive).
		Where("team_members.user_id = ? AND team_members.status = ?",
			principal.User.UserID,
			domain.TeamMemberStatusActive).
		First(&row).Error
	if err != nil {
		return Team{}, mapGormError(err)
	}
	return teamFromModel(&row), nil
}

func (s *PostgresStore) ListTeamMembers(
	ctx context.Context,
	principal UserPrincipal,
	teamID string,
) ([]TeamMember, error) {
	if _, err := s.activeTeamMember(ctx, s.gormDB, teamID, principal.User.UserID); err != nil {
		return nil, err
	}
	var rows []struct {
		TeamID          string
		UserID          string
		Email           string
		Role            string
		Status          string
		InvitedByUserID string
		JoinedAt        time.Time
		RemovedAt       *time.Time
		RemovedByUserID string
	}
	err := s.gormDB.WithContext(ctx).Raw(`
		SELECT
			tm.team_id,
			tm.user_id,
			users.email,
			tm.role,
			tm.status,
			COALESCE(tm.invited_by_user_id, '') AS invited_by_user_id,
			tm.joined_at,
			tm.removed_at,
			COALESCE(tm.removed_by_user_id, '') AS removed_by_user_id
		FROM team_members tm
		JOIN users ON users.user_id = tm.user_id
		WHERE tm.team_id = ? AND tm.status = ?
		ORDER BY tm.joined_at ASC
	`, teamID, domain.TeamMemberStatusActive).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make([]TeamMember, 0, len(rows))
	for _, row := range rows {
		out = append(out, TeamMember{
			TeamID:        row.TeamID,
			UserID:        row.UserID,
			Email:         row.Email,
			Role:          row.Role,
			Status:        row.Status,
			InvitedByUser: row.InvitedByUserID,
			JoinedAt:      row.JoinedAt,
			RemovedAt:     row.RemovedAt,
			RemovedByUser: row.RemovedByUserID,
		})
	}
	return out, nil
}

func (s *PostgresStore) ListTeamAgents(
	ctx context.Context,
	principal UserPrincipal,
	teamID string,
) ([]TeamAgent, error) {
	if _, err := s.activeTeamMember(ctx, s.gormDB, teamID, principal.User.UserID); err != nil {
		return nil, err
	}
	var rows []struct {
		TeamID           string
		AgentID          string
		AgentOwnerUserID string
		AgentOwnerEmail  string
		AddedByUserID    string
		AddedAt          time.Time
		AgentName        string
		AgentNodeID      string
		AgentStatus      string
		AgentOnline      bool
		AgentType        string
		AgentMachineType string
		AgentOS          string
		AgentHostname    string
		LastHeartbeat    *time.Time
		RegisteredAt     time.Time
	}
	err := s.gormDB.WithContext(ctx).Raw(`
		SELECT
			ta.team_id,
			ta.agent_id,
			ta.agent_owner_user_id,
			users.email AS agent_owner_email,
			ta.added_by_user_id,
			ta.added_at,
			COALESCE(agents.name, '') AS agent_name,
			COALESCE(agents.node_id, '') AS agent_node_id,
			COALESCE(agents.status, '') AS agent_status,
			computed_status(agents.last_heartbeat) = 'online' AS agent_online,
			COALESCE(agents.agent_type, '') AS agent_type,
			COALESCE(agents.machine_type, '') AS agent_machine_type,
			COALESCE(agents.os, '') AS agent_os,
			COALESCE(agents.hostname, '') AS agent_hostname,
			agents.last_heartbeat,
			agents.registered_at
		FROM team_agents ta
		JOIN users ON users.user_id = ta.agent_owner_user_id
		JOIN agents ON agents.agent_id = ta.agent_id
		WHERE ta.team_id = ? AND ta.removed_at IS NULL
		ORDER BY ta.added_at ASC
	`, teamID).Scan(&rows).Error
	if err != nil {
		return nil, mapGormError(err)
	}
	out := make([]TeamAgent, 0, len(rows))
	for i := range rows {
		row := rows[i]
		teamAgent := TeamAgent{
			TeamID:           row.TeamID,
			AgentID:          row.AgentID,
			AgentOwnerUserID: row.AgentOwnerUserID,
			AgentOwnerEmail:  row.AgentOwnerEmail,
			AddedByUserID:    row.AddedByUserID,
			AddedAt:          row.AddedAt,
		}
		if row.AgentName != "" || row.AgentNodeID != "" {
			teamAgent.Agent = &Agent{
				AgentID:       row.AgentID,
				NodeID:        row.AgentNodeID,
				OwnerUserID:   row.AgentOwnerUserID,
				Name:          row.AgentName,
				Hostname:      row.AgentHostname,
				AgentType:     row.AgentType,
				MachineType:   row.AgentMachineType,
				OS:            row.AgentOS,
				Status:        row.AgentStatus,
				Online:        row.AgentOnline,
				LastHeartbeat: row.LastHeartbeat,
				RegisteredAt:  row.RegisteredAt,
			}
		}
		out = append(out, teamAgent)
	}
	return out, nil
}

func (s *PostgresStore) ListTeamAuditEvents(
	ctx context.Context,
	principal UserPrincipal,
	teamID string,
	limit int,
) ([]TeamAuditEvent, error) {
	if _, err := s.activeTeamMember(ctx, s.gormDB, teamID, principal.User.UserID); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	var rows []teamAuditEventRow
	err := s.gormDB.WithContext(ctx).
		Model(&teamAuditEventRow{}).
		Where("team_id = ?", teamID).
		Order("created_at DESC").
		Limit(limit).
		Find(&rows).Error
	if err != nil {
		return nil, mapGormError(err)
	}
	out := make([]TeamAuditEvent, 0, len(rows))
	for i := range rows {
		out = append(out, teamAuditEventFromModel(&rows[i]))
	}
	return out, nil
}

func (s *PostgresStore) CreateTeamInvite(
	ctx context.Context,
	principal UserPrincipal,
	invite TeamInvite,
) (TeamInvite, error) {
	err := s.gormDB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		member, err := s.activeTeamMember(ctx, tx, invite.TeamID, principal.User.UserID)
		if err != nil {
			return err
		}
		if member.Role != domain.TeamRoleOwner {
			return ErrUnauthorized
		}
		if invite.Role == "" || invite.Role == domain.TeamRoleOwner {
			return ErrUnauthorized
		}
		if invite.RecipientUserID != "" {
			if _, err := s.activeTeamMember(ctx, tx, invite.TeamID, invite.RecipientUserID); err == nil {
				return ErrConflict
			} else if !errors.Is(err, ErrNotFound) {
				return err
			}
		}
		row := teamInviteModel(invite)
		row.Email = normalizeEmail(row.Email)
		if err := tx.Create(row).Error; err != nil {
			if isUniqueViolation(err) {
				return ErrConflict
			}
			return err
		}
		invite = teamInviteFromModel(row)
		if err := s.createTeamAuditEventTx(ctx, tx, TeamAuditEvent{
			TeamID:         invite.TeamID,
			ActorUserID:    principal.User.UserID,
			Action:         domain.TeamAuditActionInviteCreated,
			TargetInviteID: invite.InviteID,
			TargetUserID:   invite.RecipientUserID,
			CreatedAt:      invite.CreatedAt,
		}); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return TeamInvite{}, err
	}
	return invite, nil
}

func (s *PostgresStore) ListTeamInvites(
	ctx context.Context,
	principal UserPrincipal,
) ([]TeamInvite, error) {
	principalEmail := normalizeEmail(principal.User.Email)
	var rows []teamInviteRow
	err := s.gormDB.WithContext(ctx).
		Model(&teamInviteRow{}).
		Where("status = ?", domain.TeamInviteStatusPending).
		Where(
			"recipient_user_id = ? OR (recipient_user_id = '' AND email = ?) OR (recipient_user_id IS NULL AND email = ?)",
			principal.User.UserID,
			principalEmail,
			principalEmail,
		).
		Order("created_at DESC").
		Find(&rows).Error
	if err != nil {
		return nil, mapGormError(err)
	}
	out := make([]TeamInvite, 0, len(rows))
	for i := range rows {
		out = append(out, teamInviteFromModel(&rows[i]))
	}
	return out, nil
}

func (s *PostgresStore) ListTeamSentInvites(
	ctx context.Context,
	principal UserPrincipal,
	teamID string,
) ([]TeamInvite, error) {
	member, err := s.activeTeamMember(ctx, s.gormDB, teamID, principal.User.UserID)
	if err != nil {
		return nil, err
	}
	if !teamRoleCanViewTeamInvites(member.Role) {
		return nil, ErrUnauthorized
	}
	var rows []teamInviteRow
	err = s.gormDB.WithContext(ctx).
		Model(&teamInviteRow{}).
		Where("team_id = ?", teamID).
		Order("created_at DESC").
		Find(&rows).Error
	if err != nil {
		return nil, mapGormError(err)
	}
	out := make([]TeamInvite, 0, len(rows))
	for i := range rows {
		out = append(out, teamInviteFromModel(&rows[i]))
	}
	return out, nil
}

func (s *PostgresStore) AcceptTeamInvite(
	ctx context.Context,
	principal UserPrincipal,
	inviteID string,
	acceptedAt time.Time,
) (TeamInvite, error) {
	var invite TeamInvite
	err := s.gormDB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		row, err := s.visiblePendingTeamInvite(ctx, tx, principal, inviteID)
		if err != nil {
			return err
		}
		if _, err := s.activeTeamMember(ctx, tx, row.TeamID, principal.User.UserID); err == nil {
			return ErrConflict
		} else if !errors.Is(err, ErrNotFound) {
			return err
		}
		if err := tx.Model(&teamInviteRow{}).
			Where("invite_id = ?", inviteID).
			Updates(map[string]any{
				"status":            domain.TeamInviteStatusAccepted,
				"recipient_user_id": principal.User.UserID,
				"accepted_at":       acceptedAt,
			}).Error; err != nil {
			return err
		}
		member := teamMemberRow{
			TeamID:          row.TeamID,
			UserID:          principal.User.UserID,
			Role:            row.Role,
			Status:          domain.TeamMemberStatusActive,
			InvitedByUserID: stringPtrOrNil(row.InvitedByUserID),
			JoinedAt:        acceptedAt,
		}
		if err := tx.Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "team_id"}, {Name: "user_id"}},
			DoUpdates: clause.Assignments(map[string]any{
				"role":               member.Role,
				"status":             member.Status,
				"invited_by_user_id": member.InvitedByUserID,
				"joined_at":          member.JoinedAt,
				"removed_at":         nil,
				"removed_by_user_id": nil,
			}),
		}).Create(&member).Error; err != nil {
			return err
		}
		row.Status = domain.TeamInviteStatusAccepted
		row.RecipientUserID = stringPtrOrNil(principal.User.UserID)
		row.AcceptedAt = &acceptedAt
		invite = teamInviteFromModel(&row)
		if err := s.createTeamAuditEventTx(ctx, tx, TeamAuditEvent{
			TeamID:         invite.TeamID,
			ActorUserID:    principal.User.UserID,
			Action:         domain.TeamAuditActionInviteAccepted,
			TargetInviteID: invite.InviteID,
			TargetUserID:   principal.User.UserID,
			CreatedAt:      acceptedAt,
		}); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return TeamInvite{}, err
	}
	return invite, nil
}

func (s *PostgresStore) DeclineTeamInvite(
	ctx context.Context,
	principal UserPrincipal,
	inviteID string,
	declinedAt time.Time,
) (TeamInvite, error) {
	var invite TeamInvite
	err := s.gormDB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		row, err := s.visiblePendingTeamInvite(ctx, tx, principal, inviteID)
		if err != nil {
			return err
		}
		if err := tx.Model(&teamInviteRow{}).
			Where("invite_id = ?", inviteID).
			Updates(map[string]any{
				"status":      domain.TeamInviteStatusDeclined,
				"declined_at": declinedAt,
			}).Error; err != nil {
			return err
		}
		row.Status = domain.TeamInviteStatusDeclined
		row.DeclinedAt = &declinedAt
		invite = teamInviteFromModel(&row)
		if err := s.createTeamAuditEventTx(ctx, tx, TeamAuditEvent{
			TeamID:         invite.TeamID,
			ActorUserID:    principal.User.UserID,
			Action:         domain.TeamAuditActionInviteDeclined,
			TargetInviteID: invite.InviteID,
			TargetUserID:   principal.User.UserID,
			CreatedAt:      declinedAt,
		}); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return TeamInvite{}, err
	}
	return invite, nil
}

func (s *PostgresStore) CancelTeamInvite(
	ctx context.Context,
	principal UserPrincipal,
	teamID string,
	inviteID string,
	canceledAt time.Time,
) (TeamInvite, error) {
	var invite TeamInvite
	err := s.gormDB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		member, err := s.activeTeamMember(ctx, tx, teamID, principal.User.UserID)
		if err != nil {
			return err
		}
		if member.Role != domain.TeamRoleOwner {
			return ErrUnauthorized
		}
		var row teamInviteRow
		err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("team_id = ? AND invite_id = ? AND status = ?",
				teamID, inviteID, domain.TeamInviteStatusPending).
			First(&row).Error
		if err != nil {
			return mapGormError(err)
		}
		if err := tx.Model(&teamInviteRow{}).
			Where("invite_id = ?", inviteID).
			Updates(map[string]any{
				"status":      domain.TeamInviteStatusCanceled,
				"canceled_at": canceledAt,
			}).Error; err != nil {
			return err
		}
		row.Status = domain.TeamInviteStatusCanceled
		row.CanceledAt = &canceledAt
		invite = teamInviteFromModel(&row)
		return s.createTeamAuditEventTx(ctx, tx, TeamAuditEvent{
			TeamID:         teamID,
			ActorUserID:    principal.User.UserID,
			Action:         domain.TeamAuditActionInviteCanceled,
			TargetInviteID: inviteID,
			TargetUserID:   invite.RecipientUserID,
			CreatedAt:      canceledAt,
		})
	})
	if err != nil {
		return TeamInvite{}, err
	}
	return invite, nil
}

func (s *PostgresStore) AddTeamAgent(
	ctx context.Context,
	principal UserPrincipal,
	teamID string,
	agentID string,
	addedAt time.Time,
) (TeamAgent, error) {
	var out TeamAgent
	err := s.gormDB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		member, err := s.activeTeamMember(ctx, tx, teamID, principal.User.UserID)
		if err != nil {
			return err
		}
		if !teamRoleCanManageOwnAgents(member.Role) {
			return ErrUnauthorized
		}
		var agentOwnerUserID string
		err = tx.Raw(`
			SELECT owner_user_id
			FROM agents
			WHERE agent_id = ?
			LIMIT 1
		`, agentID).Scan(&agentOwnerUserID).Error
		if err != nil {
			return err
		}
		if agentOwnerUserID == "" || agentOwnerUserID != principal.User.UserID {
			return ErrNotFound
		}
		row := teamAgentRow{}
		err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("team_id = ? AND agent_id = ?", teamID, agentID).
			First(&row).Error
		switch {
		case err == nil && row.RemovedAt == nil:
			return ErrConflict
		case err == nil:
			row.AgentOwnerUserID = agentOwnerUserID
			row.AddedByUserID = principal.User.UserID
			row.AddedAt = addedAt
			row.RemovedAt = nil
			row.RemovedByUserID = nil
			if err := tx.Save(&row).Error; err != nil {
				return err
			}
			out = teamAgentFromModel(&row)
			out.AgentOwnerEmail = principal.User.Email
			if err := s.createTeamAuditEventTx(ctx, tx, TeamAuditEvent{
				TeamID:        teamID,
				ActorUserID:   principal.User.UserID,
				Action:        domain.TeamAuditActionAgentAdded,
				TargetAgentID: agentID,
				CreatedAt:     addedAt,
			}); err != nil {
				return err
			}
			return nil
		case !errors.Is(err, gorm.ErrRecordNotFound):
			return mapGormError(err)
		}
		row = teamAgentRow{
			TeamID:           teamID,
			AgentID:          agentID,
			AgentOwnerUserID: agentOwnerUserID,
			AddedByUserID:    principal.User.UserID,
			AddedAt:          addedAt,
		}
		if err := tx.Create(&row).Error; err != nil {
			if isUniqueViolation(err) {
				return ErrConflict
			}
			return err
		}
		out = teamAgentFromModel(&row)
		out.AgentOwnerEmail = principal.User.Email
		if err := s.createTeamAuditEventTx(ctx, tx, TeamAuditEvent{
			TeamID:        teamID,
			ActorUserID:   principal.User.UserID,
			Action:        domain.TeamAuditActionAgentAdded,
			TargetAgentID: agentID,
			CreatedAt:     addedAt,
		}); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return TeamAgent{}, err
	}
	return out, nil
}

func (s *PostgresStore) RemoveTeamAgent(
	ctx context.Context,
	principal UserPrincipal,
	teamID string,
	agentID string,
	removedAt time.Time,
) (TeamAgent, error) {
	var out TeamAgent
	err := s.gormDB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		member, err := s.activeTeamMember(ctx, tx, teamID, principal.User.UserID)
		if err != nil {
			return err
		}
		row := teamAgentRow{}
		err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("team_id = ? AND agent_id = ? AND removed_at IS NULL", teamID, agentID).
			First(&row).Error
		if err != nil {
			return mapGormError(err)
		}
		if member.Role != domain.TeamRoleOwner &&
			(member.Role != domain.TeamRoleOperator ||
				row.AgentOwnerUserID != principal.User.UserID) {
			return ErrUnauthorized
		}
		row.RemovedAt = &removedAt
		row.RemovedByUserID = stringPtrOrNil(principal.User.UserID)
		if err := tx.Save(&row).Error; err != nil {
			return err
		}
		out = teamAgentFromModel(&row)
		if err := s.createTeamAuditEventTx(ctx, tx, TeamAuditEvent{
			TeamID:        teamID,
			ActorUserID:   principal.User.UserID,
			Action:        domain.TeamAuditActionAgentRemoved,
			TargetAgentID: agentID,
			CreatedAt:     removedAt,
		}); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return TeamAgent{}, err
	}
	return out, nil
}

func (s *PostgresStore) RemoveTeamMember(
	ctx context.Context,
	principal UserPrincipal,
	teamID string,
	userID string,
	removedAt time.Time,
) (TeamMember, error) {
	var out TeamMember
	err := s.gormDB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		requester, err := s.activeTeamMember(ctx, tx, teamID, principal.User.UserID)
		if err != nil {
			return err
		}
		target, err := s.activeTeamMember(ctx, tx, teamID, userID)
		if err != nil {
			return err
		}
		if target.Role == domain.TeamRoleOwner {
			return ErrConflict
		}
		if target.UserID != principal.User.UserID && requester.Role != domain.TeamRoleOwner {
			return ErrUnauthorized
		}
		if err := tx.Model(&teamMemberRow{}).
			Where("team_id = ? AND user_id = ? AND status = ?", teamID, userID, domain.TeamMemberStatusActive).
			Updates(map[string]any{
				"status":             domain.TeamMemberStatusRemoved,
				"removed_at":         removedAt,
				"removed_by_user_id": principal.User.UserID,
			}).Error; err != nil {
			return err
		}
		if err := tx.Model(&teamAgentRow{}).
			Where("team_id = ? AND agent_owner_user_id = ? AND removed_at IS NULL", teamID, userID).
			Updates(map[string]any{
				"removed_at":         removedAt,
				"removed_by_user_id": principal.User.UserID,
			}).Error; err != nil {
			return err
		}
		target.Status = domain.TeamMemberStatusRemoved
		target.RemovedAt = &removedAt
		target.RemovedByUser = principal.User.UserID
		out = target
		return s.createTeamAuditEventTx(ctx, tx, TeamAuditEvent{
			TeamID:       teamID,
			ActorUserID:  principal.User.UserID,
			Action:       domain.TeamAuditActionMemberRemoved,
			TargetUserID: userID,
			CreatedAt:    removedAt,
		})
	})
	if err != nil {
		return TeamMember{}, err
	}
	return out, nil
}

func (s *PostgresStore) UpdateTeamMemberRole(
	ctx context.Context,
	principal UserPrincipal,
	teamID string,
	userID string,
	role string,
	updatedAt time.Time,
) (TeamMember, error) {
	var out TeamMember
	err := s.gormDB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		requester, err := s.activeTeamMember(ctx, tx, teamID, principal.User.UserID)
		if err != nil {
			return err
		}
		if requester.Role != domain.TeamRoleOwner {
			return ErrUnauthorized
		}
		if role != domain.TeamRoleOperator && role != domain.TeamRoleMember {
			return ErrUnauthorized
		}
		var row teamMemberRow
		err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("team_id = ? AND user_id = ? AND status = ?",
				teamID, userID, domain.TeamMemberStatusActive).
			First(&row).Error
		if err != nil {
			return mapGormError(err)
		}
		if row.Role == domain.TeamRoleOwner {
			return ErrConflict
		}
		previousRole := row.Role
		if previousRole != role {
			if err := tx.Model(&teamMemberRow{}).
				Where("team_id = ? AND user_id = ?", teamID, userID).
				Update("role", role).Error; err != nil {
				return err
			}
			row.Role = role
			metadata, err := json.Marshal(map[string]string{
				"previous_role": previousRole,
				"role":          role,
			})
			if err != nil {
				return err
			}
			if err := s.createTeamAuditEventTx(ctx, tx, TeamAuditEvent{
				TeamID:       teamID,
				ActorUserID:  principal.User.UserID,
				Action:       domain.TeamAuditActionMemberRoleUpdated,
				TargetUserID: userID,
				Metadata:     metadata,
				CreatedAt:    updatedAt,
			}); err != nil {
				return err
			}
		}
		out = teamMemberFromModel(&row)
		if user, err := s.GetUser(ctx, userID); err == nil {
			out.Email = user.Email
		}
		return nil
	})
	if err != nil {
		return TeamMember{}, err
	}
	return out, nil
}

func (s *PostgresStore) ArchiveTeam(
	ctx context.Context,
	principal UserPrincipal,
	teamID string,
	archivedAt time.Time,
) (Team, error) {
	var out Team
	err := s.gormDB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		member, err := s.activeTeamMember(ctx, tx, teamID, principal.User.UserID)
		if err != nil {
			return err
		}
		if member.Role != domain.TeamRoleOwner {
			return ErrUnauthorized
		}
		var row teamRow
		err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("team_id = ? AND status = ?", teamID, domain.TeamStatusActive).
			First(&row).Error
		if err != nil {
			return mapGormError(err)
		}
		if err := tx.Model(&teamRow{}).
			Where("team_id = ?", teamID).
			Updates(map[string]any{
				"status":      domain.TeamStatusArchived,
				"archived_at": archivedAt,
			}).Error; err != nil {
			return err
		}
		row.Status = domain.TeamStatusArchived
		row.ArchivedAt = &archivedAt
		out = teamFromModel(&row)
		return s.createTeamAuditEventTx(ctx, tx, TeamAuditEvent{
			TeamID:      teamID,
			ActorUserID: principal.User.UserID,
			Action:      domain.TeamAuditActionTeamArchived,
			CreatedAt:   archivedAt,
		})
	})
	if err != nil {
		return Team{}, err
	}
	return out, nil
}

func (s *PostgresStore) activeTeamMember(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	userID string,
) (TeamMember, error) {
	var row teamMemberRow
	err := tx.WithContext(ctx).
		Model(&teamMemberRow{}).
		Joins("JOIN teams ON teams.team_id = team_members.team_id").
		Where("team_members.team_id = ?", teamID).
		Where("team_members.user_id = ?", userID).
		Where("team_members.status = ?", domain.TeamMemberStatusActive).
		Where("teams.status = ?", domain.TeamStatusActive).
		First(&row).Error
	if err != nil {
		return TeamMember{}, mapGormError(err)
	}
	return teamMemberFromModel(&row), nil
}

func (s *PostgresStore) visiblePendingTeamInvite(
	ctx context.Context,
	tx *gorm.DB,
	principal UserPrincipal,
	inviteID string,
) (teamInviteRow, error) {
	var row teamInviteRow
	principalEmail := normalizeEmail(principal.User.Email)
	err := tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("invite_id = ?", inviteID).
		Where("status = ?", domain.TeamInviteStatusPending).
		Where(
			"recipient_user_id = ? OR (recipient_user_id = '' AND email = ?) OR (recipient_user_id IS NULL AND email = ?)",
			principal.User.UserID,
			principalEmail,
			principalEmail,
		).
		First(&row).Error
	if err != nil {
		return teamInviteRow{}, mapGormError(err)
	}
	return row, nil
}
