package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"strings"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	dbmodel "github.com/pax-beehive/pax-manager/internal/manager/storage/dal/model"
	"github.com/pax-beehive/pax-manager/internal/manager/storage/dal/query"
)

type PostgresStore struct {
	db     *sql.DB
	gormDB *gorm.DB
	q      *query.Query
	now    func() time.Time
}

func NewPostgresStore(db *sql.DB, now func() time.Time) *PostgresStore {
	gormDB, err := gorm.Open(postgres.New(postgres.Config{Conn: db}), &gorm.Config{})
	if err != nil {
		panic(err)
	}
	return &PostgresStore{
		db:     db,
		gormDB: gormDB,
		q:      query.Use(gormDB),
		now:    now,
	}
}

func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "duplicate key")
}

func (s *PostgresStore) EnsureSchema(ctx context.Context) error {
	initSQL, err := os.ReadFile("db/init.sql")
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, string(initSQL))
	return err
}

func (s *PostgresStore) EnsureUser(
	ctx context.Context,
	email string,
	displayName string,
	role string,
) (User, error) {
	email = normalizeEmail(email)
	if email == "" {
		return User{}, ErrUnauthorized
	}
	if role == "" {
		role = "user"
	}
	userID, err := newSecret("usr")
	if err != nil {
		return User{}, err
	}
	now := s.now().UTC()
	row := dbmodel.User{
		UserID:      userID,
		Email:       email,
		DisplayName: displayName,
		Role:        &role,
		CreatedAt:   &now,
		LastSeenAt:  &now,
	}
	err = s.gormDB.WithContext(ctx).Clauses(
		clause.OnConflict{
			Columns: []clause.Column{{Name: "email"}},
			DoUpdates: clause.Assignments(map[string]any{
				"display_name": gorm.Expr(
					"COALESCE(NULLIF(EXCLUDED.display_name, ''), users.display_name)",
				),
				"role":         gorm.Expr("EXCLUDED.role"),
				"last_seen_at": gorm.Expr("EXCLUDED.last_seen_at"),
			}),
		},
		clause.Returning{},
	).Create(&row).Error
	if err != nil {
		return User{}, err
	}
	return userFromModel(&row), nil
}

func (s *PostgresStore) GetUserByEmail(ctx context.Context, email string) (User, error) {
	u := s.q.User
	row, err := u.WithContext(ctx).Where(u.Email.Eq(normalizeEmail(email))).First()
	if err != nil {
		return User{}, mapGormError(err)
	}
	return userFromModel(row), nil
}

func (s *PostgresStore) GetUser(ctx context.Context, userID string) (User, error) {
	u := s.q.User
	row, err := u.WithContext(ctx).Where(u.UserID.Eq(userID)).First()
	if err != nil {
		return User{}, mapGormError(err)
	}
	return userFromModel(row), nil
}

func (s *PostgresStore) CreateRegistrationToken(
	ctx context.Context,
	ownerUserID string,
	tokenHash string,
	expiresAt *time.Time,
) error {
	now := s.now().UTC()
	token := dbmodel.AgentRegistrationToken{
		TokenHash:   tokenHash,
		OwnerUserID: ownerUserID,
		ExpiresAt:   expiresAt,
		CreatedAt:   &now,
	}
	return s.q.AgentRegistrationToken.WithContext(ctx).Create(&token)
}

func (s *PostgresStore) ResolveRegistrationToken(
	ctx context.Context,
	tokenHash string,
) (User, error) {
	var user User
	err := s.gormDB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		qtx := query.Use(tx)
		tokens := qtx.AgentRegistrationToken
		token, err := tokens.WithContext(ctx).
			Clauses(clause.Locking{Strength: "UPDATE"}).
			Where(tokens.TokenHash.Eq(tokenHash)).
			First()
		if err != nil {
			return mapGormError(err)
		}
		now := s.now().UTC()
		if token.UsedAt != nil || (token.ExpiresAt != nil && token.ExpiresAt.Before(now)) {
			return ErrUnauthorized
		}
		info, err := tokens.WithContext(ctx).
			Where(tokens.TokenHash.Eq(tokenHash)).
			Update(tokens.UsedAt, now)
		if err != nil {
			return err
		}
		if info.RowsAffected == 0 {
			return ErrNotFound
		}
		users := qtx.User
		row, err := users.WithContext(ctx).Where(users.UserID.Eq(token.OwnerUserID)).First()
		if err != nil {
			return mapGormError(err)
		}
		user = userFromModel(row)
		return nil
	})
	if err != nil {
		return User{}, err
	}
	return user, nil
}

func (s *PostgresStore) CreateUserAPIKey(
	ctx context.Context,
	principal UserPrincipal,
	name string,
	keyHash string,
	prefix string,
) (UserAPIKey, error) {
	keyID, err := newSecret("key")
	if err != nil {
		return UserAPIKey{}, err
	}
	now := s.now().UTC()
	row := dbmodel.UserAPIKey{
		KeyID:       keyID,
		OwnerUserID: principal.User.UserID,
		Name:        name,
		KeyHash:     keyHash,
		Prefix:      prefix,
		CreatedAt:   &now,
	}
	if err := s.q.UserAPIKey.WithContext(ctx).Create(&row); err != nil {
		return UserAPIKey{}, err
	}
	return userAPIKeyFromModel(&row), nil
}

func (s *PostgresStore) ListUserAPIKeys(
	ctx context.Context,
	principal UserPrincipal,
) ([]UserAPIKey, error) {
	keys := s.q.UserAPIKey
	rows, err := keys.WithContext(ctx).
		Where(keys.OwnerUserID.Eq(principal.User.UserID)).
		Order(keys.CreatedAt.Desc()).
		Find()
	if err != nil {
		return nil, err
	}
	return userAPIKeysFromModels(rows), nil
}

func (s *PostgresStore) RevokeUserAPIKey(
	ctx context.Context,
	principal UserPrincipal,
	keyID string,
) error {
	now := s.now().UTC()
	keys := s.q.UserAPIKey
	info, err := keys.WithContext(ctx).
		Where(keys.KeyID.Eq(keyID), keys.OwnerUserID.Eq(principal.User.UserID)).
		Update(keys.RevokedAt, now)
	if err != nil {
		return err
	}
	if info.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PostgresStore) AuthenticateUserAPIKey(ctx context.Context, keyHash string) (User, error) {
	var user User
	err := s.gormDB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		qtx := query.Use(tx)
		keys := qtx.UserAPIKey
		key, err := keys.WithContext(ctx).
			Clauses(clause.Locking{Strength: "UPDATE"}).
			Where(keys.KeyHash.Eq(keyHash)).
			First()
		if err != nil {
			return mapGormError(err)
		}
		if key.RevokedAt != nil {
			return ErrUnauthorized
		}
		now := s.now().UTC()
		info, err := keys.WithContext(ctx).
			Where(keys.KeyID.Eq(key.KeyID)).
			Update(keys.LastUsedAt, now)
		if err != nil {
			return err
		}
		if info.RowsAffected == 0 {
			return ErrNotFound
		}
		users := qtx.User
		row, err := users.WithContext(ctx).Where(users.UserID.Eq(key.OwnerUserID)).First()
		if err != nil {
			return mapGormError(err)
		}
		user = userFromModel(row)
		return nil
	})
	if err != nil {
		return User{}, err
	}
	return user, nil
}

