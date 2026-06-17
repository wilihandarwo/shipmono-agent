package controlplane

import "encoding/json"

// RegisterRequest is the body of POST /agent/v1/register. It is the only
// unauthenticated call: a one-time pairing token is exchanged for a permanent
// agent token. CSR carries the agent-generated certificate request for the mTLS
// identity (security architecture §4); omitted when the agent runs bearer-only.
type RegisterRequest struct {
	PairingToken string `json:"pairing_token"`
	OSName       string `json:"os_name"`
	OSVersion    string `json:"os_version"`
	IPAddress    string `json:"ip_address,omitempty"`
	AgentVersion string `json:"agent_version,omitempty"`
	CSR          string `json:"csr,omitempty"`
}

// RegisterResponse is the 201 body from register. The certificate fields are
// present only when the control plane is running mTLS (it signed the CSR).
type RegisterResponse struct {
	AgentToken   string `json:"agent_token"`
	ServerID     int    `json:"server_id"`
	PollInterval int    `json:"poll_interval"`

	ClientCertificate string `json:"client_certificate,omitempty"`
	CertificateChain  string `json:"certificate_chain,omitempty"`
	CABundle          string `json:"ca_bundle,omitempty"`
	AgentEndpoint     string `json:"agent_endpoint,omitempty"`
}

// RenewRequest is the body of POST /agent/v1/certificate/renew.
type RenewRequest struct {
	CSR string `json:"csr"`
}

// RenewResponse is the 200 body from certificate renewal.
type RenewResponse struct {
	ClientCertificate string `json:"client_certificate"`
	CertificateChain  string `json:"certificate_chain"`
	CABundle          string `json:"ca_bundle"`
}

// Command is the 200 body from POST /agent/v1/commands/poll. Params and
// credentials are kept raw and unmarshalled per-verb so the transport layer
// never needs to know verb-specific shapes.
type Command struct {
	ID             int             `json:"id"`
	IdempotencyKey string          `json:"idempotency_key"`
	Verb           string          `json:"verb"`
	Params         json.RawMessage `json:"params"`
	Credentials    *Credentials    `json:"credentials,omitempty"`
}

// Credentials carries the short-lived GitHub installation token, present only
// for deploy commands and merged in by the control plane at poll time. It is
// never persisted by the control plane and must never be persisted by the agent.
type Credentials struct {
	GitToken string `json:"git_token"`
}

// Event is one entry in the POST /agent/v1/commands/:id/events array. A "log"
// event carries a single Line; a terminal "status" event carries Status and,
// depending on the verb, ReleaseID / Message / Health.
type Event struct {
	Type      string      `json:"type"`
	Line      string      `json:"line,omitempty"`
	Status    string      `json:"status,omitempty"`
	ReleaseID string      `json:"release_id,omitempty"`
	Message   string      `json:"message,omitempty"`
	Health    *HealthBlob `json:"health,omitempty"`
}

// Event type and status constants — the closed vocabulary the control plane
// understands.
const (
	EventLog    = "log"
	EventStatus = "status"

	StatusSucceeded = "succeeded"
	StatusFailed    = "failed"
)

// HealthBlob is the health payload sent on heartbeat and as the status event of
// the `status` verb. The control plane draws usage bars and sparklines from
// cpu_percent / ram_percent / disk_percent.
type HealthBlob struct {
	AgentVersion      string `json:"agent_version"`
	FrankenPHPVersion string `json:"frankenphp_version"`
	FrankenPHPHealthy bool   `json:"frankenphp_healthy"`
	DiskFreeBytes     int64  `json:"disk_free_bytes"`
	LoadAvg           string `json:"load_avg"`
	CPUPercent        int    `json:"cpu_percent"`
	RAMPercent        int    `json:"ram_percent"`
	DiskPercent       int    `json:"disk_percent"`
}
