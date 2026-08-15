package storage

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func (s *PostgresStore) UpsertAgentRuntimeIdentity(
	ctx context.Context,
	identity domain.AgentRuntimeIdentity,
) error {
	return upsertAgentRuntimeIdentity(ctx, s.db, identity)
}

func upsertAgentRuntimeIdentity(
	ctx context.Context,
	exec sqlExecer,
	identity domain.AgentRuntimeIdentity,
) error {
	return writeAgentRuntimeIdentity(ctx, exec, identity, false)
}

func replaceAgentRuntimeIdentity(
	ctx context.Context,
	exec sqlExecer,
	identity domain.AgentRuntimeIdentity,
) error {
	return writeAgentRuntimeIdentity(ctx, exec, identity, true)
}

func writeAgentRuntimeIdentity(
	ctx context.Context,
	exec sqlExecer,
	identity domain.AgentRuntimeIdentity,
	authoritative bool,
) error {
	query := `
		INSERT INTO agent_runtime_identities (
			agent_id, report_epoch, schema_version, connection_id,
			report_generation, protocol_version,
			acp_agent_name, acp_agent_title, acp_agent_version,
			runtime_name, runtime_version, runtime_build, runtime_channel,
			identity_fingerprint, command_fingerprint, client_profile_hash, worker_result_hash,
			pool_consistency, observed_at
		) VALUES (
			$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19
		)
		ON CONFLICT (agent_id) DO UPDATE SET
			report_epoch = EXCLUDED.report_epoch,
			schema_version = EXCLUDED.schema_version,
			connection_id = EXCLUDED.connection_id,
			report_generation = EXCLUDED.report_generation,
			protocol_version = EXCLUDED.protocol_version,
			acp_agent_name = EXCLUDED.acp_agent_name,
			acp_agent_title = EXCLUDED.acp_agent_title,
			acp_agent_version = EXCLUDED.acp_agent_version,
			runtime_name = EXCLUDED.runtime_name,
			runtime_version = EXCLUDED.runtime_version,
			runtime_build = EXCLUDED.runtime_build,
			runtime_channel = EXCLUDED.runtime_channel,
			identity_fingerprint = EXCLUDED.identity_fingerprint,
			command_fingerprint = EXCLUDED.command_fingerprint,
			client_profile_hash = EXCLUDED.client_profile_hash,
			worker_result_hash = EXCLUDED.worker_result_hash,
			pool_consistency = EXCLUDED.pool_consistency,
			observed_at = EXCLUDED.observed_at`
	if !authoritative {
		query += `
		WHERE agent_runtime_identities.report_epoch <> EXCLUDED.report_epoch
			OR agent_runtime_identities.connection_id <> EXCLUDED.connection_id
			OR agent_runtime_identities.report_generation <= EXCLUDED.report_generation`
	}
	_, err := exec.ExecContext(ctx, query,
		identity.AgentID, identity.ReportEpoch, identity.SchemaVersion,
		identity.ConnectionID, identity.ReportGeneration, identity.ProtocolVersion,
		identity.ACPAgentName, identity.ACPAgentTitle, identity.ACPAgentVersion,
		identity.RuntimeName, identity.RuntimeVersion, identity.RuntimeBuild,
		identity.RuntimeChannel, identity.IdentityFingerprint,
		identity.CommandFingerprint, identity.ClientProfileHash, identity.WorkerResultHash,
		identity.PoolConsistency, identity.ObservedAt)
	return mapSQLError(err)
}

func (s *PostgresStore) GetAgentRuntimeIdentity(
	ctx context.Context,
	agentID string,
) (domain.AgentRuntimeIdentity, error) {
	var identity domain.AgentRuntimeIdentity
	err := s.db.QueryRowContext(ctx, `
		SELECT agent_id, report_epoch, schema_version, connection_id, report_generation,
			protocol_version, acp_agent_name, acp_agent_title, acp_agent_version,
			runtime_name, runtime_version, runtime_build, runtime_channel,
			identity_fingerprint, command_fingerprint, client_profile_hash, worker_result_hash,
			pool_consistency, observed_at
		FROM agent_runtime_identities
		WHERE agent_id = $1
	`, agentID).Scan(
		&identity.AgentID, &identity.ReportEpoch, &identity.SchemaVersion, &identity.ConnectionID,
		&identity.ReportGeneration, &identity.ProtocolVersion,
		&identity.ACPAgentName, &identity.ACPAgentTitle, &identity.ACPAgentVersion,
		&identity.RuntimeName, &identity.RuntimeVersion, &identity.RuntimeBuild,
		&identity.RuntimeChannel, &identity.IdentityFingerprint,
		&identity.CommandFingerprint, &identity.ClientProfileHash, &identity.WorkerResultHash,
		&identity.PoolConsistency, &identity.ObservedAt,
	)
	if err != nil {
		return domain.AgentRuntimeIdentity{}, mapSQLError(err)
	}
	return identity, nil
}