func (s *PostgresStore) CreateSecret(
	ctx context.Context,
	principal UserPrincipal,
	req CreateSecretRequest,
	encrypted SecretVersion,
) (Secret, SecretVersion, error) {
	secretID, err := newSecret("sec")
	if err != nil {
		return Secret{}, SecretVersion{}, err
	}
	versionID, err := newSecret("secver")
	if err != nil {
		return Secret{}, SecretVersion{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Secret{}, SecretVersion{}, err
	}
	defer func() { _ = tx.Rollback() }()
	now := s.now().UTC()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO secrets (
			secret_id, owner_user_id, name, kind, description, metadata, created_at, updated_at
		)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$7)
	`, secretID, principal.User.UserID, req.Name, req.Kind, req.Description,
		jsonDefault(req.Metadata, "{}"), now); err != nil {
		return Secret{}, SecretVersion{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO secret_versions (
			version_id, secret_id, version_number, ciphertext, nonce, key_id, state,
			created_at, created_by_user_id
		)
		VALUES ($1,$2,1,$3,$4,$5,'active',$6,$7)
	`, versionID, secretID, encrypted.Ciphertext, encrypted.Nonce, encrypted.KeyID, now,
		principal.User.UserID); err != nil {
		return Secret{}, SecretVersion{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE secrets
		SET current_version_id = $2, current_version = 1, updated_at = $3
		WHERE secret_id = $1
	`, secretID, versionID, now); err != nil {
		return Secret{}, SecretVersion{}, err
	}
	secret, err := scanSecret(tx.QueryRowContext(ctx, secretSelectSQL+`
		WHERE secret_id = $1
	`, secretID))
	if err != nil {
		return Secret{}, SecretVersion{}, err
	}
	version, err := scanSecretVersion(tx.QueryRowContext(ctx, secretVersionSelectSQL+`
		WHERE version_id = $1
	`, versionID))
	if err != nil {
		return Secret{}, SecretVersion{}, err
	}
	if err := tx.Commit(); err != nil {
		return Secret{}, SecretVersion{}, err
	}
	return secret, version, nil
}

func (s *PostgresStore) ListSecrets(
	ctx context.Context,
	principal UserPrincipal,
) ([]Secret, error) {
	rows, err := s.db.QueryContext(ctx, secretSelectSQL+`
		WHERE owner_user_id = $1 AND deleted_at IS NULL
		ORDER BY created_at DESC
	`, principal.User.UserID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return scanSecrets(rows)
}

func (s *PostgresStore) GetSecret(
	ctx context.Context,
	principal UserPrincipal,
	secretID string,
) (Secret, error) {
	return scanSecret(s.db.QueryRowContext(ctx, secretSelectSQL+`
		WHERE secret_id = $1 AND owner_user_id = $2 AND deleted_at IS NULL
	`, secretID, principal.User.UserID))
}

func (s *PostgresStore) GetSecretVersionForNode(
	ctx context.Context,
	node Node,
	agentID string,
	secretID string,
	versionSelector string,
) (Secret, SecretVersion, error) {
	if _, err := s.GetNodeAgent(ctx, node.NodeID, agentID); err != nil {
		return Secret{}, SecretVersion{}, err
	}
	secret, err := scanSecret(s.db.QueryRowContext(ctx, secretSelectSQL+`
		WHERE secret_id = $1 AND owner_user_id = $2 AND deleted_at IS NULL
	`, secretID, node.OwnerUserID))
	if err != nil {
		return Secret{}, SecretVersion{}, err
	}
	versionQuery, args, err := secretVersionLookup(secret, versionSelector)
	if err != nil {
		return Secret{}, SecretVersion{}, err
	}
	version, err := scanSecretVersion(s.db.QueryRowContext(ctx, versionQuery, args...))
	if err != nil {
		return Secret{}, SecretVersion{}, err
	}
	if version.State != "active" {
		return Secret{}, SecretVersion{}, ErrUnauthorized
	}
	return secret, version, nil
}

func (s *PostgresStore) CreateSecretVersion(
	ctx context.Context,
	node Node,
	agentID string,
	req WriteSecretVersionRequest,
	encrypted SecretVersion,
) (SecretVersion, bool, error) {
	if _, err := s.GetNodeAgent(ctx, node.NodeID, agentID); err != nil {
		return SecretVersion{}, false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SecretVersion{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	if req.IdempotencyKey != "" {
		existing, current, err := s.findIdempotentSecretVersion(
			ctx,
			tx,
			req.SecretID,
			node.NodeID,
			agentID,
			req.IdempotencyKey,
		)
		if err == nil {
			return existing, current, tx.Commit()
		}
		if !errors.Is(err, ErrNotFound) {
			return SecretVersion{}, false, err
		}
	}
	var currentVersionID string
	var ownerUserID string
	var nextVersionNumber int64
	if err := tx.QueryRowContext(ctx, `
		SELECT owner_user_id, COALESCE(current_version_id, ''), current_version + 1
		FROM secrets
		WHERE secret_id = $1 AND deleted_at IS NULL
		FOR UPDATE
	`, req.SecretID).Scan(&ownerUserID, &currentVersionID, &nextVersionNumber); err != nil {
		return SecretVersion{}, false, mapSQLError(err)
	}
	if ownerUserID != node.OwnerUserID {
		return SecretVersion{}, false, ErrNotFound
	}
	versionID, err := newSecret("secver")
	if err != nil {
		return SecretVersion{}, false, err
	}
	now := s.now().UTC()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO secret_versions (
			version_id, secret_id, version_number, ciphertext, nonce, key_id, state,
			created_at, created_by_node_id, created_by_agent_id, idempotency_key
		)
		VALUES ($1,$2,$3,$4,$5,$6,'active',$7,$8,$9,$10)
	`, versionID, req.SecretID, nextVersionNumber, encrypted.Ciphertext, encrypted.Nonce,
		encrypted.KeyID, now, node.NodeID, agentID, req.IdempotencyKey); err != nil {
		return SecretVersion{}, false, err
	}
	current := false
	if req.MakeCurrent {
		if req.ExpectedCurrentVersionID == "" || req.ExpectedCurrentVersionID != currentVersionID {
			return SecretVersion{}, false, ErrConflict
		}
		result, err := tx.ExecContext(ctx, `
			UPDATE secrets
			SET current_version_id = $3, current_version = $4, updated_at = $5
			WHERE secret_id = $1 AND current_version_id = $2
		`, req.SecretID, req.ExpectedCurrentVersionID, versionID, nextVersionNumber, now)
		if err != nil {
			return SecretVersion{}, false, err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return SecretVersion{}, false, err
		}
		if affected == 0 {
			return SecretVersion{}, false, ErrConflict
		}
		current = true
	}
	version, err := scanSecretVersion(tx.QueryRowContext(ctx, secretVersionSelectSQL+`
		WHERE version_id = $1
	`, versionID))
	if err != nil {
		return SecretVersion{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return SecretVersion{}, false, err
	}
	return version, current, nil
}

func (s *PostgresStore) RecordSecretAccess(
	ctx context.Context,
	event SecretAccessEvent,
) error {
	if event.AgentID != "" && event.SessionID != "" {
		sessionID, err := s.virtualSessionID(ctx, s.db, event.AgentID, event.SessionID)
		if err != nil {
			return err
		}
		event.SessionID = sessionID
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO secret_access_events (
			secret_id, version_id, node_id, agent_id, session_id, action, result, created_at
		)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
	`, event.SecretID, event.VersionID, event.NodeID, event.AgentID, event.SessionID,
		event.Action, event.Result, s.now().UTC())
	return err
}

const paxdArtifactSelectSQL = `
	SELECT artifact_id, product, platform, array_to_json(tags), version, build_id, bucket, object,
		generation, sha256, size_bytes, content_type, created_by, created_at, deleted_at
	FROM paxd_artifacts
`

func (s *PostgresStore) CreatePaxdArtifact(
	ctx context.Context,
	req CreatePaxdArtifactRequest,
	createdBy string,
) (PaxdArtifact, error) {
	artifactID, err := newSecret("paxdart")
	if err != nil {
		return PaxdArtifact{}, err
	}
	row := s.db.QueryRowContext(ctx, `
		INSERT INTO paxd_artifacts (
			artifact_id, product, platform, tags, version, build_id, bucket, object, generation,
			sha256, size_bytes, content_type, created_by, created_at
		)
		VALUES (
			$1, $2, $3,
			CASE WHEN $4 = '' THEN '{}'::text[] ELSE string_to_array($4, ',')::text[] END,
			$5, $6, $7, $8, $9, $10, $11, $12, $13, $14
		)
		ON CONFLICT (bucket, object, generation) DO UPDATE SET
			product = EXCLUDED.product,
			platform = EXCLUDED.platform,
			tags = EXCLUDED.tags,
			version = EXCLUDED.version,
			build_id = EXCLUDED.build_id,
			sha256 = EXCLUDED.sha256,
			size_bytes = EXCLUDED.size_bytes,
			content_type = EXCLUDED.content_type,
			created_by = EXCLUDED.created_by,
			deleted_at = NULL
		RETURNING artifact_id, product, platform, array_to_json(tags), version, build_id, bucket, object,
			generation, sha256, size_bytes, content_type, created_by, created_at, deleted_at
	`, artifactID, req.Product, req.Platform, paxdArtifactTagList(req.Tags), req.Version, req.BuildID,
		req.Bucket, req.Object, req.Generation, req.SHA256, req.SizeBytes, req.ContentType,
		createdBy, s.now().UTC())
	return scanPaxdArtifact(row)
}

func (s *PostgresStore) FindPaxdArtifact(
	ctx context.Context,
	req FindPaxdArtifactRequest,
) (PaxdArtifact, error) {
	row := s.db.QueryRowContext(ctx, paxdArtifactSelectSQL+`
		WHERE product = $1
			AND platform = $2
			AND tags @> CASE
				WHEN $3 = '' THEN '{}'::text[]
				ELSE string_to_array($3, ',')::text[]
			END
			AND deleted_at IS NULL
		ORDER BY created_at DESC, artifact_id DESC
		LIMIT 1
	`, req.Product, req.Platform, paxdArtifactTagList(req.Tags))
	return scanPaxdArtifact(row)
}

func paxdArtifactTagList(tags []string) string {
	return strings.Join(tags, ",")
}

func (s *PostgresStore) RegisterAgent(
	ctx context.Context,
	owner User,
	req RegisterAgentRequest,
	apiKeyHash string,
) (Agent, error) {
	agentID, err := newSecret("agent")
	if err != nil {
		return Agent{}, err
	}
	now := s.now().UTC()
	metadata := nullRaw(req.Metadata)

	row := s.db.QueryRowContext(ctx, `
		INSERT INTO agents (
			agent_id, node_id, owner_user_id, name, hostname, agent_type, machine_type, os,
			hermes_version, api_endpoint, api_key_hash, status, last_heartbeat, registered_at, metadata
		)
		VALUES ($1,NULL,$2,$3,$4,$5,$6,$7,$8,$9,$10,'online',$11,$12,$13)
		RETURNING `+agentSelectColumns+`
	`, agentID, owner.UserID, defaultAgentName(req), req.Hostname, defaultAgentType(req), req.MachineType, req.OS,
		req.HermesVersion, defaultAPIEndpoint(req.APIEndpoint), apiKeyHash, now, now, metadata)

	return scanAgent(row)
}

func (s *PostgresStore) NextAgentACPRequestID(
	ctx context.Context,
	agentID string,
) (int64, error) {
	var requestID int64
	err := s.db.QueryRowContext(ctx, `
		UPDATE agents
		SET next_acp_request_id = next_acp_request_id + 1
		WHERE agent_id = $1
		RETURNING next_acp_request_id
	`, agentID).Scan(&requestID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	return requestID, err
}

func (s *PostgresStore) RegisterNode(
	ctx context.Context,
	owner User,
	req RegisterNodeRequest,
	apiKeyHash string,
) (Node, error) {
	nodeID, err := newSecret("node")
	if err != nil {
		return Node{}, err
	}
	now := s.now().UTC()
	osValue := defaultOS(req.OS)
	apiEndpoint := defaultAPIEndpoint(req.APIEndpoint)
	status := "online"
	row := dbmodel.Node{
		NodeID:        nodeID,
		OwnerUserID:   owner.UserID,
		Kind:          "paxd",
		Name:          defaultNodeName(req),
		Description:   req.Description,
		Hostname:      req.Hostname,
		MachineType:   req.MachineType,
		Os:            &osValue,
		Arch:          req.Arch,
		PaxdVersion:   req.PaxdVersion,
		APIEndpoint:   &apiEndpoint,
		APIKeyHash:    apiKeyHash,
		Status:        &status,
		LastHeartbeat: &now,
		RegisteredAt:  &now,
		UserMetadata:  rawJSONPtr(req.UserMetadata),
		Metadata:      rawJSONPtr(req.Metadata),
	}
	if err := s.q.Node.WithContext(ctx).Create(&row); err != nil {
		return Node{}, err
	}
	return nodeFromModel(&row), nil
}

func (s *PostgresStore) CreateNodeRegistrationSession(
	ctx context.Context,
	session NodeRegistrationSession,
) error {
	status := session.Status
	requestedOS := defaultOS(session.Request.OS)
	requestedAPIEndpoint := defaultAPIEndpoint(session.Request.APIEndpoint)
	row := dbmodel.NodeRegistrationSession{
		RegistrationID:       session.RegistrationID,
		PairCode:             session.PairCode,
		PollTokenHash:        session.PollTokenHash,
		Status:               &status,
		RequestedName:        session.Request.Name,
		RequestedHostname:    session.Request.Hostname,
		RequestedMachineType: session.Request.MachineType,
		RequestedOs:          &requestedOS,
		RequestedArch:        session.Request.Arch,
		RequestedPaxdVersion: session.Request.PaxdVersion,
		RequestedAPIEndpoint: &requestedAPIEndpoint,
		RequestedMetadata:    rawJSONPtr(session.Request.Metadata),
		RequestIP:            session.RequestIP,
		RequestCity:          session.RequestCity,
		RequestCountry:       session.RequestCountry,
		ExpiresAt:            session.ExpiresAt,
		CreatedAt:            &session.CreatedAt,
	}
	if err := s.q.NodeRegistrationSession.WithContext(ctx).Create(&row); err != nil {
		if isUniqueViolation(err) {
			return ErrConflict
		}
		return err
	}
	return nil
}

func (s *PostgresStore) GetNodeRegistrationSession(
	ctx context.Context,
	pairCode string,
) (NodeRegistrationSession, error) {
	session, err := scanNodeRegistrationSession(s.db.QueryRowContext(ctx, `
		SELECT registration_id, pair_code, poll_token_hash, status, COALESCE(owner_user_id, ''),
			COALESCE(node_id, ''), requested_name, requested_hostname, requested_machine_type,
			requested_os, requested_arch, requested_paxd_version, requested_api_endpoint,
			COALESCE(requested_metadata, '{}'::jsonb), COALESCE(request_ip, ''),
			COALESCE(request_city, ''), COALESCE(request_country, ''), expires_at, created_at,
			approved_at, consumed_at
		FROM node_registration_sessions
		WHERE pair_code = $1
	`, pairCode))
	if err != nil {
		return NodeRegistrationSession{}, err
	}
	if session.Status == domain.NodeRegistrationStatusPending &&
		!session.ExpiresAt.After(s.now().UTC()) {
		session.Status = domain.NodeRegistrationStatusExpired
	}
	return session, nil
}

func (s *PostgresStore) DeleteStaleNodeRegistrationSessions(
	ctx context.Context,
	cutoff time.Time,
) error {
	sessions := s.q.NodeRegistrationSession
	_, err := sessions.WithContext(ctx).
		Where(sessions.ExpiresAt.Lte(cutoff)).
		Or(sessions.Status.In(
			domain.NodeRegistrationStatusConsumed,
			domain.NodeRegistrationStatusDenied,
			domain.NodeRegistrationStatusExpired,
		)).
		Delete()
	return err
}

func (s *PostgresStore) ApproveNodeRegistrationSession(
	ctx context.Context,
	principal UserPrincipal,
	pairCode string,
) (NodeRegistrationSession, error) {
	var session NodeRegistrationSession
	err := s.gormDB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		qtx := query.Use(tx)
		sessions := qtx.NodeRegistrationSession
		row, err := sessions.WithContext(ctx).
			Clauses(clause.Locking{Strength: "UPDATE"}).
			Where(sessions.PairCode.Eq(pairCode)).
			First()
		if err != nil {
			return mapGormError(err)
		}
		session = nodeRegistrationSessionFromModel(row)
		if session.Status != domain.NodeRegistrationStatusPending {
			return ErrConflict
		}
		now := s.now().UTC()
		if !session.ExpiresAt.After(now) {
			_, _ = sessions.WithContext(ctx).
				Where(sessions.RegistrationID.Eq(session.RegistrationID)).
				Update(sessions.Status, domain.NodeRegistrationStatusExpired)
			return ErrUnauthorized
		}
		info, err := sessions.WithContext(ctx).
			Where(sessions.RegistrationID.Eq(session.RegistrationID)).
			UpdateSimple(
				sessions.Status.Value(domain.NodeRegistrationStatusApproved),
				sessions.OwnerUserID.Value(principal.User.UserID),
				sessions.ApprovedAt.Value(now),
			)
		if err != nil {
			return err
		}
		if info.RowsAffected == 0 {
			return ErrNotFound
		}
		row, err = sessions.WithContext(ctx).
			Where(sessions.RegistrationID.Eq(session.RegistrationID)).
			First()
		if err != nil {
			return mapGormError(err)
		}
		session = nodeRegistrationSessionFromModel(row)
		return nil
	})
	if err != nil {
		return NodeRegistrationSession{}, err
	}
	return session, nil
}

func (s *PostgresStore) PollNodeRegistrationSession(
	ctx context.Context,
	registrationID string,
	pollTokenHash string,
) (NodeRegistrationSession, error) {
	sessions := s.q.NodeRegistrationSession
	row, err := sessions.WithContext(ctx).
		Where(
			sessions.RegistrationID.Eq(registrationID),
			sessions.PollTokenHash.Eq(pollTokenHash),
		).
		First()
	if err != nil {
		if errors.Is(mapGormError(err), ErrNotFound) {
			return NodeRegistrationSession{}, ErrUnauthorized
		}
		return NodeRegistrationSession{}, err
	}
	session := nodeRegistrationSessionFromModel(row)
	if session.Status == domain.NodeRegistrationStatusPending &&
		!session.ExpiresAt.After(s.now().UTC()) {
		session.Status = domain.NodeRegistrationStatusExpired
		_, _ = sessions.WithContext(ctx).
			Where(sessions.RegistrationID.Eq(session.RegistrationID)).
			Update(sessions.Status, domain.NodeRegistrationStatusExpired)
	}
	return session, nil
}

func (s *PostgresStore) ConsumeNodeRegistrationSession(
	ctx context.Context,
	registrationID string,
	pollTokenHash string,
	apiKeyHash string,
) (Node, error) {
	var node Node
	err := s.gormDB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		qtx := query.Use(tx)
		sessions := qtx.NodeRegistrationSession
		row, err := sessions.WithContext(ctx).
			Clauses(clause.Locking{Strength: "UPDATE"}).
			Where(
				sessions.RegistrationID.Eq(registrationID),
				sessions.PollTokenHash.Eq(pollTokenHash),
			).
			First()
		if err != nil {
			if errors.Is(mapGormError(err), ErrNotFound) {
				return ErrUnauthorized
			}
			return err
		}
		session := nodeRegistrationSessionFromModel(row)
		if session.Status != domain.NodeRegistrationStatusApproved || session.OwnerUserID == "" {
			return ErrConflict
		}
		now := s.now().UTC()
		if !session.ExpiresAt.After(now) {
			_, _ = sessions.WithContext(ctx).
				Where(sessions.RegistrationID.Eq(session.RegistrationID)).
				Update(sessions.Status, domain.NodeRegistrationStatusExpired)
			return ErrUnauthorized
		}
		nodeID, err := newSecret("node")
		if err != nil {
			return err
		}
		osValue := defaultOS(session.Request.OS)
		apiEndpoint := defaultAPIEndpoint(session.Request.APIEndpoint)
		status := "online"
		nodeRow := dbmodel.Node{
			NodeID:        nodeID,
			OwnerUserID:   session.OwnerUserID,
			Kind:          "paxd",
			Name:          defaultNodeName(session.Request),
			Hostname:      session.Request.Hostname,
			MachineType:   session.Request.MachineType,
			Os:            &osValue,
			Arch:          session.Request.Arch,
			PaxdVersion:   session.Request.PaxdVersion,
			APIEndpoint:   &apiEndpoint,
			APIKeyHash:    apiKeyHash,
			Status:        &status,
			LastHeartbeat: &now,
			RegisteredAt:  &now,
			Metadata:      rawJSONPtr(session.Request.Metadata),
		}
		if err := qtx.Node.WithContext(ctx).Create(&nodeRow); err != nil {
			return err
		}
		info, err := sessions.WithContext(ctx).
			Where(sessions.RegistrationID.Eq(session.RegistrationID)).
			UpdateSimple(
				sessions.Status.Value(domain.NodeRegistrationStatusConsumed),
				sessions.NodeID.Value(nodeRow.NodeID),
				sessions.ConsumedAt.Value(now),
			)
		if err != nil {
			return err
		}
		if info.RowsAffected == 0 {
			return ErrNotFound
		}
		node = nodeFromModel(&nodeRow)
		return nil
	})
	if err != nil {
		return Node{}, err
	}
	return node, nil
}

func (s *PostgresStore) CreatePaxlDeviceLoginSession(
	ctx context.Context,
	session PaxlDeviceLoginSession,
) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO paxl_device_login_sessions (
			login_id, user_code, poll_token_hash, status, client_name, expires_at, created_at
		)
		VALUES ($1,$2,$3,$4,$5,$6,$7)
	`, session.LoginID, session.UserCode, session.PollTokenHash, session.Status,
		session.ClientName, session.ExpiresAt, session.CreatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return ErrConflict
		}
		return err
	}
	return nil
}

