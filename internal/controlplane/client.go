// Package controlplane is the agent's HTTP/JSON client for the ShipMono
// control plane's /agent/v1 contract: register, poll, ack, events, heartbeat.
//
// It is the production counterpart to the Ruby simulator (bin/dev-agent) in the
// control-plane repo, which is the executable spec for this contract.
package controlplane

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// Client talks to one control plane. It is safe for sequential use by the poll
// loop; it is not designed for concurrent calls (the agent is single-threaded
// per the contract).
type Client struct {
	baseURL string
	httpc   *http.Client
	token   string // Bearer token; empty until set after register/load
}

// New returns a Client for the given control-plane base URL (e.g.
// "https://app.shipmono.dev"). A trailing slash is trimmed. Used for the
// unauthenticated pairing call and for bearer-only operation (dev/test).
func New(baseURL string) *Client {
	return newClient(baseURL, ipv4PreferringTransport())
}

// NewWithTLS returns a Client that speaks mutual TLS: it presents the client cert
// from tlsConfig and pins the control-plane CA in it (security architecture §4).
// Used for all steady-state calls against the mTLS endpoint after pairing.
func NewWithTLS(baseURL string, tlsConfig *tls.Config) *Client {
	t := ipv4PreferringTransport()
	t.TLSClientConfig = tlsConfig
	return newClient(baseURL, t)
}

func newClient(baseURL string, t *http.Transport) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		httpc: &http.Client{
			// Generous enough for slow networks, short enough that a hung
			// control plane doesn't wedge the loop forever.
			Timeout:   20 * time.Second,
			Transport: t,
		},
	}
}

// ipv4PreferringTransport dials the control plane over IPv4 when the box has it,
// falling back to IPv6. This keeps the public IP the control plane observes (and
// then advertises for the customer's DNS record) the box's IPv4 — the universal
// default for web hosting — on the common dual-stack box, rather than whichever
// family the OS happened to pick. An IPv6-only box still works via the fallback.
func ipv4PreferringTransport() *http.Transport {
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if conn, err := dialer.DialContext(ctx, "tcp4", addr); err == nil {
			return conn, nil
		}
		return dialer.DialContext(ctx, "tcp6", addr)
	}
	return t
}

// SetToken sets the Bearer token used for all authenticated calls.
func (c *Client) SetToken(t string) { c.token = t }

// Register exchanges a one-time pairing token for a permanent agent token.
// Unauthenticated. Maps non-201 responses to typed errors.
func (c *Client) Register(ctx context.Context, req RegisterRequest) (RegisterResponse, error) {
	resp, body, err := c.do(ctx, "register", "/agent/v1/register", req, false)
	if err != nil {
		return RegisterResponse{}, err
	}
	switch resp.StatusCode {
	case http.StatusCreated:
		var out RegisterResponse
		if err := json.Unmarshal(body, &out); err != nil {
			return RegisterResponse{}, fmt.Errorf("controlplane: decode register response: %w", err)
		}
		return out, nil
	case http.StatusUnprocessableEntity:
		return RegisterResponse{}, ErrUnsupportedOS
	case http.StatusUnauthorized:
		return RegisterResponse{}, ErrInvalidPairingToken
	case http.StatusTooManyRequests:
		return RegisterResponse{}, ErrRateLimited
	default:
		return RegisterResponse{}, &UnexpectedStatusError{Op: "register", Code: resp.StatusCode, Body: string(body)}
	}
}

// RenewCertificate exchanges a fresh CSR for a renewed client cert (security
// architecture §4). Authenticated over the existing mTLS channel.
func (c *Client) RenewCertificate(ctx context.Context, csr string) (RenewResponse, error) {
	resp, body, err := c.do(ctx, "certificate/renew", "/agent/v1/certificate/renew", RenewRequest{CSR: csr}, true)
	if err != nil {
		return RenewResponse{}, err
	}
	switch resp.StatusCode {
	case http.StatusOK:
		var out RenewResponse
		if err := json.Unmarshal(body, &out); err != nil {
			return RenewResponse{}, fmt.Errorf("controlplane: decode renew response: %w", err)
		}
		return out, nil
	case http.StatusUnauthorized:
		return RenewResponse{}, classifyUnauthorized(body)
	default:
		return RenewResponse{}, &UnexpectedStatusError{Op: "certificate/renew", Code: resp.StatusCode, Body: string(body)}
	}
}

