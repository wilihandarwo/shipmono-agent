package verbs

import (
	"context"
	"encoding/json"
	"regexp"
	"sync"
	"testing"

	"github.com/wilihandarwo/shipmono-agent/internal/config"
	"github.com/wilihandarwo/shipmono-agent/internal/controlplane"
	"github.com/wilihandarwo/shipmono-agent/internal/executor"
	"github.com/wilihandarwo/shipmono-agent/internal/health"
)

// recordingSink captures streamed logs and the single terminal status.
type recordingSink struct {
	mu     sync.Mutex
	logs   []string
	status *controlplane.Event
}

func (s *recordingSink) Log(line string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.logs = append(s.logs, line)
}

func (s *recordingSink) Status(ev controlplane.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := ev
	s.status = &e
}

func newSim() executor.Executor {
	cfg := config.Config{Simulate: true, AppRoot: "/srv/app"}
	return executor.New(cfg, health.Static(controlplane.HealthBlob{CPUPercent: 10}))
}

func params(t *testing.T, v any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestDispatchVerbs(t *testing.T) {
	releaseRE := regexp.MustCompile(`^\d{8}T\d{6}$`)

	cases := []struct {
		name       string
		cmd        controlplane.Command
		wantStatus string
		check      func(t *testing.T, ev controlplane.Event)
	}{
		{
			name: "deploy succeeds with release id",
			cmd: controlplane.Command{
				Verb:        "deploy",
				Params:      params(t, executor.DeployParams{RepoID: 42, RepoFullName: "demo/app", GitRef: "main", GitSha: "abcdef1234567890"}),
				Credentials: &controlplane.Credentials{GitToken: "ghs_x"},
			},
			wantStatus: controlplane.StatusSucceeded,
			check: func(t *testing.T, ev controlplane.Event) {
				if !releaseRE.MatchString(ev.ReleaseID) {
					t.Errorf("release_id %q does not match YYYYMMDDTHHMMSS", ev.ReleaseID)
				}
			},
		},
		{
			name:       "rollback echoes release id",
			cmd:        controlplane.Command{Verb: "rollback", Params: params(t, executor.RollbackParams{ReleaseID: "20260101T120000"})},
			wantStatus: controlplane.StatusSucceeded,
			check: func(t *testing.T, ev controlplane.Event) {
				if ev.ReleaseID != "20260101T120000" {
					t.Errorf("got release_id %q", ev.ReleaseID)
				}
			},
		},
		{name: "reload", cmd: controlplane.Command{Verb: "reload"}, wantStatus: controlplane.StatusSucceeded},
		{name: "restore", cmd: controlplane.Command{Verb: "restore", Params: params(t, executor.RestoreParams{PointInTime: "2026-01-01T00:00:00Z"})}, wantStatus: controlplane.StatusSucceeded},
		{name: "add_domain", cmd: controlplane.Command{Verb: "add_domain", Params: params(t, executor.DomainParams{Domain: "example.com"})}, wantStatus: controlplane.StatusSucceeded},
		{name: "remove_domain", cmd: controlplane.Command{Verb: "remove_domain", Params: params(t, executor.DomainParams{Domain: "example.com"})}, wantStatus: controlplane.StatusSucceeded},
		{
			name:       "status carries health",
			cmd:        controlplane.Command{Verb: "status"},
			wantStatus: controlplane.StatusSucceeded,
			check: func(t *testing.T, ev controlplane.Event) {
				if ev.Health == nil || ev.Health.CPUPercent != 10 {
					t.Errorf("expected health blob with cpu=10, got %+v", ev.Health)
				}
			},
		},
		{
			name:       "unknown verb is refused, never executed",
			cmd:        controlplane.Command{Verb: "exec"},
			wantStatus: controlplane.StatusFailed,
			check: func(t *testing.T, ev controlplane.Event) {
				if ev.Message != "unknown verb exec" {
					t.Errorf("got message %q", ev.Message)
				}
			},
		},
	}

	ex := newSim()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sink := &recordingSink{}
			Dispatch(context.Background(), ex, tc.cmd, sink)
			if sink.status == nil {
				t.Fatal("no terminal status event emitted")
			}
			if sink.status.Type != controlplane.EventStatus {
				t.Errorf("terminal event type = %q, want status", sink.status.Type)
			}
			if sink.status.Status != tc.wantStatus {
				t.Errorf("status = %q, want %q", sink.status.Status, tc.wantStatus)
			}
			if tc.check != nil {
				tc.check(t, *sink.status)
			}
		})
	}
}

func TestDispatchInvalidParamsFailsCleanly(t *testing.T) {
	ex := newSim()
	sink := &recordingSink{}
	// params is a JSON string where an object is expected.
	Dispatch(context.Background(), ex, controlplane.Command{
		Verb:   "rollback",
		Params: json.RawMessage(`"not-an-object"`),
	}, sink)
	if sink.status == nil || sink.status.Status != controlplane.StatusFailed {
		t.Fatalf("expected failed status for malformed params, got %+v", sink.status)
	}
}
