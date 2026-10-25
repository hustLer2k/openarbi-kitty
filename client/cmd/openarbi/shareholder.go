package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"

	"github.com/hustLer2k/openarbi-kitty/client/internal/client"
)

const maxShareholderInput = 32 << 10

// Exit code 3 and this constant stderr object are the sender's narrow retry
// contract. Other command failures retain exit code 1 and remain uncertain.
const shareholderNotDispatchedJSON = `{"error":"shareholder_not_dispatched","retryable":true}`

type shareholderNotDispatchedError struct{ cause error }

func (e *shareholderNotDispatchedError) Error() string { return e.cause.Error() }
func (e *shareholderNotDispatchedError) Unwrap() error { return e.cause }

type shareholderPublisher interface {
	PublishShareholder(context.Context, client.PublishRequest) (client.PublishResponse, error)
}

func runShareholder(ctx context.Context, api shareholderPublisher, args []string, stdin io.Reader, stdout io.Writer) error {
	if len(args) != 1 || args[0] != "publish" {
		return &shareholderNotDispatchedError{errors.New("usage: openarbi shareholder publish < update.json")}
	}
	b, err := io.ReadAll(io.LimitReader(stdin, maxShareholderInput+1))
	if err != nil || len(b) > maxShareholderInput {
		return &shareholderNotDispatchedError{errors.New("shareholder input unavailable or exceeds 32 KiB")}
	}
	var in client.PublishRequest
	// Reject trailing values and unknown fields before contacting the API.
	if err := decodeShareholder(b, &in); err != nil {
		return &shareholderNotDispatchedError{err}
	}
	if in.SourceKind != "local" {
		return &shareholderNotDispatchedError{errors.New("shareholder CLI requires a local source")}
	}
	if err := client.Validate(in); err != nil {
		return &shareholderNotDispatchedError{err}
	}
	out, err := api.PublishShareholder(ctx, in)
	if err != nil {
		// Only explicit authentication rejection is safe after an HTTP call.
		// 5xx responses, disconnects and malformed acknowledgements stay unknown.
		if _, uncertain := errors.AsType[*client.UncertainError](err); !uncertain {
			if response, ok := errors.AsType[*client.ResponseError](err); ok && (response.Status == 401 || response.Status == 403) {
				return &shareholderNotDispatchedError{err}
			}
		}
		return err
	}
	return json.NewEncoder(stdout).Encode(out)
}

func decodeShareholder(data []byte, out *client.PublishRequest) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(out) != nil {
		return errors.New("invalid shareholder JSON input")
	}
	if decoder.Decode(new(any)) != io.EOF {
		return errors.New("shareholder input must contain one JSON object")
	}
	return nil
}
