package manager

import (
	"context"

	"github.com/cloudwego/hertz/pkg/app"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func CreateFriend(c context.Context, ctx *app.RequestContext) {
	var req CreateFriendRequest
	decodeBody(ctx, &req)
	status, data, err := serviceFromContext(ctx).userapi.CreateFriend(
		c,
		requestMetadata(ctx),
		req,
	)
	writeEndpointResult(ctx, status, data, err)
}

func ListFriends(c context.Context, ctx *app.RequestContext) {
	status, data, err := serviceFromContext(ctx).userapi.ListFriends(
		c,
		requestMetadata(ctx),
		domain.ListFriendsFilter{
			Status:    string(ctx.QueryArgs().Peek("status")),
			Direction: string(ctx.QueryArgs().Peek("direction")),
			Alias:     string(ctx.QueryArgs().Peek("alias")),
			Limit:     queryInt(ctx, "limit"),
			Cursor:    string(ctx.QueryArgs().Peek("cursor")),
		},
	)
	writeEndpointResult(ctx, status, data, err)
}

func GetFriend(c context.Context, ctx *app.RequestContext) {
	status, data, err := serviceFromContext(ctx).userapi.GetFriend(
		c,
		requestMetadata(ctx),
		ctx.Param("friend_id"),
	)
	writeEndpointResult(ctx, status, data, err)
}

func AcceptFriend(c context.Context, ctx *app.RequestContext) {
	var req AcceptFriendRequest
	decodeBody(ctx, &req)
	status, data, err := serviceFromContext(ctx).userapi.AcceptFriend(
		c,
		requestMetadata(ctx),
		ctx.Param("friend_id"),
		req,
	)
	writeEndpointResult(ctx, status, data, err)
}

func RemoveFriend(c context.Context, ctx *app.RequestContext) {
	status, data, err := serviceFromContext(ctx).userapi.RemoveFriend(
		c,
		requestMetadata(ctx),
		ctx.Param("friend_id"),
	)
	writeEndpointResult(ctx, status, data, err)
}

func BlockFriend(c context.Context, ctx *app.RequestContext) {
	status, data, err := serviceFromContext(ctx).userapi.BlockFriend(
		c,
		requestMetadata(ctx),
		ctx.Param("friend_id"),
	)
	writeEndpointResult(ctx, status, data, err)
}
