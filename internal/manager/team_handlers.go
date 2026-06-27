package manager

import (
	"context"

	"github.com/cloudwego/hertz/pkg/app"
)

func CreateTeam(c context.Context, ctx *app.RequestContext) {
	var req CreateTeamRequest
	decodeBody(ctx, &req)
	status, data, err := serviceFromContext(ctx).userapi.CreateTeam(
		c,
		requestMetadata(ctx),
		req,
	)
	writeEndpointResult(ctx, status, data, err)
}

func ListTeams(c context.Context, ctx *app.RequestContext) {
	status, data, err := serviceFromContext(ctx).userapi.ListTeams(
		c,
		requestMetadata(ctx),
	)
	writeEndpointResult(ctx, status, data, err)
}

func GetTeam(c context.Context, ctx *app.RequestContext) {
	status, data, err := serviceFromContext(ctx).userapi.GetTeam(
		c,
		requestMetadata(ctx),
		ctx.Param("team_id"),
	)
	writeEndpointResult(ctx, status, data, err)
}

func ArchiveTeam(c context.Context, ctx *app.RequestContext) {
	status, data, err := serviceFromContext(ctx).userapi.ArchiveTeam(
		c,
		requestMetadata(ctx),
		ctx.Param("team_id"),
	)
	writeEndpointResult(ctx, status, data, err)
}

func ListTeamMembers(c context.Context, ctx *app.RequestContext) {
	status, data, err := serviceFromContext(ctx).userapi.ListTeamMembers(
		c,
		requestMetadata(ctx),
		ctx.Param("team_id"),
	)
	writeEndpointResult(ctx, status, data, err)
}

func UpdateTeamMemberRole(c context.Context, ctx *app.RequestContext) {
	var req UpdateTeamMemberRoleRequest
	decodeBody(ctx, &req)
	status, data, err := serviceFromContext(ctx).userapi.UpdateTeamMemberRole(
		c,
		requestMetadata(ctx),
		ctx.Param("team_id"),
		ctx.Param("member_user_id"),
		req,
	)
	writeEndpointResult(ctx, status, data, err)
}

func RemoveTeamMember(c context.Context, ctx *app.RequestContext) {
	status, data, err := serviceFromContext(ctx).userapi.RemoveTeamMember(
		c,
		requestMetadata(ctx),
		ctx.Param("team_id"),
		ctx.Param("member_user_id"),
	)
	writeEndpointResult(ctx, status, data, err)
}

func LeaveTeam(c context.Context, ctx *app.RequestContext) {
	status, data, err := serviceFromContext(ctx).userapi.LeaveTeam(
		c,
		requestMetadata(ctx),
		ctx.Param("team_id"),
	)
	writeEndpointResult(ctx, status, data, err)
}

func CreateTeamInvite(c context.Context, ctx *app.RequestContext) {
	var req CreateTeamInviteRequest
	decodeBody(ctx, &req)
	status, data, err := serviceFromContext(ctx).userapi.CreateTeamInvite(
		c,
		requestMetadata(ctx),
		ctx.Param("team_id"),
		req,
	)
	writeEndpointResult(ctx, status, data, err)
}

func CancelTeamInvite(c context.Context, ctx *app.RequestContext) {
	status, data, err := serviceFromContext(ctx).userapi.CancelTeamInvite(
		c,
		requestMetadata(ctx),
		ctx.Param("team_id"),
		ctx.Param("invite_id"),
	)
	writeEndpointResult(ctx, status, data, err)
}

func ListTeamInvites(c context.Context, ctx *app.RequestContext) {
	status, data, err := serviceFromContext(ctx).userapi.ListTeamInvites(
		c,
		requestMetadata(ctx),
	)
	writeEndpointResult(ctx, status, data, err)
}

func AcceptTeamInvite(c context.Context, ctx *app.RequestContext) {
	status, data, err := serviceFromContext(ctx).userapi.AcceptTeamInvite(
		c,
		requestMetadata(ctx),
		ctx.Param("invite_id"),
	)
	writeEndpointResult(ctx, status, data, err)
}

func DeclineTeamInvite(c context.Context, ctx *app.RequestContext) {
	status, data, err := serviceFromContext(ctx).userapi.DeclineTeamInvite(
		c,
		requestMetadata(ctx),
		ctx.Param("invite_id"),
	)
	writeEndpointResult(ctx, status, data, err)
}

func ListTeamAgents(c context.Context, ctx *app.RequestContext) {
	status, data, err := serviceFromContext(ctx).userapi.ListTeamAgents(
		c,
		requestMetadata(ctx),
		ctx.Param("team_id"),
	)
	writeEndpointResult(ctx, status, data, err)
}

func ListTeamAuditEvents(c context.Context, ctx *app.RequestContext) {
	status, data, err := serviceFromContext(ctx).userapi.ListTeamAuditEvents(
		c,
		requestMetadata(ctx),
		ctx.Param("team_id"),
		queryInt(ctx, "limit"),
	)
	writeEndpointResult(ctx, status, data, err)
}

func AddTeamAgent(c context.Context, ctx *app.RequestContext) {
	var req AddTeamAgentRequest
	decodeBody(ctx, &req)
	status, data, err := serviceFromContext(ctx).userapi.AddTeamAgent(
		c,
		requestMetadata(ctx),
		ctx.Param("team_id"),
		req,
	)
	writeEndpointResult(ctx, status, data, err)
}

func RemoveTeamAgent(c context.Context, ctx *app.RequestContext) {
	status, data, err := serviceFromContext(ctx).userapi.RemoveTeamAgent(
		c,
		requestMetadata(ctx),
		ctx.Param("team_id"),
		ctx.Param("agent_id"),
	)
	writeEndpointResult(ctx, status, data, err)
}
