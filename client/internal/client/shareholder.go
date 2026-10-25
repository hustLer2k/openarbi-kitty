package client

import (
	"context"
	"errors"
)

func (c *Client) PublishShareholder(ctx context.Context, in PublishRequest) (PublishResponse, error) {
	var out PublishResponse
	if in.SourceKind != "local" {
		return out, errors.New("normal shareholder API requires a local source")
	}
	if err := Validate(in); err != nil {
		return out, err
	}
	if err := c.json(ctx, "POST", "/v1/shareholder/updates", in, &out); err != nil {
		return out, err
	}
	if (out.Status != "sent" && out.Status != "suppressed") || out.MessageID <= 0 || out.ChatID == 0 {
		return PublishResponse{}, &UncertainError{Cause: errors.New("invalid shareholder acknowledgement")}
	}
	return out, nil
}
