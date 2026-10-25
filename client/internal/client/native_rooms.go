package client

import (
	"context"
	"errors"
	"net/url"
)

func roomPath(id string) string { return "/v1/rooms/" + url.PathEscape(id) }
func (c *Client) Rooms(ctx context.Context) ([]Room, error) {
	var out []Room
	err := c.json(ctx, "GET", "/v1/rooms", nil, &out)
	return out, err
}
func (c *Client) CreateRoom(ctx context.Context, in CreateRequest) (Room, error) {
	var out Room
	if in.IdempotencyKey == "" {
		return out, errors.New("idempotency key required")
	}
	err := c.json(ctx, "POST", "/v1/rooms", in, &out)
	return out, err
}
func (c *Client) Room(ctx context.Context, id string) (Room, error) {
	var out Room
	err := c.json(ctx, "GET", roomPath(id), nil, &out)
	return out, err
}
func (c *Client) AttachRoom(ctx context.Context, id string) (AttachInfo, error) {
	var out AttachInfo
	err := c.json(ctx, "POST", roomPath(id)+"/attach", struct{}{}, &out)
	return out, err
}
func (c *Client) StopRoom(ctx context.Context, id string) (Room, error) {
	var out Room
	err := c.json(ctx, "POST", roomPath(id)+"/stop", struct{}{}, &out)
	return out, err
}
func (c *Client) ResumeRoom(ctx context.Context, id string) (Room, error) {
	var out Room
	err := c.json(ctx, "POST", roomPath(id)+"/resume", struct{}{}, &out)
	return out, err
}
func (c *Client) RoomMembers(ctx context.Context, id string) ([]Member, error) {
	var out []Member
	err := c.json(ctx, "GET", roomPath(id)+"/members", nil, &out)
	return out, err
}
func (c *Client) AddRoomMember(ctx context.Context, id, identity string) ([]Member, error) {
	var out []Member
	err := c.json(ctx, "POST", roomPath(id)+"/members", struct {
		IdentityID string `json:"identity_id"`
	}{identity}, &out)
	return out, err
}
func (c *Client) RemoveRoomMember(ctx context.Context, id, identity string) ([]Member, error) {
	var out []Member
	err := c.json(ctx, "DELETE", roomPath(id)+"/members/"+url.PathEscape(identity), nil, &out)
	return out, err
}