// Poll asks for the next pending command. Returns the command (200),
// ErrNoWork (204), ErrRevoked / ErrUnauthorized (401), or an error.
func (c *Client) Poll(ctx context.Context) (*Command, error) {
	resp, body, err := c.do(ctx, "poll", "/agent/v1/commands/poll", struct{}{}, true)
	if err != nil {
		return nil, err
	}
	switch resp.StatusCode {
	case http.StatusOK:
		var cmd Command
		if err := json.Unmarshal(body, &cmd); err != nil {
			return nil, fmt.Errorf("controlplane: decode command: %w", err)
		}
		return &cmd, nil
	case http.StatusNoContent:
		return nil, ErrNoWork
	case http.StatusUnauthorized:
		return nil, classifyUnauthorized(body)
	default:
		return nil, &UnexpectedStatusError{Op: "poll", Code: resp.StatusCode, Body: string(body)}
	}
}

// Ack acknowledges receipt of a command. Sent immediately after a command is
// received, before execution.
func (c *Client) Ack(ctx context.Context, commandID int) error {
	return c.expectNoContent(ctx, "ack", fmt.Sprintf("/agent/v1/commands/%d/ack", commandID), struct{}{})
}

// PostEvents streams a batch of log/status events for a command.
func (c *Client) PostEvents(ctx context.Context, commandID int, events []Event) error {
	body := struct {
		Events []Event `json:"events"`
	}{Events: events}
	return c.expectNoContent(ctx, "events", fmt.Sprintf("/agent/v1/commands/%d/events", commandID), body)
}

// Heartbeat reports current health. Sent on every idle poll.
func (c *Client) Heartbeat(ctx context.Context, health HealthBlob) error {
	body := struct {
		Health HealthBlob `json:"health"`
	}{Health: health}
	return c.expectNoContent(ctx, "heartbeat", "/agent/v1/heartbeat", body)
}

// expectNoContent performs an authenticated POST that should return 204. A 401
// is surfaced as revoked/unauthorized so callers (e.g. the events poster during
// a long deploy) can react to mid-stream revocation.
func (c *Client) expectNoContent(ctx context.Context, op, path string, payload any) error {
	resp, body, err := c.do(ctx, op, path, payload, true)
	if err != nil {
		return err
	}
	switch resp.StatusCode {
	case http.StatusNoContent, http.StatusOK:
		return nil
	case http.StatusUnauthorized:
		return classifyUnauthorized(body)
	default:
		return &UnexpectedStatusError{Op: op, Code: resp.StatusCode, Body: string(body)}
	}
}

// do sends a JSON POST and returns the response plus its (fully read) body.
// Connection-level failures are wrapped so errors.Is(err, ErrUnreachable-ish)
// can be detected; we surface them as a plain error and let the loop treat any
// non-typed error from Poll as "retry".
func (c *Client) do(ctx context.Context, op, path string, payload any, auth bool) (*http.Response, []byte, error) {
	buf, err := json.Marshal(payload)
	if err != nil {
		return nil, nil, fmt.Errorf("controlplane: %s: marshal body: %w", op, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(buf))
	if err != nil {
		return nil, nil, fmt.Errorf("controlplane: %s: build request: %w", op, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if auth {
		if c.token == "" {
			return nil, nil, errors.New("controlplane: " + op + ": no agent token set")
		}
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.httpc.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("controlplane: %s: %w", op, err)
	}
	defer resp.Body.Close()

	// Bodies are tiny (commands, error envelopes); cap defensively anyway.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, nil, fmt.Errorf("controlplane: %s: read body: %w", op, err)
	}
	return resp, body, nil
}

// classifyUnauthorized distinguishes a revoked identity (delete credential and
// exit) from a plain unauthorized (re-pair needed).
func classifyUnauthorized(body []byte) error {
	var env struct {
		Revoked bool `json:"revoked"`
	}
	_ = json.Unmarshal(body, &env)
	if env.Revoked {
		return ErrRevoked
	}
	return ErrUnauthorized
}
