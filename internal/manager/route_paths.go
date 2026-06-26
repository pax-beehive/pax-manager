package manager

const (
	routeLegacySessionHistory = "/api/user/agents/:agentId/sessions/:sessionId/history"
	routeSessionHistory       = "/api/v1/user/:user_id/agents/:agent_id/sessions/:session_id/history"

	routeCreateKnowledgeCapsule  = "/api/v1/user/:user_id/sessions/:session_id/knowledge-capsules"
	routeListKnowledgeCapsules   = "/api/v1/user/:user_id/knowledge-capsules"
	routeGetKnowledgeCapsule     = "/api/v1/user/:user_id/knowledge-capsules/:capsule_id"
	routeArchiveKnowledgeCapsule = "/api/v1/user/:user_id/knowledge-capsules/:capsule_id/archive"
	routeInjectKnowledgeCapsule  = "/api/v1/user/:user_id/sessions/:session_id/knowledge-injections"
	routeListKnowledgeInjections = "/api/v1/user/:user_id/sessions/:session_id/knowledge-injections"

	routeCreateEnvelope  = "/api/v1/user/:user_id/envelopes"
	routeListEnvelopes   = "/api/v1/user/:user_id/envelopes"
	routeGetEnvelope     = "/api/v1/user/:user_id/envelopes/:envelope_id"
	routeAcceptEnvelope  = "/api/v1/user/:user_id/envelopes/:envelope_id/accept"
	routeArchiveEnvelope = "/api/v1/user/:user_id/envelopes/:envelope_id/archive"

	routeCreateFriend = "/api/v1/user/:user_id/friends"
	routeListFriends  = "/api/v1/user/:user_id/friends"
	routeGetFriend    = "/api/v1/user/:user_id/friends/:friend_id"
	routeAcceptFriend = "/api/v1/user/:user_id/friends/:friend_id/accept"
	routeAliasFriend  = "/api/v1/user/:user_id/friends/:friend_id/alias"
	routeRemoveFriend = "/api/v1/user/:user_id/friends/:friend_id/remove"
	routeBlockFriend  = "/api/v1/user/:user_id/friends/:friend_id/block"

	routeOpenAPI     = "/openapi"
	routeOpenAPIJSON = "/openapi.json"
	routeEcho        = "/api/echo"

	routeStartNodeRegistration   = "/api/v1/node/registration/start"
	routePollNodeRegistration    = "/api/v1/node/registration/poll"
	routeGetNodeRegistration     = "/api/v1/user/:user_id/node-registrations/:pair_code"
	routeApproveNodeRegistration = "/api/v1/user/:user_id/node-registrations/:pair_code/approve"

	routeStartPaxlDeviceLogin   = "/api/v1/paxl/device-login/start"
	routePollPaxlDeviceLogin    = "/api/v1/paxl/device-login/poll"
	routeApprovePaxlDeviceLogin = "/api/v1/user/:user_id/paxl/device-logins/:user_code/approve"

	routeLegacyAgentWS        = "/api/agent/ws"
	routeNodeControlTunnel    = "/api/v1/node/control"
	routeAgentACPTunnel       = "/api/v1/agent/tunnel"
	routeUserACPTunnel        = "/api/v1/user/:userID/agents/:agentID/tunnel"
	routeUserSessionACPTunnel = "/api/v1/user/:userID/agents/:agentID/sessions/:sessionID/tunnel"
	routeUserConversation     = "/api/v1/user/:userID/nodes/:nodeID/agents/:agentID/conversation"

	routeDownloadGenericArtifact = "/api/v1/public/artifacts/download"
	routeDownloadPaxdArtifact    = "/api/v1/public/paxd/download"
	routeDownloadPaxlArtifact    = "/api/v1/public/paxl/download"
	routeDownloadPaxdInstaller   = "/api/v1/public/paxd/install.sh"
	routeDownloadPaxlInstaller   = "/api/v1/public/paxl/install.sh"
	routePublishGenericArtifact  = "/api/v1/admin/artifacts"
	routePublishPaxdArtifact     = "/api/v1/admin/paxd/artifacts"

	openAPISessionHistoryPath       = "/api/v1/user/{user_id}/agents/{agent_id}/sessions/{session_id}/history"
	openAPIUserACPTunnelPath        = "/api/v1/user/{user_id}/agents/{agent_id}/tunnel"
	openAPIUserSessionACPTunnelPath = "/api/v1/user/{user_id}/agents/{agent_id}/sessions/{session_id}/tunnel"
)