func (s *PostgresStore) DeleteStalePaxlDeviceLoginSessions(
	ctx context.Context,
	cutoff time.Time,
) error {
	_, err := s.db.ExecContext(ctx, `
		DELETE FROM paxl_device_login_sessions
		WHERE expires_at <= $1 OR status IN ($2, $3)
	`, cutoff, domain.PaxlDeviceLoginStatusConsumed, domain.PaxlDeviceLoginStatusExpired)
	return err
}

func (s *PostgresStore) ApprovePaxlDeviceLoginSession(
	ctx context.Context,
	principal UserPrincipal,
	userCode string,
	userAPIKey UserAPIKey,
	apiKey string,
) (PaxlDeviceLoginSession, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return PaxlDeviceLoginSession{}, err
	}
	defer func() { _ = tx.Rollback() }()
	session, err := scanPaxlDeviceLoginSession(tx.QueryRowContext(ctx, paxlDeviceLoginSelectSQL+`
		WHERE user_code = $1
		FOR UPDATE
	`, userCode))
	if err != nil {
		return PaxlDeviceLoginSession{}, err
	}
	if session.Status != domain.PaxlDeviceLoginStatusPending {
		return PaxlDeviceLoginSession{}, ErrConflict
	}
	now := s.now().UTC()
	if !session.ExpiresAt.After(now) {
		_, _ = tx.ExecContext(ctx, `
			UPDATE paxl_device_login_sessions SET status = $2 WHERE login_id = $1
		`, session.LoginID, domain.PaxlDeviceLoginStatusExpired)
		return PaxlDeviceLoginSession{}, ErrUnauthorized
	}
	nodeID, err := newSecret("node")
	if err != nil {
		return PaxlDeviceLoginSession{}, err
	}
	nodeName := firstNonEmpty(session.ClientName, "paxl")
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO nodes (
			node_id, owner_user_id, kind, name, hostname, machine_type, os, arch,
			paxd_version, api_endpoint, api_key_hash, status, registered_at, metadata
		)
		VALUES ($1,$2,'paxl',$3,$3,'','unknown','','','',$4,'offline',$5,$6)
	`, nodeID, principal.User.UserID, nodeName, "paxl-device:"+nodeID, now,
		nullRaw(json.RawMessage(`{"kind":"paxl"}`))); err != nil {
		return PaxlDeviceLoginSession{}, err
	}
	_, err = tx.ExecContext(ctx, `
		UPDATE paxl_device_login_sessions
		SET status = $2, owner_user_id = $3, user_api_key_id = $4, api_key = $5, node_id = $6, approved_at = $7
		WHERE login_id = $1
	`, session.LoginID, domain.PaxlDeviceLoginStatusApproved, principal.User.UserID,
		userAPIKey.KeyID, apiKey, nodeID, now)
	if err != nil {
		return PaxlDeviceLoginSession{}, err
	}
	session, err = scanPaxlDeviceLoginSession(tx.QueryRowContext(ctx, paxlDeviceLoginSelectSQL+`
		WHERE login_id = $1
	`, session.LoginID))
	if err != nil {
		return PaxlDeviceLoginSession{}, err
	}
	if err := tx.Commit(); err != nil {
		return PaxlDeviceLoginSession{}, err
	}
	return session, nil
}

func (s *PostgresStore) PollPaxlDeviceLoginSession(
	ctx context.Context,
	loginID string,
	pollTokenHash string,
) (PaxlDeviceLoginSession, error) {
	session, err := scanPaxlDeviceLoginSession(s.db.QueryRowContext(ctx, paxlDeviceLoginSelectSQL+`
		WHERE login_id = $1 AND poll_token_hash = $2
	`, loginID, pollTokenHash))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return PaxlDeviceLoginSession{}, ErrUnauthorized
		}
		return PaxlDeviceLoginSession{}, err
	}
	if session.Status == domain.PaxlDeviceLoginStatusPending &&
		!session.ExpiresAt.After(s.now().UTC()) {
		session.Status = domain.PaxlDeviceLoginStatusExpired
		_, _ = s.db.ExecContext(ctx, `
			UPDATE paxl_device_login_sessions SET status = $2 WHERE login_id = $1
		`, loginID, domain.PaxlDeviceLoginStatusExpired)
	}
	return session, nil
}

func (s *PostgresStore) ConsumePaxlDeviceLoginSession(
	ctx context.Context,
	loginID string,
	pollTokenHash string,
) (PaxlDeviceLoginSession, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return PaxlDeviceLoginSession{}, err
	}
	defer func() { _ = tx.Rollback() }()
	session, err := scanPaxlDeviceLoginSession(tx.QueryRowContext(ctx, paxlDeviceLoginSelectSQL+`
		WHERE login_id = $1 AND poll_token_hash = $2
		FOR UPDATE
	`, loginID, pollTokenHash))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return PaxlDeviceLoginSession{}, ErrUnauthorized
		}
		return PaxlDeviceLoginSession{}, err
	}
	if session.Status != domain.PaxlDeviceLoginStatusApproved ||
		session.OwnerUserID == "" || session.APIKey == "" {
		return PaxlDeviceLoginSession{}, ErrConflict
	}
	now := s.now().UTC()
	if !session.ExpiresAt.After(now) {
		_, _ = tx.ExecContext(ctx, `
			UPDATE paxl_device_login_sessions SET status = $2 WHERE login_id = $1
		`, session.LoginID, domain.PaxlDeviceLoginStatusExpired)
		return PaxlDeviceLoginSession{}, ErrUnauthorized
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE paxl_device_login_sessions
		SET status = $2, api_key = NULL, consumed_at = $3
		WHERE login_id = $1
	`, session.LoginID, domain.PaxlDeviceLoginStatusConsumed, now); err != nil {
		return PaxlDeviceLoginSession{}, err
	}
	if err := tx.Commit(); err != nil {
		return PaxlDeviceLoginSession{}, err
	}
	return session, nil
}

const paxlDeviceLoginSelectSQL = `
	SELECT login_id, user_code, poll_token_hash, status, client_name,
		COALESCE(owner_user_id, ''), COALESCE(user_api_key_id, ''), COALESCE(node_id, ''),
		COALESCE(api_key, ''),
		expires_at, created_at, approved_at, consumed_at
	FROM paxl_device_login_sessions
`

const nodeSelectColumns = `
	node_id, owner_user_id, COALESCE(kind, 'paxd'), name, description, hostname,
	machine_type, os, arch, paxd_version, api_endpoint, computed_status(last_heartbeat),
	last_heartbeat, registered_at, COALESCE(user_metadata, '{}'::jsonb),
	COALESCE(metadata, '{}'::jsonb)
`

const agentSelectColumns = `
	agent_id, COALESCE(node_id, ''), owner_user_id, name, description,
	COALESCE(card, '{}'::jsonb), hostname, agent_type, machine_type, os,
	hermes_version, api_endpoint, status, computed_status(last_heartbeat),
	last_heartbeat, registered_at, COALESCE(user_metadata, '{}'::jsonb),
	COALESCE(metadata, '{}'::jsonb)
`

func (s *PostgresStore) AuthenticateNode(ctx context.Context, apiKeyHash string) (Node, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT `+nodeSelectColumns+`
		FROM nodes
		WHERE api_key_hash = $1
			AND deleted_at IS NULL
	`, apiKeyHash)
	return scanNode(row)
}

func (s *PostgresStore) AuthenticateAgent(ctx context.Context, apiKeyHash string) (Agent, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT `+agentSelectColumns+`
		FROM agents
		WHERE api_key_hash = $1
			AND deleted_at IS NULL
	`, apiKeyHash)
	return scanAgent(row)
}

func (s *PostgresStore) UpsertNodeStatus(
	ctx context.Context,
	node Node,
	report NodeStatusReport,
) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if report.RuntimeFence != "" {
		var currentFence string
		err = tx.QueryRowContext(ctx, `
			SELECT connection_fence
			FROM node_runtime_fences
			WHERE node_id = $1
			FOR UPDATE
		`, node.NodeID).Scan(&currentFence)
		if errors.Is(err, sql.ErrNoRows) || err == nil && currentFence != report.RuntimeFence {
			return ErrConflict
		}
		if err != nil {
			return err
		}
	}
	now := s.now().UTC()
	if report.MachineType != "" {
		node.MachineType = report.MachineType
	}
	if report.OS != "" {
		node.OS = report.OS
	}
	if report.Arch != "" {
		node.Arch = report.Arch
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE nodes
		SET status = 'online',
			last_heartbeat = $2,
			hostname = COALESCE(NULLIF($3, ''), hostname),
			metadata = COALESCE($4, metadata),
			machine_type = COALESCE(NULLIF($5, ''), machine_type),
			os = COALESCE(NULLIF($6, ''), os),
			arch = COALESCE(NULLIF($7, ''), arch)
		WHERE node_id = $1
	`, node.NodeID, now, report.Hostname, nullRaw(report.Metadata), report.MachineType, report.OS, report.Arch); err != nil {
		return err
	}
	for _, input := range report.Agents {
		agentID := input.AgentID
		if agentID == "" {
			agentID, err = newSecret("agent")
			if err != nil {
				return err
			}
		}
		result, err := tx.ExecContext(
			ctx,
			`
			INSERT INTO agents (
				agent_id, node_id, owner_user_id, name, description, card, hostname, agent_type,
				machine_type, os, hermes_version, api_endpoint, api_key_hash, status,
				last_heartbeat, registered_at, metadata
			)
			VALUES ($1,$2,$3,$4,$5,COALESCE($6, '{}'::jsonb),$7,$8,$9,$10,$11,$12,$13,$14,$15,$15,$16)
			ON CONFLICT (agent_id) DO UPDATE SET
				name = COALESCE(NULLIF(EXCLUDED.name, ''), agents.name),
				description = COALESCE(NULLIF(EXCLUDED.description, ''), agents.description),
				card = COALESCE(EXCLUDED.card, agents.card),
				agent_type = COALESCE(NULLIF(EXCLUDED.agent_type, ''), agents.agent_type),
				status = EXCLUDED.status,
				last_heartbeat = EXCLUDED.last_heartbeat,
				metadata = COALESCE(EXCLUDED.metadata, agents.metadata)
			WHERE agents.node_id = EXCLUDED.node_id
				AND agents.owner_user_id = EXCLUDED.owner_user_id
		`,
			agentID,
			node.NodeID,
			node.OwnerUserID,
			firstNonEmpty(input.Name, "agent"),
			input.Description,
			nullRaw(input.Card),
			node.Hostname,
			firstNonEmpty(input.AgentType, "hermes"),
			node.MachineType,
			node.OS,
			node.PaxdVersion,
			node.APIEndpoint,
			"node:"+node.NodeID+":"+agentID,
			reportedAgentStatus(input),
			now,
			nullRaw(input.Metadata),
		)
		if err != nil {
			return err
		}
		adopted, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if adopted == 0 {
			// An existing agent with this ID belongs to a different node or
			// owner. Status reports must never re-home agents.
			continue
		}
		if input.RuntimeIdentity != nil {
			identity := *input.RuntimeIdentity
			identity.AgentID = agentID
			writeIdentity := upsertAgentRuntimeIdentity
			if report.RuntimeFence != "" {
				writeIdentity = replaceAgentRuntimeIdentity
			}
			if err := writeIdentity(ctx, tx, identity); err != nil {
				return err
			}
		}
		for _, session := range input.Sessions {
			if session.SessionID == "" {
				continue
			}
			session, err = s.normalizeReportedSessionInput(ctx, tx, agentID, session)
			if err != nil {
				return err
			}
			if err := upsertSessionTx(ctx, tx, node.NodeID, agentID, session, "", now); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

func (s *PostgresStore) UpsertAgentStatus(ctx context.Context, report AgentStatusReport) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	now := s.now().UTC()
	var agentNodeID sql.NullString
	err = tx.QueryRowContext(ctx, `
		UPDATE agents
		SET status = 'online', last_heartbeat = $2, hostname = COALESCE(NULLIF($3, ''), hostname)
		WHERE agent_id = $1
			AND deleted_at IS NULL
		RETURNING node_id
	`, report.AgentID, now, report.Hostname).Scan(&agentNodeID)
	if err == sql.ErrNoRows {
		return ErrNotFound
	}
	if err != nil {
		return err
	}

	for _, input := range report.Sessions {
		if input.SessionID == "" {
			continue
		}
		input, err = s.normalizeReportedSessionInput(ctx, tx, report.AgentID, input)
		if err != nil {
			return err
		}
		if err := upsertSessionTx(
			ctx,
			tx,
			agentNodeID.String,
			report.AgentID,
			input,
			"",
			now,
		); err != nil {
			return err
		}
	}

	return tx.Commit()
}

func (s *PostgresStore) UpsertAgentSessions(
	ctx context.Context,
	node Node,
	agentID string,
	sessions []SessionStatusInput,
) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	var exists bool
	if err := tx.QueryRowContext(ctx, `
		SELECT EXISTS(SELECT 1 FROM agents WHERE agent_id = $1 AND node_id = $2 AND deleted_at IS NULL)
	`, agentID, node.NodeID).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return ErrNotFound
	}

	now := s.now().UTC()
	for _, input := range sessions {
		if input.SessionID == "" {
			return ErrConflict
		}
		input, err = s.normalizeReportedSessionInput(ctx, tx, agentID, input)
		if err != nil {
			return err
		}
		if err := upsertSessionTx(ctx, tx, node.NodeID, agentID, input, "", now); err != nil {
			return err
		}
	}

	return tx.Commit()
}

func (s *PostgresStore) ListNodes(ctx context.Context, principal UserPrincipal) ([]Node, error) {
	query := `
		SELECT ` + nodeSelectColumns + `
		FROM nodes
		WHERE owner_user_id = $1
			AND deleted_at IS NULL
	`
	args := []any{principal.User.UserID}
	query += ` ORDER BY registered_at ASC`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return scanNodes(rows)
}

func (s *PostgresStore) GetNode(
	ctx context.Context,
	principal UserPrincipal,
	nodeID string,
) (Node, error) {
	query := `
		SELECT ` + nodeSelectColumns + `
		FROM nodes
		WHERE node_id = $1
			AND owner_user_id = $2
			AND deleted_at IS NULL
	`
	args := []any{nodeID, principal.User.UserID}
	return scanNode(s.db.QueryRowContext(ctx, query, args...))
}

func (s *PostgresStore) UpdateNode(
	ctx context.Context,
	principal UserPrincipal,
	req UpdateNodeRequest,
) (Node, error) {
	row := s.db.QueryRowContext(ctx, `
		UPDATE nodes
		SET name = COALESCE(NULLIF($3, ''), name),
			description = COALESCE(NULLIF($4, ''), description),
			user_metadata = COALESCE($5, user_metadata)
		WHERE node_id = $1
			AND owner_user_id = $2
			AND deleted_at IS NULL
		RETURNING `+nodeSelectColumns+`
	`, req.NodeID, principal.User.UserID, req.Name, req.Description, nullRaw(req.UserMetadata))
	return scanNode(row)
}

func (s *PostgresStore) DeleteNode(
	ctx context.Context,
	principal UserPrincipal,
	req DeleteNodeRequest,
) (Node, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Node{}, err
	}
	defer func() { _ = tx.Rollback() }()

	node, err := scanNode(tx.QueryRowContext(ctx, `
		SELECT `+nodeSelectColumns+`
		FROM nodes
		WHERE node_id = $1
			AND owner_user_id = $2
			AND deleted_at IS NULL
		FOR UPDATE
	`, req.NodeID, principal.User.UserID))
	if err != nil {
		return Node{}, err
	}

	now := s.now().UTC()
	tombstoneSuffix := strconv.FormatInt(now.UnixNano(), 10)
	if _, err := tx.ExecContext(ctx, `
		UPDATE agents
		SET deleted_at = COALESCE(deleted_at, $3),
			deleted_by_user_id = COALESCE(deleted_by_user_id, $2),
			status = 'deleted',
			last_heartbeat = NULL,
			api_key_hash = 'revoked:agent:' || agent_id || ':' || $4
		WHERE node_id = $1
			AND deleted_at IS NULL
	`, req.NodeID, principal.User.UserID, now, tombstoneSuffix); err != nil {
		return Node{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE nodes
		SET deleted_at = $3,
			deleted_by_user_id = $2,
			status = 'deleted',
			last_heartbeat = NULL,
			api_key_hash = 'revoked:node:' || node_id || ':' || $4
		WHERE node_id = $1
			AND owner_user_id = $2
			AND deleted_at IS NULL
	`, req.NodeID, principal.User.UserID, now, tombstoneSuffix); err != nil {
		return Node{}, err
	}
	if err := tx.Commit(); err != nil {
		return Node{}, err
	}
	return node, nil
}

