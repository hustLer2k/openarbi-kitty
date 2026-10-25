package client

import (
	"context"
	"errors"
	"net/url"
)

// DesktopAllocation describes the versioned allocation protocol. A missing or
// incompatible capability must never fall back to ordinary room creation.
type DesktopAllocation struct {
	Version        int `json:"version"`
	MaxActiveRooms int `json:"max_active_rooms"`
}
type ClaimRequest struct {
	DeviceID       string `json:"device_id"`
	SessionID      string `json:"session_id"`
	IdempotencyKey string `json:"idempotency_key"`
}
type RoomBinding struct {
	DeviceID  string `json:"device_id"`
	SessionID string `json:"session_id"`
	Room      Room   `json:"room"`
	Ready     bool   `json:"ready"`
}

func (c *Client) ClaimRoom(ctx context.Context, in ClaimRequest) (RoomBinding, error) {
	var out RoomBinding
	if in.DeviceID == "" || in.SessionID == "" || in.IdempotencyKey == "" {
		return out, errors.New("device, session and idempotency key are required")
	}
	err := c.json(ctx, "POST", "/v1/rooms/claim", in, &out)
	return out, err
}
func (c *Client) RoomBinding(ctx context.Context, device, session string) (RoomBinding, error) {
	var out RoomBinding
	if device == "" || session == "" {
		return out, errors.New("device and session are required")
	}
	err := c.json(ctx, "GET", "/v1/rooms/bindings/"+url.PathEscape(device)+"/"+url.PathEscape(session), nil, &out)
	return out, err
}

// DesktopCapabilities is separate from the base CLI handshake so older CLI
// consumers do not need to understand desktop allocation.
func (c *Client) DesktopCapabilities(ctx context.Context) (DesktopAllocation, error) {
	var out struct {
		Version           string            `json:"version"`
		DesktopAllocation DesktopAllocation `json:"desktop_allocation"`
	}
	if err := c.json(ctx, "GET", "/v1/capabilities", nil, &out); err != nil {
		return DesktopAllocation{}, err
	}
	if out.Version != ProtocolVersion || out.DesktopAllocation.Version != 1 || out.DesktopAllocation.MaxActiveRooms < 1 {
		return DesktopAllocation{}, errors.New("server does not support desktop allocation version 1; update the OpenArbi server and client")
	}
	return out.DesktopAllocation, nil
}

// CloseIdleConnections releases idle sockets owned by a short-lived API consumer.
func (c *Client) CloseIdleConnections() { c.http.CloseIdleConnections() }
