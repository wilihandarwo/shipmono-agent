package controlplane

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newTestClient(h http.HandlerFunc) (*Client, *httptest.Server) {
	srv := httptest.NewServer(h)
	c := New(srv.URL)
	c.SetToken("agt_test")
	return c, srv
}

func TestRegister(t *testing.T) {
	cases := []struct {
		name    string
		code    int
		body    string
		wantErr error
		wantTok string
	}{
		{"created", 201, `{"agent_token":"agt_abc","server_id":7,"poll_interval":2}`, nil, "agt_abc"},
		{"unsupported os", 422, `{"error":"unsupported_os"}`, ErrUnsupportedOS, ""},
		{"invalid token", 401, `{"error":"invalid_pairing_token"}`, ErrInvalidPairingToken, ""},
		{"rate limited", 429, `{"error":"rate_limited"}`, ErrRateLimited, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, srv := newTestClient(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/agent/v1/register" {
					t.Errorf("path = %s", r.URL.Path)
				}
				if got := r.Header.Get("Authorization"); got != "" {
					t.Errorf("register must be unauthenticated, got %q", got)
				}
				w.WriteHeader(tc.code)
				io.WriteString(w, tc.body)
			})
			defer srv.Close()

			resp, err := c.Register(context.Background(), RegisterRequest{PairingToken: "p", OSName: "ubuntu", OSVersion: "24.04"})
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if tc.wantTok != "" && resp.AgentToken != tc.wantTok {
				t.Errorf("agent_token = %q, want %q", resp.AgentToken, tc.wantTok)
			}
		})
	}
}

func TestPoll(t *testing.T) {
	t.Run("200 returns command and sends bearer", func(t *testing.T) {
		c, srv := newTestClient(func(w http.ResponseWriter, r *http.Request) {
			if got := r.Header.Get("Authorization"); got != "Bearer agt_test" {
				t.Errorf("auth header = %q", got)
			}
			w.WriteHeader(200)
			io.WriteString(w, `{"id":5,"verb":"reload","params":{}}`)
		})
		defer srv.Close()
		cmd, err := c.Poll(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if cmd.ID != 5 || cmd.Verb != "reload" {
			t.Errorf("got %+v", cmd)
		}
	})

	t.Run("204 is ErrNoWork", func(t *testing.T) {
		c, srv := newTestClient(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
		defer srv.Close()
		_, err := c.Poll(context.Background())
		if !errors.Is(err, ErrNoWork) {
			t.Fatalf("err = %v, want ErrNoWork", err)
		}
	})

	t.Run("401 revoked is ErrRevoked", func(t *testing.T) {
		c, srv := newTestClient(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(401)
			io.WriteString(w, `{"error":"unauthorized","revoked":true}`)
		})
		defer srv.Close()
		_, err := c.Poll(context.Background())
		if !errors.Is(err, ErrRevoked) {
			t.Fatalf("err = %v, want ErrRevoked", err)
		}
	})

	t.Run("401 plain is ErrUnauthorized", func(t *testing.T) {
		c, srv := newTestClient(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(401)
			io.WriteString(w, `{"error":"unauthorized"}`)
		})
		defer srv.Close()
		_, err := c.Poll(context.Background())
		if !errors.Is(err, ErrUnauthorized) {
			t.Fatalf("err = %v, want ErrUnauthorized", err)
		}
	})
}

func TestAckEventsHeartbeat(t *testing.T) {
	var gotPaths []string
	var lastBody string
	c, srv := newTestClient(func(w http.ResponseWriter, r *http.Request) {
		gotPaths = append(gotPaths, r.URL.Path)
		b, _ := io.ReadAll(r.Body)
		lastBody = string(b)
		w.WriteHeader(204)
	})
	defer srv.Close()

	ctx := context.Background()
	if err := c.Ack(ctx, 9); err != nil {
		t.Fatal(err)
	}
	if err := c.PostEvents(ctx, 9, []Event{{Type: EventLog, Line: "hello"}}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(lastBody, `"events"`) || !strings.Contains(lastBody, "hello") {
		t.Errorf("events body = %s", lastBody)
	}
	if err := c.Heartbeat(ctx, HealthBlob{CPUPercent: 33}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(lastBody, `"cpu_percent":33`) {
		t.Errorf("heartbeat body = %s", lastBody)
	}

	want := []string{"/agent/v1/commands/9/ack", "/agent/v1/commands/9/events", "/agent/v1/heartbeat"}
	if strings.Join(gotPaths, ",") != strings.Join(want, ",") {
		t.Errorf("paths = %v, want %v", gotPaths, want)
	}
}

func TestUnexpectedStatus(t *testing.T) {
	c, srv := newTestClient(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) })
	defer srv.Close()
	_, err := c.Poll(context.Background())
	var ue *UnexpectedStatusError
	if !errors.As(err, &ue) || ue.Code != 500 {
		t.Fatalf("err = %v, want UnexpectedStatusError 500", err)
	}
}

func TestRenewCertificate(t *testing.T) {
	t.Run("200 returns the new cert and sends the CSR + bearer", func(t *testing.T) {
		var gotBody, gotAuth, gotPath string
		c, srv := newTestClient(func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.Path
			gotAuth = r.Header.Get("Authorization")
			b, _ := io.ReadAll(r.Body)
			gotBody = string(b)
			w.WriteHeader(200)
			io.WriteString(w, `{"client_certificate":"LEAF","certificate_chain":"CHAIN","ca_bundle":"CA"}`)
		})
		defer srv.Close()

		resp, err := c.RenewCertificate(context.Background(), "CSRPEM")
		if err != nil {
			t.Fatal(err)
		}
		if resp.CertificateChain != "CHAIN" || resp.CABundle != "CA" {
			t.Errorf("resp = %+v", resp)
		}
		if gotPath != "/agent/v1/certificate/renew" {
			t.Errorf("path = %s", gotPath)
		}
		if gotAuth != "Bearer agt_test" {
			t.Errorf("auth = %q", gotAuth)
		}
		if !strings.Contains(gotBody, `"csr":"CSRPEM"`) {
			t.Errorf("body = %s", gotBody)
		}
	})

	t.Run("401 revoked is classified", func(t *testing.T) {
		c, srv := newTestClient(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(401)
			io.WriteString(w, `{"revoked":true}`)
		})
		defer srv.Close()
		_, err := c.RenewCertificate(context.Background(), "CSRPEM")
		if !errors.Is(err, ErrRevoked) {
			t.Fatalf("err = %v, want ErrRevoked", err)
		}
	})
}