func (s *PostgresStore) GetNodeAgent(
	ctx context.Context,
	nodeID string,
	agentID string,
) (Agent, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT `+agentSelectColumns+`
		FROM agents
		WHERE node_id = $1
			AND agent_id = $2
			AND deleted_at IS NULL
	`, nodeID, agentID)
	return scanAgent(row)
}

func (s *PostgresStore) ListNodeAgents(
	ctx context.Context,
	principal UserPrincipal,
	nodeID string,
) ([]Agent, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+agentSelectColumns+`
		FROM agents
		WHERE node_id = $1
			AND deleted_at IS NULL
			AND (
				owner_user_id = $2
				OR `+teamAgentAccessSQL("agents.agent_id", "$2")+`
			)
		ORDER BY registered_at ASC
	`, nodeID, principal.User.UserID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	agents, err := scanAgents(rows)
	if err != nil {
		return nil, err
	}
	if len(agents) == 0 {
		if _, err := s.GetNode(ctx, principal, nodeID); err != nil {
			return nil, err
		}
	}
	return agents, nil
}

func (s *PostgresStore) CreateNodeAgent(
	ctx context.Context,
	principal UserPrincipal,
	req CreateAgentRequest,
) (Agent, MailboxMessage, error) {
	node, err := s.GetNode(ctx, principal, req.NodeID)
	if err != nil {
		return Agent{}, MailboxMessage{}, err
	}
	agentID, err := newSecret("agent")
	if err != nil {
		return Agent{}, MailboxMessage{}, err
	}
	now := s.now().UTC()
	agent, err := scanAgent(s.db.QueryRowContext(ctx, `
		INSERT INTO agents (
			agent_id, node_id, owner_user_id, name, description, card, hostname, agent_type,
			machine_type, os, hermes_version, api_endpoint, api_key_hash, status,
			last_heartbeat, registered_at, user_metadata, metadata
		)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,'pending',$14,$14,$15,$16)
		RETURNING `+agentSelectColumns+`
	`, agentID, node.NodeID, node.OwnerUserID, firstNonEmpty(req.Name, "agent"), req.Description,
		jsonDefault(req.Card, "{}"), node.Hostname, firstNonEmpty(req.AgentType, "hermes"),
		node.MachineType, node.OS, node.PaxdVersion, node.APIEndpoint,
		"node:"+node.NodeID+":"+agentID, now, jsonDefault(req.UserMetadata, "{}"),
		nullRaw(req.Metadata)))
	if err != nil {
		return Agent{}, MailboxMessage{}, err
	}
	msg, err := s.insertMailbox(
		ctx,
		principal.User.UserID,
		node.OwnerUserID,
		node.NodeID,
		agent.AgentID,
		"",
		"bootstrap",
		"command",
		req.Metadata,
		"pending",
		"user_to_node",
		now,
	)
	if err != nil {
		return Agent{}, MailboxMessage{}, err
	}
	return agent, msg, nil
}

func (s *PostgresStore) UpdateNodeAgent(
	ctx context.Context,
	principal UserPrincipal,
	req UpdateAgentProfileRequest,
) (Agent, error) {
	row := s.db.QueryRowContext(ctx, `
		UPDATE agents
		SET name = COALESCE(NULLIF($4, ''), name),
			description = COALESCE(NULLIF($5, ''), description),
			card = COALESCE($6, card),
			user_metadata = COALESCE($7, user_metadata)
		WHERE node_id = $1
			AND agent_id = $2
			AND owner_user_id = $3
			AND deleted_at IS NULL
		RETURNING `+agentSelectColumns+`
	`, req.NodeID, req.AgentID, principal.User.UserID, req.Name, req.Description,
		nullRaw(req.Card), nullRaw(req.UserMetadata))
	return scanAgent(row)
}

func (s *PostgresStore) DeleteNodeAgent(
	ctx context.Context,
	principal UserPrincipal,
	req DeleteAgentRequest,
) (Agent, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Agent{}, err
	}
	defer func() { _ = tx.Rollback() }()

	agent, err := scanAgent(tx.QueryRowContext(ctx, `
		SELECT `+agentSelectColumns+`
		FROM agents
		WHERE node_id = $1
			AND agent_id = $2
			AND owner_user_id = $3
			AND deleted_at IS NULL
		FOR UPDATE
	`, req.NodeID, req.AgentID, principal.User.UserID))
	if err != nil {
		return Agent{}, err
	}

	now := s.now().UTC()
	if _, err := tx.ExecContext(ctx, `
		UPDATE agents
		SET deleted_at = $4,
			deleted_by_user_id = $3,
			status = 'deleted',
			last_heartbeat = NULL,
			api_key_hash = 'revoked:agent:' || agent_id || ':' || $5
		WHERE node_id = $1
			AND agent_id = $2
			AND owner_user_id = $3
			AND deleted_at IS NULL
	`, req.NodeID, req.AgentID, principal.User.UserID, now, strconv.FormatInt(now.UnixNano(), 10)); err != nil {
		return Agent{}, err
	}
	if err := tx.Commit(); err != nil {
		return Agent{}, err
	}
	return agent, nil
}

// validateSessionReferences rejects session reference fields that point at
// resources the principal cannot legitimately attach: conversations they are
// not a member of, or profiles/representative agents owned by someone else.
func (s *PostgresStore) validateSessionReferences(
	ctx context.Context,
	principal UserPrincipal,
	req CreateSessionRequest,
) error {
	if req.ConversationID != "" {
		member, err := s.hasActiveConversationMembership(
			ctx,
			req.ConversationID,
			principal.User.UserID,
		)
		if err != nil {
			return err
		}
		if !member {
			return ErrNotFound
		}
	}
	if req.ProfileID != "" {
		var profileOwner string
		err := s.db.QueryRowContext(ctx, `
			SELECT owner_id FROM agent_profiles WHERE profile_id = $1
		`, req.ProfileID).Scan(&profileOwner)
		if err != nil {
			return mapSQLError(err)
		}
		if profileOwner != principal.User.UserID {
			return ErrNotFound
		}
	}
	if req.RepresentativeAgentID != "" {
		var repOwner string
		err := s.db.QueryRowContext(ctx, `
			SELECT a.owner_user_id
			FROM representative_agents r
			JOIN agents a ON a.agent_id = r.runtime_agent_id
			WHERE r.representative_agent_id = $1 AND r.status = $2
		`, req.RepresentativeAgentID, domain.ConversationStatusActive).Scan(&repOwner)
		if err != nil {
			return mapSQLError(err)
		}
		if repOwner != principal.User.UserID {
			return ErrNotFound
		}
	}
	return nil
}

func (s *PostgresStore) CreateNodeAgentSession(
	ctx context.Context,
	principal UserPrincipal,
	req CreateSessionRequest,
) (AgentSession, error) {
	if req.SessionID == "" {
		generated, err := newSecret("sess")
		if err != nil {
			return AgentSession{}, err
		}
		req.SessionID = generated
	}
	var ownerUserID string
	if err := s.db.QueryRowContext(ctx, `
		SELECT owner_user_id FROM agents WHERE agent_id = $1 AND node_id = $2 AND deleted_at IS NULL
	`, req.AgentID, req.NodeID).Scan(&ownerUserID); err != nil {
		return AgentSession{}, mapSQLError(err)
	}
	if !canAccessOwner(principal, ownerUserID) {
		_, err := s.GetAgent(ctx, principal, req.AgentID)
		if err != nil {
			return AgentSession{}, ErrNotFound
		}
	}
	if req.NodeID == "" {
		return AgentSession{}, ErrNotFound
	}
	now := s.now().UTC()
	input := SessionStatusInput{
		SessionID:      req.SessionID,
		AgentType:      req.AgentType,
		NativeID:       req.NativeID,
		SessionName:    req.SessionName,
		WorkspaceRoots: req.WorkspaceRoots,
		Source:         req.Source,
		Status:         "idle",
	}
	if err := upsertSessionTx(
		ctx,
		dbExecer{s.db},
		req.NodeID,
		req.AgentID,
		input,
		req.PrimaryProjectID,
		now,
	); err != nil {
		return AgentSession{}, err
	}
	if err := s.validateSessionReferences(ctx, principal, req); err != nil {
		return AgentSession{}, err
	}
	createdBy := strings.TrimSpace(req.CreatedByUserID)
	if createdBy != principal.User.UserID {
		createdByMember := false
		if req.ConversationID != "" {
			member, err := s.hasActiveConversationMembership(ctx, req.ConversationID, createdBy)
			if err != nil {
				return AgentSession{}, err
			}
			createdByMember = member
		}
		if !createdByMember {
			createdBy = principal.User.UserID
		}
	}
	if req.ConversationID != "" || req.ProfileID != "" ||
		req.RepresentativeAgentID != "" || createdBy != "" {
		if _, err := s.db.ExecContext(ctx, `
			UPDATE agent_sessions
			SET conversation_id = COALESCE(NULLIF($3,''), conversation_id),
				profile_id = COALESCE(NULLIF($4,''), profile_id),
				representative_agent_id = COALESCE(NULLIF($5,''), representative_agent_id),
				created_by_user_id = COALESCE(NULLIF(created_by_user_id, ''), $6)
			WHERE agent_id = $1 AND session_id = $2
		`, req.AgentID, req.SessionID, req.ConversationID, req.ProfileID,
			req.RepresentativeAgentID, createdBy); err != nil {
			return AgentSession{}, err
		}
	}
	if req.PaxConfig.CWD != "" || req.PaxConfig.ApprovalMode != "" ||
		req.PaxConfig.PermissionChoiceID != "" {
		config := req.PaxConfig
		config.ApprovalMode = normalizeSessionApprovalMode(config.ApprovalMode)
		if err := updateSessionPaxConfig(
			ctx,
			dbExecer{s.db},
			req.AgentID,
			req.SessionID,
			config,
			now,
		); err != nil {
			return AgentSession{}, err
		}
	}
	return scanSession(s.db.QueryRowContext(ctx, sessionSelectSQL+`
		WHERE agent_sessions.agent_id = $1 AND agent_sessions.session_id = $2
	`, req.AgentID, req.SessionID))
}

func (s *PostgresStore) UpdateNodeAgentSession(
	ctx context.Context,
	principal UserPrincipal,
	req UpdateSessionRequest,
) (AgentSession, error) {
	session, err := s.GetSession(ctx, principal, req.SessionID)
	if err != nil {
		return AgentSession{}, err
	}
	if session.AgentID != req.AgentID || (session.NodeID != "" && session.NodeID != req.NodeID) {
		return AgentSession{}, ErrNotFound
	}
	now := s.now().UTC()
	if req.SessionName != nil || req.UseReportedName {
		var customName any
		if req.SessionName != nil {
			customName = *req.SessionName
		}
		result, err := s.db.ExecContext(ctx, `
			UPDATE agent_sessions
			SET custom_session_name = $3, updated_at = $4
			WHERE agent_id = $1 AND session_id = $2
		`, req.AgentID, req.SessionID, customName, now)
		if err != nil {
			return AgentSession{}, err
		}
		if rows, err := result.RowsAffected(); err == nil && rows == 0 {
			return AgentSession{}, ErrNotFound
		}
	}
	if req.PaxConfig.ApprovalMode != "" {
		config := session.PaxConfig
		config.ApprovalMode = normalizeSessionApprovalMode(req.PaxConfig.ApprovalMode)
		config.PermissionChoiceID = req.PaxConfig.PermissionChoiceID
		if err := updateSessionPaxConfig(
			ctx,
			dbExecer{s.db},
			req.AgentID,
			req.SessionID,
			config,
			now,
		); err != nil {
			return AgentSession{}, err
		}
	}
	if req.Archived != nil {
		result, err := s.db.ExecContext(ctx, `
			UPDATE agent_sessions
			SET archived_at = CASE WHEN $3 THEN COALESCE(archived_at, $4) ELSE NULL END,
				updated_at = $4
			WHERE agent_id = $1 AND session_id = $2
		`, req.AgentID, req.SessionID, *req.Archived, now)
		if err != nil {
			return AgentSession{}, err
		}
		if rows, err := result.RowsAffected(); err == nil && rows == 0 {
			return AgentSession{}, ErrNotFound
		}
	}
	return scanSession(s.db.QueryRowContext(ctx, sessionSelectSQL+`
		WHERE agent_sessions.agent_id = $1 AND agent_sessions.session_id = $2
	`, req.AgentID, req.SessionID))
}

// LinkAgentSessionNativeID sets native_id on a session row only when it is not
// already populated, so it never clobbers a native id learned from a paxd
// status report. See the domain.Store interface for why A2A delivery needs it.
func (s *PostgresStore) LinkAgentSessionNativeID(
	ctx context.Context,
	agentID string,
	sessionID string,
	nativeID string,
) error {
	agentID = strings.TrimSpace(agentID)
	sessionID = strings.TrimSpace(sessionID)
	nativeID = strings.TrimSpace(nativeID)
	if agentID == "" || sessionID == "" || nativeID == "" {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE agent_sessions
		SET native_id = $3, updated_at = $4
		WHERE agent_id = $1 AND session_id = $2
			AND (native_id IS NULL OR native_id = '')
	`, agentID, sessionID, nativeID, s.now().UTC())
	return err
}

