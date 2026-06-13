package daemon

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wilihandarwo/shipmono-agent/internal/config"
	"github.com/wilihandarwo/shipmono-agent/internal/controlplane"
	"github.com/wilihandarwo/shipmono-agent/internal/credstore"
	"github.com/wilihandarwo/shipmono-agent/internal/executor"
	"github.com/wilihandarwo/shipmono-agent/internal/health"
)

// mockControlPlane scripts the poll responses and records the order of every
// request path so the test can assert ack-before-events and the revoke exit.
type mockControlPlane struct {
	mu        sync.Mutex
	paths     []string
	pollCount int
}

func (m *mockControlPlane) record(p string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.paths = append(m.paths, p)
}

func (m *mockControlPlane) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	m.record(r.URL.Path)

	switch {
	case r.URL.Path == "/agent/v1/commands/poll":
		m.mu.Lock()
		m.pollCount++
		n := m.pollCount
		m.mu.Unlock()
		switch n {
		case 1:
			// First poll: hand out a reload command.
			w.WriteHeader(200)
			io.WriteString(w, `{"id":1,"verb":"reload","params":{}}`)
		case 2:
			// Second poll: no work (agent should heartbeat).
			w.WriteHeader(204)
		default:
			// Third poll onward: revoked.
			w.WriteHeader(401)
			io.WriteString(w, `{"error":"unauthorized","revoked":true}`)
		}
	default:
		// ack / events / heartbeat all return 204.
		w.WriteHeader(204)
	}
}

func (m *mockControlPlane) recorded() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.paths...)
}

func TestRunFullLifecycle(t *testing.T) {
	mock := &mockControlPlane{}
	srv := httptest.NewServer(mock)
	defer srv.Close()

	home := t.TempDir()
	if err := credstore.Save(home, credstore.Credential{Host: srv.URL, AgentToken: "agt", ServerID: 1}); err != nil {
		t.Fatal(err)
	}

	cfg := config.Config{Home: home, AppRoot: home, Host: srv.URL, Simulate: true, PollInterval: 5 * time.Millisecond}
	client := controlplane.New(srv.URL)
	client.SetToken("agt")
	ex := executor.New(cfg, health.Static(controlplane.HealthBlob{CPUPercent: 12}))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := Run(ctx, cfg, client, ex, health.Static(controlplane.HealthBlob{CPUPercent: 12}))
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if ctx.Err() != nil {
		t.Fatal("Run did not exit on revoke before the 5s safety timeout")
	}

	// Credential must be deleted on revoke.
	if _, err := credstore.Load(home); !errors.Is(err, credstore.ErrNotPaired) {
		t.Errorf("credential should be deleted after revoke, Load err = %v", err)
	}

	paths := mock.recorded()
	joined := strings.Join(paths, "\n")

	// Ordering: the command was acked before its events were posted.
	ackIdx := indexOf(paths, "/agent/v1/commands/1/ack")
	evIdx := indexOf(paths, "/agent/v1/commands/1/events")
	if ackIdx < 0 || evIdx < 0 || ackIdx > evIdx {
		t.Errorf("expected ack before events; ack=%d events=%d in:\n%s", ackIdx, evIdx, joined)
	}
	// A heartbeat was sent on the idle (204) poll.
	if indexOf(paths, "/agent/v1/heartbeat") < 0 {
		t.Errorf("expected a heartbeat after the 204 poll; got:\n%s", joined)
	}
}

func indexOf(s []string, want string) int {
	for i, v := range s {
		if v == want {
			return i
		}
	}
	return -1
}

func TestBackoffGrowsAndResets(t *testing.T) {
	bo := newBackoff(2*time.Second, 30*time.Second)
	// First few delays grow toward the cap (allowing ±20% jitter).
	d1 := bo.next()
	d2 := bo.next()
	if d1 <= 0 || d2 <= 0 {
		t.Fatalf("delays must be positive: %v %v", d1, d2)
	}
	// Drive to the cap and confirm it never exceeds max + jitter headroom.
	for i := 0; i < 10; i++ {
		d := bo.next()
		if d > 36*time.Second { // 30s cap + 20% jitter
			t.Fatalf("backoff exceeded cap: %v", d)
		}
	}
	bo.reset()
	if got := bo.cur; got != 2*time.Second {
		t.Errorf("reset cur = %v, want 2s", got)
	}
}