func (s *PostgresStore) InsertPermissionProfile(
	ctx context.Context,
	profile domain.AgentPermissionProfile,
) error {
	definition, err := json.Marshal(profile.Definition)
	if err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO agent_permission_profiles (
			profile_id, revision, status, owner_user_id, agent_type,
			acp_agent_name, acp_agent_version_constraint, runtime_name,
			runtime_version_constraint, priority, definition, source, created_at
		) VALUES (
			$1,$2,$3,NULLIF($4,''),$5,$6,$7,$8,$9,$10,$11,$12,$13
		)
		ON CONFLICT (profile_id, revision) DO NOTHING
	`, profile.ProfileID, profile.Revision, profile.Status, profile.OwnerUserID,
		profile.AgentType, profile.ACPAgentName, profile.ACPAgentVersionConstraint,
		profile.RuntimeName, profile.RuntimeVersionConstraint, profile.Priority,
		definition, profile.Source, profile.CreatedAt)
	if err != nil {
		return mapSQLError(err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return ErrConflict
	}
	return nil
}

func (s *PostgresStore) ListActivePermissionProfiles(
	ctx context.Context,
	ownerUserID string,
	agentType string,
) ([]domain.AgentPermissionProfile, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT profile_id, revision, status, owner_user_id, agent_type,
			acp_agent_name, acp_agent_version_constraint, runtime_name,
			runtime_version_constraint, priority, definition, source, created_at
		FROM agent_permission_profiles
		WHERE status = 'active'
			AND (owner_user_id IS NULL OR owner_user_id = $1)
			AND (agent_type = '' OR LOWER(agent_type) = LOWER($2))
		ORDER BY (owner_user_id IS NOT NULL) DESC, priority DESC, profile_id, revision DESC
	`, ownerUserID, agentType)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	profiles := make([]domain.AgentPermissionProfile, 0)
	for rows.Next() {
		var profile domain.AgentPermissionProfile
		var owner sql.NullString
		var definition []byte
		if err := rows.Scan(
			&profile.ProfileID, &profile.Revision, &profile.Status, &owner,
			&profile.AgentType, &profile.ACPAgentName,
			&profile.ACPAgentVersionConstraint, &profile.RuntimeName,
			&profile.RuntimeVersionConstraint, &profile.Priority, &definition,
			&profile.Source, &profile.CreatedAt,
		); err != nil {
			return nil, err
		}
		profile.OwnerUserID = owner.String
		if err := json.Unmarshal(definition, &profile.Definition); err != nil {
			return nil, err
		}
		profiles = append(profiles, profile)
	}
	return profiles, rows.Err()
}

func (s *PostgresStore) UpsertPermissionObservation(
	ctx context.Context,
	observation domain.AgentPermissionObservation,
) (domain.AgentPermissionObservation, error) {
	catalog, err := json.Marshal(observation.Catalog)
	if err != nil {
		return domain.AgentPermissionObservation{}, err
	}
	err = s.db.QueryRowContext(ctx, `
		INSERT INTO agent_permission_observations (
			agent_id, identity_fingerprint, catalog_revision, catalog_hash,
			catalog, observed_at, expires_at
		) VALUES ($1,$2,1,$3,$4,$5,$6)
		ON CONFLICT (agent_id, identity_fingerprint) DO UPDATE SET
			catalog_revision = CASE
				WHEN agent_permission_observations.catalog_hash = EXCLUDED.catalog_hash
				THEN agent_permission_observations.catalog_revision
				ELSE agent_permission_observations.catalog_revision + 1
			END,
			catalog_hash = EXCLUDED.catalog_hash,
			catalog = EXCLUDED.catalog,
			observed_at = EXCLUDED.observed_at,
			expires_at = EXCLUDED.expires_at
		RETURNING catalog_revision
	`, observation.AgentID, observation.IdentityFingerprint,
		observation.CatalogHash, catalog, observation.ObservedAt,
		observation.ExpiresAt).Scan(&observation.CatalogRevision)
	if err != nil {
		return domain.AgentPermissionObservation{}, mapSQLError(err)
	}
	return observation, nil
}

func (s *PostgresStore) GetPermissionObservation(
	ctx context.Context,
	agentID string,
	identityFingerprint string,
) (domain.AgentPermissionObservation, error) {
	var observation domain.AgentPermissionObservation
	var catalog []byte
	err := s.db.QueryRowContext(ctx, `
		SELECT agent_id, identity_fingerprint, catalog_revision, catalog_hash,
			catalog, observed_at, expires_at
		FROM agent_permission_observations
		WHERE agent_id = $1 AND identity_fingerprint = $2
	`, agentID, identityFingerprint).Scan(
		&observation.AgentID, &observation.IdentityFingerprint,
		&observation.CatalogRevision, &observation.CatalogHash, &catalog,
		&observation.ObservedAt, &observation.ExpiresAt,
	)
	if err != nil {
		return domain.AgentPermissionObservation{}, mapSQLError(err)
	}
	if err := json.Unmarshal(catalog, &observation.Catalog); err != nil {
		return domain.AgentPermissionObservation{}, err
	}
	return observation, nil
}