// SetSessionApprovalMode overwrites the legacy approval policy while preserving
// unrelated session config. A legacy override clears permission_choice_id so a
// native/manual selection cannot coexist with auto approval.
func (s *PostgresStore) SetSessionApprovalMode(
	ctx context.Context,
	agentID string,
	sessionID string,
	mode string,
) error {
	agentID = strings.TrimSpace(agentID)
	sessionID = strings.TrimSpace(sessionID)
	if agentID == "" || sessionID == "" {
		return nil
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE agent_sessions
		SET metadata = jsonb_set(
			COALESCE(metadata, '{}'::jsonb),
			'{pax_config}',
			(COALESCE(metadata->'pax_config', '{}'::jsonb) - 'permission_choice_id') ||
				jsonb_build_object('approval_mode', $3::text),
			true
		),
			updated_at = $4
		WHERE agent_id = $1 AND session_id = $2
	`, agentID, sessionID, normalizeSessionApprovalMode(mode), s.now().UTC())
	if err != nil {
		return err
	}
	if rows, err := result.RowsAffected(); err == nil && rows == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteProvisionalAgentSession removes an exact session/new binding that
// never advanced to its first prompt. Session-owned rows cascade from the
// agent_sessions foreign key.
func (s *PostgresStore) DeleteProvisionalAgentSession(
	ctx context.Context,
	agentID string,
	sessionID string,
) error {
	agentID = strings.TrimSpace(agentID)
	sessionID = strings.TrimSpace(sessionID)
	if agentID == "" || sessionID == "" {
		return ErrNotFound
	}
	result, err := s.db.ExecContext(ctx, `
		DELETE FROM agent_sessions
		WHERE agent_id = $1 AND session_id = $2
	`, agentID, sessionID)
	if err != nil {
		return err
	}
	if rows, err := result.RowsAffected(); err == nil && rows == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PostgresStore) ListAgents(ctx context.Context, principal UserPrincipal) ([]Agent, error) {
	query := `
		SELECT ` + agentSelectColumns + `
		FROM agents
		WHERE deleted_at IS NULL
			AND (
				owner_user_id = $1
				OR ` + teamAgentAccessSQL("agents.agent_id", "$1") + `
			)
	`
	args := []any{principal.User.UserID}
	query += ` ORDER BY registered_at ASC`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return scanAgents(rows)
}

func (s *PostgresStore) GetAgent(
	ctx context.Context,
	principal UserPrincipal,
	agentID string,
) (Agent, error) {
	query := `
		SELECT ` + agentSelectColumns + `
		FROM agents
		WHERE agent_id = $1
			AND deleted_at IS NULL
			AND (
				owner_user_id = $2
				OR ` + teamAgentAccessSQL("agents.agent_id", "$2") + `
			)
	`
	args := []any{agentID, principal.User.UserID}
	row := s.db.QueryRowContext(ctx, query, args...)
	return scanAgent(row)
}

func (s *PostgresStore) DeleteAgent(
	ctx context.Context,
	principal UserPrincipal,
	req DeleteAgentRequest,
) (Agent, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Agent{}, err
	}
	defer func() { _ = tx.Rollback() }()

	agent, err := scanAgent(tx.QueryRowContext(ctx, `
		SELECT `+agentSelectColumns+`
		FROM agents
		WHERE agent_id = $1
			AND owner_user_id = $2
			AND deleted_at IS NULL
		FOR UPDATE
	`, req.AgentID, principal.User.UserID))
	if err != nil {
		return Agent{}, err
	}

	now := s.now().UTC()
	if _, err := tx.ExecContext(ctx, `
		UPDATE agents
		SET deleted_at = $3,
			deleted_by_user_id = $2,
			status = 'deleted',
			last_heartbeat = NULL,
			api_key_hash = 'revoked:agent:' || agent_id || ':' || $4
		WHERE agent_id = $1
			AND owner_user_id = $2
			AND deleted_at IS NULL
	`, req.AgentID, principal.User.UserID, now, strconv.FormatInt(now.UnixNano(), 10)); err != nil {
		return Agent{}, err
	}
	if err := tx.Commit(); err != nil {
		return Agent{}, err
	}
	return agent, nil
}

func (s *PostgresStore) ListAgentSessions(
	ctx context.Context,
	principal UserPrincipal,
	agentID string,
) ([]AgentSession, error) {
	query := sessionSelectSQL + ` JOIN agents a ON a.agent_id = agent_sessions.agent_id WHERE agent_sessions.agent_id = $1 AND a.deleted_at IS NULL AND agent_sessions.archived_at IS NULL`
	query += ` AND (
		a.owner_user_id = $2
		OR ` + teamAgentAccessSQL("a.agent_id", "$2") + `
	)`
	args := []any{agentID, principal.User.UserID}
	query += ` ORDER BY COALESCE(agent_sessions.last_user_message_at, agent_sessions.last_message_at, agent_sessions.updated_at) DESC, agent_sessions.updated_at DESC`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return scanSessions(rows)
}

func (s *PostgresStore) ListSessions(
	ctx context.Context,
	principal UserPrincipal,
	filter domain.ListSessionsFilter,
) (domain.ListSessionsResult, error) {
	if filter.OwnerUserID != "" && !canAccessOwner(principal, filter.OwnerUserID) {
		return domain.ListSessionsResult{}, ErrNotFound
	}
	pageSize, pageNum := normalizeSessionPage(filter.PageSize, filter.PageNum)
	args := []any{principal.User.UserID}
	clauses := []string{
		"a.deleted_at IS NULL",
		`(
			a.owner_user_id = $1
			OR ` + teamAgentAccessSQL("a.agent_id", "$1") + `
		)`,
	}
	addFilter := func(clause string, value any) {
		args = append(args, value)
		clauses = append(clauses, clause+" $"+strconv.Itoa(len(args)))
	}
	addInFilter := func(column string, values []string) {
		if len(values) == 0 {
			return
		}
		placeholders := make([]string, 0, len(values))
		for _, value := range values {
			args = append(args, value)
			placeholders = append(placeholders, "$"+strconv.Itoa(len(args)))
		}
		clauses = append(clauses, column+" IN ("+strings.Join(placeholders, ",")+")")
	}
	if filter.OwnerUserID != "" {
		addFilter("a.owner_user_id =", filter.OwnerUserID)
	}
	addInFilter("agent_sessions.node_id", filter.NodeIDs)
	addInFilter("agent_sessions.agent_id", filter.AgentIDs)
	if filter.PrimaryProjectID != "" {
		addFilter("agent_sessions.primary_project_id =", filter.PrimaryProjectID)
	}
	if !filter.IncludeArchived {
		clauses = append(clauses, "agent_sessions.archived_at IS NULL")
	}
	where := " WHERE " + strings.Join(clauses, " AND ")
	var total int64
	if err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM agent_sessions
		JOIN agents a ON a.agent_id = agent_sessions.agent_id
	`+where, args...).Scan(&total); err != nil {
		return domain.ListSessionsResult{}, err
	}
	args = append(args, pageSize, (pageNum-1)*pageSize)
	query := sessionSelectSQL + `
		JOIN agents a ON a.agent_id = agent_sessions.agent_id
	` + where + `
		ORDER BY COALESCE(agent_sessions.last_user_message_at, agent_sessions.last_message_at, agent_sessions.updated_at) DESC,
			agent_sessions.updated_at DESC
		LIMIT $` + strconv.Itoa(len(args)-1) + ` OFFSET $` + strconv.Itoa(len(args))
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return domain.ListSessionsResult{}, err
	}
	defer func() { _ = rows.Close() }()
	sessions, err := scanSessions(rows)
	if err != nil {
		return domain.ListSessionsResult{}, err
	}
	return domain.ListSessionsResult{
		Sessions: sessions,
		Pagination: domain.Pagination{
			PageNum:    pageNum,
			PageSize:   pageSize,
			Total:      total,
			TotalPages: totalPages(total, pageSize),
		},
	}, nil
}

func (s *PostgresStore) GetSession(
	ctx context.Context,
	principal UserPrincipal,
	sessionID string,
) (AgentSession, error) {
	query := sessionSelectSQL + ` JOIN agents a ON a.agent_id = agent_sessions.agent_id WHERE agent_sessions.session_id = $1 AND a.deleted_at IS NULL`
	query += ` AND (
		a.owner_user_id = $2
		OR ` + teamAgentAccessSQL("a.agent_id", "$2") + `
	)`
	args := []any{sessionID, principal.User.UserID}
	query += ` ORDER BY agent_sessions.updated_at DESC LIMIT 1`
	row := s.db.QueryRowContext(ctx, query, args...)
	return scanSession(row)
}

func (s *PostgresStore) UpdateSessionRuntimeState(
	ctx context.Context,
	state SessionRuntimeState,
) error {
	if state.AgentID == "" || state.SessionID == "" {
		return ErrNotFound
	}
	if state.UpdatedAt.IsZero() {
		state.UpdatedAt = s.now().UTC()
	}
	status, currentTask, runID, runStatus := state.StatusSummary()
	stateJSON, err := json.Marshal(state)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var lockedAgentID string
	if err := tx.QueryRowContext(ctx, `
		SELECT agent_id FROM agents WHERE agent_id = $1 AND deleted_at IS NULL FOR UPDATE
	`, state.AgentID).Scan(&lockedAgentID); err != nil {
		return mapSQLError(err)
	}
	var authority string
	err = tx.QueryRowContext(ctx, `
		SELECT runtime_authority
		FROM agent_runtime_snapshot_heads
		WHERE agent_id = $1
	`, state.AgentID).Scan(&authority)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if authority == domain.RuntimeAuthoritySnapshot {
		return tx.Commit()
	}
	runtimeStatus := domain.NormalizeRuntimeStatus(state.Lifecycle)
	_, err = tx.ExecContext(ctx, `
		INSERT INTO agent_sessions (
			node_id, agent_id, session_id, status, current_task, run_id, run_status,
			runtime_status, runtime_turn_instance_id, metadata, created_at, updated_at
		)
		VALUES (
			NULLIF($1,''), $2, $3, $4, $5, $6, $7,
			$8, NULLIF($9, ''), jsonb_build_object('runtime_state', $10::jsonb), $11, $11
		)
		ON CONFLICT (agent_id, session_id) DO UPDATE SET
			node_id = COALESCE(EXCLUDED.node_id, agent_sessions.node_id),
			status = EXCLUDED.status,
			current_task = EXCLUDED.current_task,
			run_id = EXCLUDED.run_id,
			run_status = EXCLUDED.run_status,
			runtime_status = EXCLUDED.runtime_status,
			runtime_turn_instance_id = EXCLUDED.runtime_turn_instance_id,
			metadata = jsonb_set(
				COALESCE(agent_sessions.metadata, '{}'::jsonb),
				'{runtime_state}',
				$10::jsonb,
				true
			),
			updated_at = EXCLUDED.updated_at
	`, state.NodeID, state.AgentID, state.SessionID, status, currentTask, runID, runStatus,
		runtimeStatus, state.TurnInstanceID, stateJSON, state.UpdatedAt)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *PostgresStore) ListSessionMessages(
	ctx context.Context,
	principal UserPrincipal,
	sessionID string,
) ([]MailboxMessage, error) {
	query := mailboxSelectSQL + ` WHERE session_id = $1`
	query += ` AND (
		owner_user_id = $2
		OR ` + teamAgentAccessSQL("mailbox.agent_id", "$2") + `
	)`
	args := []any{sessionID, principal.User.UserID}
	query += ` ORDER BY id ASC`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return scanMailboxRows(rows)
}

func (s *PostgresStore) CreateMailboxMessage(
	ctx context.Context,
	principal UserPrincipal,
	req CreateMailboxRequest,
) (MailboxMessage, error) {
	messageType := defaultMessageType(req.MessageType)
	if messageType == "" {
		return MailboxMessage{}, ErrConflict
	}
	payload, err := mailboxPayload(req)
	if err != nil {
		return MailboxMessage{}, ErrConflict
	}
	payload, _, err = postgresSafeJSON(payload)
	if err != nil {
		return MailboxMessage{}, ErrConflict
	}
	message, _ := postgresSafeText(req.Message)
	var ownerUserID string
	var agentNodeID string
	agentQuery := `SELECT owner_user_id, COALESCE(node_id, '') FROM agents WHERE agent_id = $1 AND deleted_at IS NULL`
	agentArgs := []any{req.AgentID}
	if req.NodeID != "" {
		agentQuery += ` AND node_id = $2`
		agentArgs = append(agentArgs, req.NodeID)
	}
	err = s.db.QueryRowContext(ctx, agentQuery, agentArgs...).Scan(&ownerUserID, &agentNodeID)
	if err != nil {
		return MailboxMessage{}, mapSQLError(err)
	}
	if !canAccessOwner(principal, ownerUserID) {
		if _, err := s.GetAgent(ctx, principal, req.AgentID); err != nil {
			return MailboxMessage{}, ErrNotFound
		}
	}
	if req.SessionID != "" {
		var sessionNodeID string
		err = s.db.QueryRowContext(ctx, `
			SELECT COALESCE(node_id, '') FROM agent_sessions
			WHERE agent_id = $1 AND session_id = $2
			ORDER BY updated_at DESC
			LIMIT 1
		`, req.AgentID, req.SessionID).Scan(&sessionNodeID)
		if err != nil {
			return MailboxMessage{}, mapSQLError(err)
		}
		if req.NodeID != "" && sessionNodeID != "" && sessionNodeID != agentNodeID {
			return MailboxMessage{}, ErrNotFound
		}
	}
	messageID, err := newSecret("msg")
	if err != nil {
		return MailboxMessage{}, err
	}
	now := s.now().UTC()
	row := s.db.QueryRowContext(ctx, `
		INSERT INTO mailbox (
			message_id, user_id, owner_user_id, node_id, agent_id, session_id, message, message_type,
			payload, status, direction, created_at, expires_at
		)
		VALUES ($1,$2,$3,NULLIF($4,''),$5,NULLIF($6,''),$7,$8,$9,'pending','user_to_node',$10,$11)
		RETURNING `+mailboxReturningSQL+`
	`, messageID, principal.User.UserID, ownerUserID, agentNodeID, req.AgentID, req.SessionID, message, messageType, payload, now, expiresAt(now, messageType))
	msg, err := scanMailbox(row)
	if err != nil {
		return MailboxMessage{}, err
	}
	if err := s.saveMailboxHistory(ctx, msg); err != nil {
		return MailboxMessage{}, err
	}
	return msg, nil
}

