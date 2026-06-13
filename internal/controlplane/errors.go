package controlplane

import (
	"errors"
	"fmt"
)

// Sentinel errors mapping the control plane's status codes to conditions the
// poll loop branches on. Callers should use errors.Is against these.
var (
	// ErrNoWork is returned by Poll on a 204 (no pending command).
	ErrNoWork = errors.New("controlplane: no pending command")

	// ErrRevoked is returned on 401 {revoked:true}: the agent's identity has
	// been revoked. The loop must delete its credential and exit.
	ErrRevoked = errors.New("controlplane: agent identity revoked")

	// ErrUnauthorized is returned on a 401 without revoked:true: the token is
	// not accepted but the server was not explicitly revoked (re-pair needed).
	ErrUnauthorized = errors.New("controlplane: unauthorized")

	// ErrRateLimited is returned on 429 (register throttling).
	ErrRateLimited = errors.New("controlplane: rate limited")

	// ErrUnsupportedOS is returned on 422 from register (non-Ubuntu-LTS).
	ErrUnsupportedOS = errors.New("controlplane: unsupported OS")

	// ErrInvalidPairingToken is returned on 401 from register.
	ErrInvalidPairingToken = errors.New("controlplane: invalid or expired pairing token")
)

// UnexpectedStatusError is returned when the control plane responds with a
// status code the agent does not specifically handle.
type UnexpectedStatusError struct {
	Op   string // e.g. "poll", "heartbeat"
	Code int
	Body string
}

func (e *UnexpectedStatusError) Error() string {
	body := e.Body
	if len(body) > 256 {
		body = body[:256] + "…"
	}
	return fmt.Sprintf("controlplane: %s: unexpected status %d: %s", e.Op, e.Code, body)
}
