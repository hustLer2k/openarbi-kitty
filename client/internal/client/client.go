// Package client implements the authenticated OpenArbi v1 protocol. Mutations
// are sent once: a lost acknowledgement is returned to the caller for explicit
// retry with the original idempotency key.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const maxResponse = 16 << 20

// UncertainError means the command may have been accepted. Retain the original
// body/key and deliberately retry it; never generate a new key automatically.
type UncertainError struct{ Cause error }

func (e *UncertainError) Error() string {
	return "acknowledgement lost; use /retry with the original request"
}
func (e *UncertainError) Unwrap() error { return e.Cause }

type ResponseError struct {
	Status int
	API    *APIError
}

func (e *ResponseError) Error() string { return e.API.Error() }
func (e *ResponseError) Unwrap() error { return e.API }

type Client struct {
	base  string
	token string
	http  *http.Client
}

func NormalizeOrigin(base string) (string, error) {
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("API URL must be an HTTP(S) origin without credentials, query, or fragment")
	}
	if u.Scheme == "http" && u.Hostname() != "localhost" && u.Hostname() != "127.0.0.1" && u.Hostname() != "::1" {
		return "", errors.New("unencrypted HTTP is restricted to loopback")
	}
	return strings.TrimRight(base, "/"), nil
}
func New(base, token string, transport *http.Client) (*Client, error) {
	normalized, err := NormalizeOrigin(base)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(token) == "" || strings.ContainsAny(token, "\r\n") {
		return nil, errors.New("a valid access token is required")
	}
	if transport == nil {
		transport = &http.Client{}
	}
	// Redirects must never carry a token or replay a mutation to a second origin.
	h := *transport
	h.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{normalized, token, &h}, nil
}
func (c *Client) request(ctx context.Context, method, path string, body any) (*http.Response, error) {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, r)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("X-OpenArbi-Version", ProtocolVersion)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		if method == http.MethodPost {
			return nil, &UncertainError{err}
		}
		return nil, errors.New("API connection failed")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		var api APIError
		if json.NewDecoder(io.LimitReader(resp.Body, maxResponse)).Decode(&api) != nil || api.Code == "" {
			responseError := &ResponseError{Status: resp.StatusCode, API: &APIError{Code: fmt.Sprintf("http_%d", resp.StatusCode), Message: "API request failed"}}
			if method == http.MethodPost && resp.StatusCode >= 500 {
				return nil, &UncertainError{responseError}
			}
			return nil, responseError
		}
		responseError := &ResponseError{Status: resp.StatusCode, API: &api}
		if method == http.MethodPost && resp.StatusCode >= 500 {
			return nil, &UncertainError{responseError}
		}
		return nil, responseError
	}
	return resp, nil
}
func (c *Client) json(ctx context.Context, method, path string, body, out any) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	resp, err := c.request(ctx, method, path, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse+1))
	if err == nil && len(b) > maxResponse {
		err = errors.New("response exceeds limit")
	}
	if err == nil {
		err = json.Unmarshal(b, out)
	}
	if err != nil {
		if method == http.MethodPost {
			return &UncertainError{err}
		}
		return errors.New("invalid API response")
	}
	return nil
}
func (c *Client) Capabilities(ctx context.Context) (Capabilities, error) {
	var out Capabilities
	err := c.json(ctx, "GET", "/v1/capabilities", nil, &out)
	if err == nil && out.Version != ProtocolVersion {
		err = fmt.Errorf("unsupported API version %q", out.Version)
	}
	return out, err
}