func (s *PostgresStore) CreateApproval(
	ctx context.Context,
	node Node,
	req CreateApprovalRequest,
) (AgentApproval, error) {
	var ownerUserID string
	if err := s.db.QueryRowContext(ctx, `
		SELECT owner_user_id FROM agents WHERE agent_id = $1 AND node_id = $2 AND deleted_at IS NULL
	`, req.AgentID, node.NodeID).Scan(&ownerUserID); err != nil {
		return AgentApproval{}, mapSQLError(err)
	}
	if ownerUserID != node.OwnerUserID {
		return AgentApproval{}, ErrUnauthorized
	}
	sessionID := req.SessionID
	if sessionID != "" {
		translated, err := s.virtualSessionID(ctx, s.db, req.AgentID, sessionID)
		if err != nil {
			return AgentApproval{}, err
		}
		sessionID = translated
	}
	approvalID, err := newSecret("appr")
	if err != nil {
		return AgentApproval{}, err
	}
	now := s.now().UTC()
	row := s.db.QueryRowContext(ctx, `
		INSERT INTO agent_approvals (
			approval_id, owner_user_id, request_node_id, request_agent_id, request_session_id,
			source_message_id, domain, operation, resource_type, resource_ref, title, description,
			risk_level, action_fingerprint, request_body, requested_effects, options, status,
			created_at, expires_at, raw_payload
		)
		VALUES (
			$1,$2,NULLIF($3,''),NULLIF($4,''),$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,
			$15,$16,$17,'pending',$18,$19,$20
			)
			RETURNING `+approvalReturningSQL+`
	`, approvalID, ownerUserID, node.NodeID, req.AgentID, sessionID, firstNonEmpty(req.NativeID, req.SourceMessageID),
		defaultApprovalDomain(req.Domain), req.Operation, req.ResourceType, req.ResourceRef,
		req.Title, req.Description, defaultApprovalRiskLevel(req.RiskLevel), req.ActionFingerprint,
		jsonDefault(req.RequestBody, "{}"), jsonDefault(req.RequestedEffects, "[]"),
		jsonOrDefault(req.Options, "[]"), now, req.ExpiresAt, jsonDefault(req.RawPayload, "{}"))
	approval, err := scanApproval(row)
	if err != nil {
		return AgentApproval{}, err
	}
	if err := upsertAuditEvents(ctx, s, auditEventsForApprovalRequested(approval)); err != nil {
		return AgentApproval{}, err
	}
	return s.translateApprovalToNative(ctx, s.db, approval)
}

func (s *PostgresStore) GetApproval(
	ctx context.Context,
	principal UserPrincipal,
	approvalID string,
) (AgentApproval, error) {
	query := approvalSelectSQL + ` WHERE approval_id = $1`
	query += ` AND owner_user_id = $2`
	args := []any{approvalID, principal.User.UserID}
	return scanApproval(s.db.QueryRowContext(ctx, query, args...))
}

func (s *PostgresStore) GetNodeApproval(
	ctx context.Context,
	node Node,
	agentID string,
	approvalID string,
) (AgentApproval, error) {
	approval, err := scanApproval(s.db.QueryRowContext(ctx, approvalSelectSQL+`
		WHERE approval_id = $1
			AND owner_user_id = $2
			AND request_node_id = $3
			AND request_agent_id = $4
	`, approvalID, node.OwnerUserID, node.NodeID, agentID))
	if err != nil {
		return AgentApproval{}, err
	}
	return s.translateApprovalToNative(ctx, s.db, approval)
}

func (s *PostgresStore) ListApprovals(
	ctx context.Context,
	filter ApprovalFilter,
) ([]AgentApproval, error) {
	query, args := approvalListQuery(filter)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return scanApprovals(rows)
}

func (s *PostgresStore) DecideApproval(
	ctx context.Context,
	principal UserPrincipal,
	approvalID string,
	req ApprovalDecisionRequest,
) (AgentApproval, error) {
	approval, err := s.GetApproval(ctx, principal, approvalID)
	if err != nil {
		return AgentApproval{}, err
	}
	if approval.Status != "pending" {
		return AgentApproval{}, ErrConflict
	}
	decision, scope, grantNodeID, grantAgentID, grantSessionID, err := approvalDecisionGrant(
		approval,
		req,
	)
	if err != nil {
		return AgentApproval{}, err
	}
	now := s.now().UTC()
	row := s.db.QueryRowContext(ctx, `
		UPDATE agent_approvals
		SET status = 'decided',
			decision = $2,
			decision_option = $3,
			decision_scope = $4,
			grant_node_id = $5,
			grant_agent_id = $6,
			grant_session_id = $7,
			grant_body = $8,
			decided_by_user_id = $9,
			decided_at = $10
		WHERE approval_id = $1
		RETURNING `+approvalReturningSQL+`
	`, approvalID, decision, req.DecisionOption, scope, grantNodeID, grantAgentID, grantSessionID,
		jsonDefault(req.GrantBody, "{}"), principal.User.UserID, now)
	approval, err = scanApproval(row)
	if err != nil {
		return AgentApproval{}, err
	}
	if err := upsertAuditEvents(ctx, s, auditEventsForApprovalDecided(approval)); err != nil {
		return AgentApproval{}, err
	}
	return approval, nil
}

func (s *PostgresStore) RecordApprovalResponse(
	ctx context.Context,
	principal UserPrincipal,
	approvalID string,
	responseBody json.RawMessage,
	responseError string,
) (AgentApproval, error) {
	approval, err := s.GetApproval(ctx, principal, approvalID)
	if err != nil {
		return AgentApproval{}, err
	}
	if approval.RespondedAt != nil {
		return AgentApproval{}, ErrConflict
	}
	now := s.now().UTC()
	row := s.db.QueryRowContext(ctx, `
		UPDATE agent_approvals
		SET responded_at = $3,
			response_body = $4,
			response_error = $5
		WHERE approval_id = $1
			AND owner_user_id = $2
			AND responded_at IS NULL
		RETURNING `+approvalReturningSQL+`
	`, approvalID, principal.User.UserID, now, jsonDefault(responseBody, "{}"), responseError)
	return scanApproval(row)
}

func (s *PostgresStore) ListApprovalGrants(
	ctx context.Context,
	filter ApprovalGrantFilter,
) ([]AgentApproval, error) {
	query, args := approvalGrantListQuery(filter)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return scanApprovals(rows)
}

func (s *PostgresStore) FindReusableApprovalGrant(
	ctx context.Context,
	lookup ApprovalGrantLookup,
) (AgentApproval, error) {
	return scanApproval(s.db.QueryRowContext(ctx, approvalSelectSQL+`
		WHERE owner_user_id = $1
			AND domain = $2
			AND operation = $3
			AND action_fingerprint = $4
			AND status = 'decided'
			AND decision = 'allow'
			AND decision_scope <> 'once'
			AND grant_revoked_at IS NULL
			AND (expires_at IS NULL OR expires_at > $8)
			AND (grant_node_id = '*' OR grant_node_id = $5)
			AND (grant_agent_id = '*' OR grant_agent_id = $6)
			AND (grant_session_id = '*' OR grant_session_id = $7)
		ORDER BY
			CASE WHEN grant_session_id = '*' THEN 0 ELSE 1 END DESC,
			CASE WHEN grant_agent_id = '*' THEN 0 ELSE 1 END DESC,
			CASE WHEN grant_node_id = '*' THEN 0 ELSE 1 END DESC,
			decided_at DESC
		LIMIT 1
	`, lookup.OwnerUserID, lookup.Domain, lookup.Operation, lookup.ActionFingerprint,
		lookup.RequestNodeID, lookup.RequestAgentID, lookup.RequestSessionID, s.now().UTC()))
}

func (s *PostgresStore) RevokeApprovalGrant(
	ctx context.Context,
	principal UserPrincipal,
	grantID string,
	req RevokeApprovalGrantRequest,
) (AgentApproval, error) {
	approval, err := s.GetApproval(ctx, principal, grantID)
	if err != nil {
		return AgentApproval{}, err
	}
	if approval.Decision != "allow" || approval.DecisionScope == "" ||
		approval.DecisionScope == "once" {
		return AgentApproval{}, ErrConflict
	}
	if approval.GrantRevokedAt != nil {
		return approval, nil
	}
	now := s.now().UTC()
	row := s.db.QueryRowContext(ctx, `
		UPDATE agent_approvals
		SET grant_revoked_at = $2,
			grant_revoked_by_user_id = $3,
			grant_revocation_reason = $4
		WHERE approval_id = $1
		RETURNING `+approvalReturningSQL+`
	`, grantID, now, principal.User.UserID, req.Reason)
	return scanApproval(row)
}

func (s *PostgresStore) ListMailbox(
	ctx context.Context,
	filter MailboxFilter,
) ([]MailboxMessage, error) {
	limit := filter.Limit
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	clauses := []string{"1=1"}
	args := []any{}
	add := func(clause string, value any) {
		args = append(args, value)
		clauses = append(clauses, clause+" $"+strconvArg(len(args)))
	}
	if filter.AgentID != "" {
		add("agent_id =", filter.AgentID)
	}
	if filter.NodeID != "" {
		add("node_id =", filter.NodeID)
	}
	if filter.SessionID != "" {
		add("session_id =", filter.SessionID)
	}
	if filter.Status != "" {
		add("status =", filter.Status)
	}
	add("owner_user_id =", filter.Principal.User.UserID)
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, mailboxSelectSQL+`
		WHERE `+strings.Join(clauses, " AND ")+`
		ORDER BY id DESC
		LIMIT $`+strconvArg(len(args)), args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return scanMailboxRows(rows)
}

func (s *PostgresStore) PullMailbox(
	ctx context.Context,
	agentID string,
	sessionID string,
	offset int64,
	limit int,
) (MailboxPull, error) {
	if limit <= 0 || limit > 100 {
		limit = 10
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return MailboxPull{}, err
	}
	defer func() { _ = tx.Rollback() }()

	now := s.now().UTC()
	querySessionID := sessionID
	if querySessionID != "" {
		querySessionID, err = s.virtualSessionID(ctx, tx, agentID, querySessionID)
		if err != nil {
			return MailboxPull{}, err
		}
	}
	rows, err := s.pullMailboxRows(ctx, tx, agentID, querySessionID, offset, limit, now)
	if err != nil {
		return MailboxPull{}, err
	}
	messages, err := scanMailboxRows(rows)
	closeErr := rows.Close()
	if err != nil {
		return MailboxPull{}, err
	}
	if closeErr != nil {
		return MailboxPull{}, closeErr
	}

	hasMore := len(messages) > limit
	if hasMore {
		messages = messages[:limit]
	}
	if err := s.translateMailboxMessagesToNative(ctx, tx, messages); err != nil {
		return MailboxPull{}, err
	}

	maxOffset := offset
	for i := range messages {
		messages[i].Status = "delivered"
		messages[i].DeliveredAt = &now
		if messages[i].ID > maxOffset {
			maxOffset = messages[i].ID
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE mailbox
			SET status = 'delivered', delivered_at = $2
			WHERE id = $1
		`, messages[i].ID, now); err != nil {
			return MailboxPull{}, err
		}
	}

	if err := tx.Commit(); err != nil {
		return MailboxPull{}, err
	}
	return MailboxPull{Messages: messages, MaxOffset: maxOffset, HasMore: hasMore}, nil
}

func (s *PostgresStore) PullNodeMailbox(
	ctx context.Context,
	nodeID string,
	agentID string,
	sessionID string,
	offset int64,
	limit int,
) (MailboxPull, error) {
	if limit <= 0 || limit > 100 {
		limit = 10
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return MailboxPull{}, err
	}
	defer func() { _ = tx.Rollback() }()
	now := s.now().UTC()
	querySessionID := sessionID
	if agentID != "" && querySessionID != "" {
		querySessionID, err = s.virtualSessionID(ctx, tx, agentID, querySessionID)
		if err != nil {
			return MailboxPull{}, err
		}
	}
	clauses := []string{
		"node_id = $1",
		"id > $2",
		"status = 'pending'",
		"COALESCE(direction, 'user_to_node') <> 'node_to_user'",
	}
	args := []any{nodeID, offset}
	if agentID != "" {
		args = append(args, agentID)
		clauses = append(clauses, "agent_id = $"+strconvArg(len(args)))
	}
	if querySessionID != "" {
		args = append(args, querySessionID)
		clauses = append(clauses, "session_id = $"+strconvArg(len(args)))
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE mailbox
		SET status = 'expired'
		WHERE node_id = $1 AND status = 'pending' AND expires_at IS NOT NULL AND expires_at < $2
	`, nodeID, now); err != nil {
		return MailboxPull{}, err
	}
	args = append(args, limit+1)
	rows, err := tx.QueryContext(ctx, mailboxSelectSQL+`
		WHERE `+strings.Join(clauses, " AND ")+`
		ORDER BY id ASC
		LIMIT $`+strconvArg(len(args))+`
		FOR UPDATE SKIP LOCKED
	`, args...)
	if err != nil {
		return MailboxPull{}, err
	}
	messages, err := scanMailboxRows(rows)
	closeErr := rows.Close()
	if err != nil {
		return MailboxPull{}, err
	}
	if closeErr != nil {
		return MailboxPull{}, closeErr
	}
	hasMore := len(messages) > limit
	if hasMore {
		messages = messages[:limit]
	}
	if err := s.translateMailboxMessagesToNative(ctx, tx, messages); err != nil {
		return MailboxPull{}, err
	}
	maxOffset := offset
	for i := range messages {
		messages[i].Status = "delivered"
		messages[i].DeliveredAt = &now
		if messages[i].ID > maxOffset {
			maxOffset = messages[i].ID
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE mailbox
			SET status = 'delivered', delivered_at = $2
			WHERE id = $1
		`, messages[i].ID, now); err != nil {
			return MailboxPull{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return MailboxPull{}, err
	}
	return MailboxPull{Messages: messages, MaxOffset: maxOffset, HasMore: hasMore}, nil
}

func (s *PostgresStore) pullMailboxRows(
	ctx context.Context,
	tx *sql.Tx,
	agentID string,
	sessionID string,
	offset int64,
	limit int,
	now time.Time,
) (*sql.Rows, error) {
	if sessionID == "" {
		if _, err := tx.ExecContext(ctx, `
			UPDATE mailbox
			SET status = 'expired'
			WHERE agent_id = $1 AND status = 'pending' AND expires_at IS NOT NULL AND expires_at < $2
		`, agentID, now); err != nil {
			return nil, err
		}
		return tx.QueryContext(ctx, mailboxSelectSQL+`
			WHERE agent_id = $1 AND id > $2 AND status = 'pending'
			ORDER BY id ASC
			LIMIT $3
			FOR UPDATE SKIP LOCKED
		`, agentID, offset, limit+1)
	}

	if _, err := tx.ExecContext(ctx, `
		UPDATE mailbox
		SET status = 'expired'
		WHERE agent_id = $1 AND session_id = $2 AND status = 'pending'
			AND expires_at IS NOT NULL AND expires_at < $3
	`, agentID, sessionID, now); err != nil {
		return nil, err
	}
	return tx.QueryContext(ctx, mailboxSelectSQL+`
		WHERE agent_id = $1 AND session_id = $2 AND id > $3 AND status = 'pending'
		ORDER BY id ASC
		LIMIT $4
		FOR UPDATE SKIP LOCKED
	`, agentID, sessionID, offset, limit+1)
}

func (s *PostgresStore) MarkMessageResult(
	ctx context.Context,
	agentID string,
	messageID string,
	req MessageResultRequest,
) error {
	if req.Status != "completed" && req.Status != "failed" {
		return ErrConflict
	}
	completedAt := s.now().UTC()
	if req.CompletedAt != nil {
		completedAt = req.CompletedAt.UTC()
	}
	resultText, _ := postgresSafeText(req.Result)
	errorText, _ := postgresSafeText(req.Error)
	result, err := s.db.ExecContext(ctx, `
		UPDATE mailbox
		SET status = $3, result = $4, error = $5, completed_at = $6
		WHERE agent_id = $1 AND message_id = $2
	`, agentID, messageID, req.Status, resultText, errorText, completedAt)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrNotFound
	}
	msg, err := scanMailbox(s.db.QueryRowContext(
		ctx,
		mailboxSelectSQL+` WHERE agent_id = $1 AND message_id = $2`,
		agentID,
		messageID,
	))
	if err != nil {
		return err
	}
	if err := upsertAuditEvents(ctx, s, auditEventsForMailbox(msg)); err != nil {
		return err
	}
	return nil
}

func (s *PostgresStore) MarkNodeMessageResult(
	ctx context.Context,
	nodeID string,
	messageID string,
	req MessageResultRequest,
) error {
	if req.Status != "completed" && req.Status != "failed" {
		return ErrConflict
	}
	completedAt := s.now().UTC()
	if req.CompletedAt != nil {
		completedAt = req.CompletedAt.UTC()
	}
	resultText, _ := postgresSafeText(firstNonEmpty(req.Result, req.Content, req.ResultMessageID))
	errorText, _ := postgresSafeText(req.Error)
	result, err := s.db.ExecContext(
		ctx,
		`
		UPDATE mailbox
		SET status = $3, result = $4, error = $5, completed_at = $6, payload = COALESCE($7, payload),
			events = COALESCE($8, events), file_changes = COALESCE($9, file_changes), token_usage = COALESCE($10, token_usage)
		WHERE node_id = $1 AND message_id = $2
	`,
		nodeID,
		messageID,
		req.Status,
		resultText,
		errorText,
		completedAt,
		nullRaw(req.Payload),
		nullRaw(req.Events),
		jsonOrNil(req.FileChanges),
		jsonOrNil(req.TokenUsage),
	)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrNotFound
	}
	msg, err := scanMailbox(s.db.QueryRowContext(
		ctx,
		mailboxSelectSQL+` WHERE node_id = $1 AND message_id = $2`,
		nodeID,
		messageID,
	))
	if err != nil {
		return err
	}
	if err := upsertAuditEvents(ctx, s, auditEventsForMailbox(msg)); err != nil {
		return err
	}
	return nil
}

func (s *PostgresStore) MarkNodeMessageDelivered(
	ctx context.Context,
	nodeID string,
	req MarkDeliveredRequest,
) error {
	deliveredAt := s.now().UTC()
	if req.DeliveredAt != nil {
		deliveredAt = req.DeliveredAt.UTC()
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE mailbox
		SET status = CASE WHEN status = 'pending' THEN 'delivered' ELSE status END,
			delivered_at = $3
		WHERE node_id = $1 AND message_id = $2
	`, nodeID, req.MessageID, deliveredAt)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PostgresStore) CreateNodeOutboundMessage(
	ctx context.Context,
	node Node,
	req CreateOutboundMessageRequest,
) (MailboxMessage, error) {
	var ownerUserID string
	if err := s.db.QueryRowContext(ctx, `
		SELECT owner_user_id FROM agents WHERE agent_id = $1 AND node_id = $2 AND deleted_at IS NULL
	`, req.AgentID, node.NodeID).Scan(&ownerUserID); err != nil {
		return MailboxMessage{}, mapSQLError(err)
	}
	createdAt := s.now().UTC()
	if req.CreatedAt != nil {
		createdAt = req.CreatedAt.UTC()
	}
	sessionID := req.SessionID
	if sessionID != "" {
		translated, err := s.virtualSessionID(ctx, s.db, req.AgentID, sessionID)
		if err != nil {
			return MailboxMessage{}, err
		}
		sessionID = translated
	}
	msg, err := s.insertMailbox(
		ctx,
		ownerUserID,
		ownerUserID,
		node.NodeID,
		req.AgentID,
		sessionID,
		req.Content,
		defaultOutboundMessageType(req.MessageType),
		req.Payload,
		defaultOutboundStatus(req.Status),
		"node_to_user",
		createdAt,
	)
	if err != nil {
		return MailboxMessage{}, err
	}
	_, err = s.db.ExecContext(ctx, `
		UPDATE mailbox
		SET parent_message_id = $2, turn_id = $3, response_id = $4, events = $5,
			file_changes = COALESCE($6, '[]'::jsonb), token_usage = $7
		WHERE id = $1
	`, msg.ID, req.ParentMessageID, req.TurnID, req.ResponseID, nullRaw(req.Events),
		jsonOrNil(req.FileChanges), jsonOrNil(req.TokenUsage))
	if err != nil {
		return MailboxMessage{}, err
	}
	msg, err = scanMailbox(s.db.QueryRowContext(ctx, mailboxSelectSQL+` WHERE id = $1`, msg.ID))
	if err != nil {
		return MailboxMessage{}, err
	}
	if err := s.saveMailboxHistory(ctx, msg); err != nil {
		return MailboxMessage{}, err
	}
	if err := upsertAuditEvents(ctx, s, auditEventsForMailbox(msg)); err != nil {
		return MailboxMessage{}, err
	}
	msg.SessionID, err = s.nativeSessionID(ctx, s.db, msg.AgentID, msg.SessionID)
	if err != nil {
		return MailboxMessage{}, err
	}
	msg.Payload = replacePayloadSessionID(msg.Payload, msg.SessionID)
	return msg, nil
}

func (s *PostgresStore) UpdateOffset(ctx context.Context, agentID string, offset int64) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO message_offsets (agent_id, last_offset, updated_at)
		VALUES ($1, $2, $3)
		ON CONFLICT (agent_id) DO UPDATE SET
			last_offset = GREATEST(message_offsets.last_offset, EXCLUDED.last_offset),
			updated_at = EXCLUDED.updated_at
	`, agentID, offset, s.now().UTC())
	return err
}

func (s *PostgresStore) UpdateNodeOffset(ctx context.Context, nodeID string, offset int64) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO node_message_offsets (node_id, last_offset, updated_at)
		VALUES ($1, $2, $3)
		ON CONFLICT (node_id) DO UPDATE SET
			last_offset = GREATEST(node_message_offsets.last_offset, EXCLUDED.last_offset),
			updated_at = EXCLUDED.updated_at
	`, nodeID, offset, s.now().UTC())
	return err
}

const sessionSelectSQL = `
	SELECT agent_sessions.id, COALESCE(agent_sessions.node_id, ''), agent_sessions.agent_id, session_id,
		COALESCE(conversation_id, ''), COALESCE(profile_id, ''),
		COALESCE(representative_agent_id, ''), COALESCE(created_by_user_id, ''),
		COALESCE(custom_session_name, session_name, ''), COALESCE(session_name, ''),
		custom_session_name IS NOT NULL, COALESCE(agent_sessions.agent_type, ''),
		COALESCE(native_id, ''), COALESCE(primary_project_id, ''), COALESCE(preview, ''),
		COALESCE(workspace_roots, '[]'::jsonb), COALESCE(source, ''),
		COALESCE(agent_sessions.transport, 'manager'), agent_sessions.status,
		COALESCE(current_task, ''), last_message_at, last_user_message_at, message_count, token_input,
		token_output, token_total, cache_read_tokens, cache_write_tokens, cache_creation_tokens,
		reasoning_tokens, estimated_cost_usd, actual_cost_usd, cost_usd, COALESCE(model, ''), COALESCE(run_id, ''),
		COALESCE(run_status, ''), runtime_status, COALESCE(runtime_turn_instance_id, ''),
		agent_sessions.created_at, agent_sessions.updated_at, agent_sessions.archived_at,
		COALESCE(agent_sessions.metadata, '{}'::jsonb)
	FROM agent_sessions`

const mailboxSelectSQL = `
	SELECT ` + mailboxReturningSQL + `
	FROM mailbox`

const mailboxReturningSQL = `
	id, message_id, user_id, owner_user_id, COALESCE(node_id, ''), agent_id, COALESCE(session_id, ''), message, message_type,
		COALESCE(payload, '{}'::jsonb), status, delivered_at, completed_at,
		COALESCE(result, ''), COALESCE(error, ''), created_at, expires_at,
		COALESCE(direction, ''), COALESCE(parent_message_id, ''), COALESCE(turn_id, ''), COALESCE(response_id, ''),
		COALESCE(events, '{}'::jsonb), COALESCE(file_changes, '[]'::jsonb), COALESCE(token_usage, '{}'::jsonb)`

const approvalSelectSQL = `
	SELECT ` + approvalReturningSQL + `
	FROM agent_approvals`

const approvalReturningSQL = `
	approval_id, owner_user_id, COALESCE(request_node_id, ''), COALESCE(request_agent_id, ''),
		COALESCE(request_session_id, ''), COALESCE(source_message_id, ''), grant_node_id,
		grant_agent_id, grant_session_id, domain, operation, resource_type, resource_ref,
		title, description, risk_level, action_fingerprint, request_body, requested_effects,
		options, status, decision, decision_option, decision_scope, grant_body,
		COALESCE(decided_by_user_id, ''), grant_revoked_at,
		COALESCE(grant_revoked_by_user_id, ''), grant_revocation_reason, created_at,
		expires_at, decided_at, raw_payload, responded_at, response_body,
		COALESCE(response_error, '')`

const secretSelectSQL = `
	SELECT secret_id, owner_user_id, name, kind, description, metadata,
		COALESCE(current_version_id, ''), current_version, created_at, updated_at, deleted_at
	FROM secrets`

const secretVersionSelectSQL = `
	SELECT version_id, secret_id, version_number, ciphertext, nonce, key_id, state, created_at,
		COALESCE(created_by_user_id, ''), COALESCE(created_by_node_id, ''),
		COALESCE(created_by_agent_id, ''), idempotency_key
	FROM secret_versions`

func approvalListQuery(filter ApprovalFilter) (string, []any) {
	limit := filter.Limit
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	query := approvalSelectSQL
	clauses := []string{"1=1"}
	args := []any{}
	add := func(column string, value string) {
		if value == "" {
			return
		}
		args = append(args, value)
		clauses = append(clauses, column+" = $"+strconvArg(len(args)))
	}
	add("owner_user_id", filter.Principal.User.UserID)
	add("status", filter.Status)
	add("decision", filter.Decision)
	add("domain", filter.Domain)
	add("operation", filter.Operation)
	add("resource_type", filter.ResourceType)
	add("resource_ref", filter.ResourceRef)
	add("request_node_id", filter.RequestNodeID)
	add("request_agent_id", filter.RequestAgentID)
	add("request_session_id", filter.RequestSessionID)
	add("decision_scope", filter.DecisionScope)
	if !filter.IncludeRevoked {
		clauses = append(clauses, "grant_revoked_at IS NULL")
	}
	args = append(args, limit)
	return query + `
		WHERE ` + strings.Join(clauses, " AND ") + `
		ORDER BY created_at DESC
		LIMIT $` + strconvArg(len(args)), args
}

func approvalGrantListQuery(filter ApprovalGrantFilter) (string, []any) {
	limit := filter.Limit
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	query := approvalSelectSQL
	clauses := []string{"status = 'decided'", "decision = 'allow'", "decision_scope <> 'once'"}
	args := []any{}
	add := func(column string, value string) {
		if value == "" {
			return
		}
		args = append(args, value)
		clauses = append(clauses, column+" = $"+strconvArg(len(args)))
	}
	add("owner_user_id", filter.Principal.User.UserID)
	add("domain", filter.Domain)
	add("operation", filter.Operation)
	add("resource_type", filter.ResourceType)
	add("resource_ref", filter.ResourceRef)
	add("decision_scope", filter.DecisionScope)
	add("grant_node_id", filter.GrantNodeID)
	add("grant_agent_id", filter.GrantAgentID)
	add("grant_session_id", filter.GrantSessionID)
	if filter.ActiveOnly {
		clauses = append(clauses, "grant_revoked_at IS NULL")
	}
	args = append(args, limit)
	return query + `
		WHERE ` + strings.Join(clauses, " AND ") + `
		ORDER BY decided_at DESC
		LIMIT $` + strconvArg(len(args)), args
}

type sqlExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

type sqlQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type dbExecer struct {
	db *sql.DB
}

func (e dbExecer) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return e.db.ExecContext(ctx, query, args...)
}

func (e dbExecer) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return e.db.QueryRowContext(ctx, query, args...)
}

func updateSessionPaxConfig(
	ctx context.Context,
	exec sqlExecer,
	agentID string,
	sessionID string,
	config SessionPaxConfig,
	updatedAt time.Time,
) error {
	config.ApprovalMode = normalizeSessionApprovalMode(config.ApprovalMode)
	configJSON, err := json.Marshal(config)
	if err != nil {
		return err
	}
	result, err := exec.ExecContext(ctx, `
		UPDATE agent_sessions
		SET metadata = jsonb_set(COALESCE(metadata, '{}'::jsonb), '{pax_config}', $3::jsonb, true),
			updated_at = $4
		WHERE agent_id = $1 AND session_id = $2
	`, agentID, sessionID, configJSON, updatedAt)
	if err != nil {
		return err
	}
	if rows, err := result.RowsAffected(); err == nil && rows == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PostgresStore) normalizeReportedSessionInput(
	ctx context.Context,
	queryer sqlQueryer,
	agentID string,
	input SessionStatusInput,
) (SessionStatusInput, error) {
	nativeID := reportedNativeSessionID(input)
	if nativeID == "" {
		return input, nil
	}
	var sessionID string
	err := queryer.QueryRowContext(ctx, `
		SELECT session_id FROM agent_sessions
		WHERE agent_id = $1 AND native_id = $2
		ORDER BY updated_at DESC
		LIMIT 1
	`, agentID, nativeID).Scan(&sessionID)
	if err != nil && err != sql.ErrNoRows {
		return input, err
	}
	if err == nil {
		input.SessionID = sessionID
	} else if input.NativeID == "" || input.SessionID == "" || !isManagerSessionID(input.SessionID) {
		generated, genErr := newSecret("sess")
		if genErr != nil {
			return input, genErr
		}
		input.SessionID = generated
	}
	input.NativeID = nativeID
	return input, nil
}

func (s *PostgresStore) virtualSessionID(
	ctx context.Context,
	queryer sqlQueryer,
	agentID string,
	sessionID string,
) (string, error) {
	if sessionID == "" {
		return "", nil
	}
	var translated string
	err := queryer.QueryRowContext(ctx, `
		SELECT session_id FROM agent_sessions
		WHERE agent_id = $1 AND native_id = $2
		ORDER BY updated_at DESC
		LIMIT 1
	`, agentID, sessionID).Scan(&translated)
	if err == nil {
		return translated, nil
	}
	if err != sql.ErrNoRows {
		return "", err
	}
	err = queryer.QueryRowContext(ctx, `
		SELECT session_id FROM agent_sessions
		WHERE agent_id = $1 AND session_id = $2
		ORDER BY updated_at DESC
		LIMIT 1
	`, agentID, sessionID).Scan(&translated)
	if err == nil {
		return translated, nil
	}
	if err != sql.ErrNoRows {
		return "", err
	}
	return sessionID, nil
}

func (s *PostgresStore) nativeSessionID(
	ctx context.Context,
	queryer sqlQueryer,
	agentID string,
	sessionID string,
) (string, error) {
	if sessionID == "" {
		return "", nil
	}
	var nativeID string
	err := queryer.QueryRowContext(ctx, `
		SELECT COALESCE(native_id, '') FROM agent_sessions
		WHERE agent_id = $1 AND session_id = $2
		ORDER BY updated_at DESC
		LIMIT 1
	`, agentID, sessionID).Scan(&nativeID)
	if err == nil && nativeID != "" {
		return nativeID, nil
	}
	if err != nil && err != sql.ErrNoRows {
		return "", err
	}
	return sessionID, nil
}

func (s *PostgresStore) translateMailboxMessagesToNative(
	ctx context.Context,
	queryer sqlQueryer,
	messages []MailboxMessage,
) error {
	for i := range messages {
		nativeID, err := s.nativeSessionID(ctx, queryer, messages[i].AgentID, messages[i].SessionID)
		if err != nil {
			return err
		}
		messages[i].SessionID = nativeID
		messages[i].Payload = replacePayloadSessionID(messages[i].Payload, nativeID)
	}
	return nil
}

func (s *PostgresStore) translateApprovalToNative(
	ctx context.Context,
	queryer sqlQueryer,
	approval AgentApproval,
) (AgentApproval, error) {
	var err error
	if approval.RequestAgentID != "" && approval.RequestSessionID != "" {
		approval.RequestSessionID, err = s.nativeSessionID(
			ctx,
			queryer,
			approval.RequestAgentID,
			approval.RequestSessionID,
		)
		if err != nil {
			return AgentApproval{}, err
		}
	}
	if approval.GrantAgentID != "" && approval.GrantSessionID != "" {
		approval.GrantSessionID, err = s.nativeSessionID(
			ctx,
			queryer,
			approval.GrantAgentID,
			approval.GrantSessionID,
		)
		if err != nil {
			return AgentApproval{}, err
		}
	}
	return approval, nil
}

func upsertSessionTx(
	ctx context.Context,
	exec sqlExecer,
	nodeID string,
	agentID string,
	input SessionStatusInput,
	primaryProjectID string,
	now time.Time,
) error {
	roots, err := json.Marshal(input.WorkspaceRoots)
	if err != nil {
		return err
	}
	_, err = exec.ExecContext(
		ctx,
		`
		INSERT INTO agent_sessions (
			node_id, agent_id, session_id, session_name, agent_type, native_id,
			primary_project_id, preview,
			workspace_roots, source, status, current_task, last_message_at, last_user_message_at, message_count,
			token_input, token_output, token_total, cache_read_tokens, cache_write_tokens,
			cache_creation_tokens, reasoning_tokens, estimated_cost_usd, actual_cost_usd, cost_usd,
			model, run_id, run_status, created_at, updated_at
		)
		VALUES (NULLIF($1,''),$2,$3,$4,$5,$6,NULLIF($7,''),$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27,$28,$29,$29)
		ON CONFLICT (agent_id, session_id) DO UPDATE SET
			node_id = COALESCE(EXCLUDED.node_id, agent_sessions.node_id),
			session_name = COALESCE(NULLIF(EXCLUDED.session_name, ''), agent_sessions.session_name),
			agent_type = EXCLUDED.agent_type,
			native_id = COALESCE(NULLIF(EXCLUDED.native_id, ''), agent_sessions.native_id),
			primary_project_id = COALESCE(
				NULLIF(agent_sessions.primary_project_id, ''),
				NULLIF(EXCLUDED.primary_project_id, '')
			),
			preview = EXCLUDED.preview,
			workspace_roots = EXCLUDED.workspace_roots,
			source = COALESCE(NULLIF(agent_sessions.source, ''), EXCLUDED.source),
			status = EXCLUDED.status,
			current_task = EXCLUDED.current_task,
			last_message_at = GREATEST(agent_sessions.last_message_at, EXCLUDED.last_message_at),
			last_user_message_at = GREATEST(agent_sessions.last_user_message_at, EXCLUDED.last_user_message_at),
			message_count = EXCLUDED.message_count,
			token_input = EXCLUDED.token_input,
			token_output = EXCLUDED.token_output,
			token_total = EXCLUDED.token_total,
			cache_read_tokens = EXCLUDED.cache_read_tokens,
			cache_write_tokens = EXCLUDED.cache_write_tokens,
			cache_creation_tokens = EXCLUDED.cache_creation_tokens,
			reasoning_tokens = EXCLUDED.reasoning_tokens,
			estimated_cost_usd = EXCLUDED.estimated_cost_usd,
			actual_cost_usd = EXCLUDED.actual_cost_usd,
			cost_usd = EXCLUDED.cost_usd,
			model = EXCLUDED.model,
			run_id = EXCLUDED.run_id,
			run_status = EXCLUDED.run_status,
			updated_at = EXCLUDED.updated_at
		`,
		nodeID,
		agentID,
		input.SessionID,
		input.SessionName,
		input.AgentType,
		input.NativeID,
		primaryProjectID,
		input.Preview,
		roots,
		input.Source,
		defaultSessionStatus(input.Status),
		input.CurrentTask,
		input.LastMessageAt,
		input.LastUserMessageAt,
		input.MessageCount,
		input.TokenUsage.Input,
		input.TokenUsage.Output,
		input.TokenUsage.Total,
		input.TokenUsage.CacheRead,
		input.TokenUsage.CacheWrite,
		input.TokenUsage.CacheCreation,
		input.TokenUsage.Reasoning,
		input.TokenUsage.EstimatedCostUSD,
		input.TokenUsage.ActualCostUSD,
		input.TokenUsage.CostUSD,
		input.Model,
		input.RunID,
		input.RunStatus,
		now,
	)
	return err
}

func (s *PostgresStore) insertMailbox(
	ctx context.Context,
	userID string,
	ownerUserID string,
	nodeID string,
	agentID string,
	sessionID string,
	message string,
	messageType string,
	payload []byte,
	status string,
	direction string,
	createdAt time.Time,
) (MailboxMessage, error) {
	messageID, err := newSecret("msg")
	if err != nil {
		return MailboxMessage{}, err
	}
	message, _ = postgresSafeText(message)
	if len(payload) > 0 {
		payload, _, err = postgresSafeJSON(payload)
		if err != nil {
			return MailboxMessage{}, err
		}
	}
	row := s.db.QueryRowContext(ctx, `
		INSERT INTO mailbox (
			message_id, user_id, owner_user_id, node_id, agent_id, session_id, message, message_type,
			payload, status, direction, created_at, expires_at
		)
		VALUES ($1,$2,$3,NULLIF($4,''),$5,NULLIF($6,''),$7,$8,$9,$10,$11,$12,$13)
		RETURNING `+mailboxReturningSQL+`
	`, messageID, userID, ownerUserID, nodeID, agentID, sessionID, message, messageType, payload,
		status, direction, createdAt, expiresAt(createdAt, messageType))
	msg, err := scanMailbox(row)
	if err != nil {
		return MailboxMessage{}, err
	}
	if err := s.saveMailboxHistory(ctx, msg); err != nil {
		return MailboxMessage{}, err
	}
	return msg, nil
}

func jsonOrNil(v any) any {
	data, err := json.Marshal(v)
	if err != nil || string(data) == "null" || string(data) == "{}" {
		return nil
	}
	normalized, _, err := postgresSafeJSON(data)
	if err != nil {
		return nil
	}
	return []byte(normalized)
}

func jsonDefault(raw json.RawMessage, fallback string) []byte {
	if len(raw) == 0 || !json.Valid(raw) {
		return []byte(fallback)
	}
	normalized, _, err := postgresSafeJSON(raw)
	if err != nil {
		return []byte(fallback)
	}
	return normalized
}

func jsonOrDefault(v any, fallback string) []byte {
	data, err := json.Marshal(v)
	if err != nil || string(data) == "null" {
		return []byte(fallback)
	}
	normalized, _, err := postgresSafeJSON(data)
	if err != nil {
		return []byte(fallback)
	}
	return normalized
}

func secretVersionLookup(
	secret Secret,
	versionSelector string,
) (string, []any, error) {
	selector := strings.TrimSpace(versionSelector)
	if selector == "" || selector == "latest" {
		if secret.CurrentVersionID == "" {
			return "", nil, ErrNotFound
		}
		return secretVersionSelectSQL + `
			WHERE version_id = $1 AND secret_id = $2
		`, []any{secret.CurrentVersionID, secret.SecretID}, nil
	}
	selector = strings.TrimPrefix(selector, "version:")
	versionNumber, err := strconv.ParseInt(selector, 10, 64)
	if err != nil || versionNumber <= 0 {
		return "", nil, ErrNotFound
	}
	return secretVersionSelectSQL + `
		WHERE secret_id = $1 AND version_number = $2
	`, []any{secret.SecretID, versionNumber}, nil
}

func (s *PostgresStore) findIdempotentSecretVersion(
	ctx context.Context,
	tx *sql.Tx,
	secretID string,
	nodeID string,
	agentID string,
	idempotencyKey string,
) (SecretVersion, bool, error) {
	version, err := scanSecretVersion(tx.QueryRowContext(ctx, secretVersionSelectSQL+`
		WHERE secret_id = $1
			AND created_by_node_id = $2
			AND created_by_agent_id = $3
			AND idempotency_key = $4
	`, secretID, nodeID, agentID, idempotencyKey))
	if err != nil {
		return SecretVersion{}, false, err
	}
	var currentVersionID string
	if err := tx.QueryRowContext(ctx, `
		SELECT COALESCE(current_version_id, '') FROM secrets WHERE secret_id = $1
	`, secretID).Scan(&currentVersionID); err != nil {
		return SecretVersion{}, false, mapSQLError(err)
	}
	return version, currentVersionID == version.VersionID, nil
}

func defaultApprovalDomain(v string) string {
	if strings.TrimSpace(v) == "" {
		return "agent_action"
	}
	return v
}

func defaultApprovalRiskLevel(v string) string {
	if strings.TrimSpace(v) == "" {
		return "unknown"
	}
	return v
}

func approvalDecisionGrant(
	approval AgentApproval,
	req ApprovalDecisionRequest,
) (string, string, string, string, string, error) {
	switch req.DecisionOption {
	case "deny":
		return "deny", "once", approval.RequestNodeID, approval.RequestAgentID,
			approval.RequestSessionID, nil
	case "allow_once":
		return "allow", "once", approval.RequestNodeID, approval.RequestAgentID,
			approval.RequestSessionID, nil
	case "allow_for_this_agent":
		return "allow", "agent", approval.RequestNodeID, approval.RequestAgentID, "*", nil
	case "allow_for_this_node":
		return "allow", "node", approval.RequestNodeID, "*", "*", nil
	case "allow_always_on_all_agents":
		return "allow", "across_all_agents", "*", "*", "*", nil
	default:
		if req.GrantNodeID == "" && req.GrantAgentID == "" && req.GrantSessionID == "" {
			return "", "", "", "", "", ErrConflict
		}
		return "allow", "custom", req.GrantNodeID, req.GrantAgentID, req.GrantSessionID, nil
	}
}
