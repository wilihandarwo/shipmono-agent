//go:build linux

package health

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/wilihandarwo/shipmono-agent/internal/config"
	"github.com/wilihandarwo/shipmono-agent/internal/controlplane"
	"github.com/wilihandarwo/shipmono-agent/internal/version"
)

func newSampler(appRoot string) Sampler { return &linuxSampler{appRoot: appRoot} }

type linuxSampler struct{ appRoot string }

func (s *linuxSampler) Sample(ctx context.Context) controlplane.HealthBlob {
	blob := controlplane.HealthBlob{
		AgentVersion:      version.Version,
		LoadAvg:           readLoadAvg(),
		CPUPercent:        sampleCPUPercent(ctx),
		RAMPercent:        readRAMPercent(),
		FrankenPHPVersion: frankenphpVersion(ctx),
		FrankenPHPHealthy: frankenphpHealthy(ctx),
	}
	free, pct := diskStats(s.appRoot)
	blob.DiskFreeBytes = free
	blob.DiskPercent = pct
	return blob
}

// readLoadAvg returns the 1-minute load average formatted to two decimals,
// matching the contract's "0.45"-style string.
func readLoadAvg() string {
	raw, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return "0.00"
	}
	fields := strings.Fields(string(raw))
	if len(fields) == 0 {
		return "0.00"
	}
	v, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return "0.00"
	}
	return strconv.FormatFloat(v, 'f', 2, 64)
}

// readRAMPercent computes used memory percent from MemTotal/MemAvailable.
func readRAMPercent() int {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0
	}
	defer f.Close()

	var total, available float64
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "MemTotal:"):
			total = meminfoValue(line)
		case strings.HasPrefix(line, "MemAvailable:"):
			available = meminfoValue(line)
		}
	}
	if total <= 0 {
		return 0
	}
	return clampPercent((1 - available/total) * 100)
}

func meminfoValue(line string) float64 {
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return 0
	}
	v, _ := strconv.ParseFloat(fields[1], 64) // kB; ratio cancels the unit
	return v
}

// sampleCPUPercent reads /proc/stat twice over a short window and returns the
// busy fraction of jiffies as a percent.
func sampleCPUPercent(ctx context.Context) int {
	idle1, total1, ok1 := readCPUTimes()
	if !ok1 {
		return 0
	}
	select {
	case <-time.After(200 * time.Millisecond):
	case <-ctx.Done():
		return 0
	}
	idle2, total2, ok2 := readCPUTimes()
	if !ok2 {
		return 0
	}
	dTotal := total2 - total1
	dIdle := idle2 - idle1
	if dTotal <= 0 {
		return 0
	}
	return clampPercent((1 - float64(dIdle)/float64(dTotal)) * 100)
}

// readCPUTimes parses the aggregate "cpu" line of /proc/stat and returns the
// idle jiffies (idle+iowait) and the total across all fields.
func readCPUTimes() (idle, total uint64, ok bool) {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return 0, 0, false
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 5 || fields[0] != "cpu" {
			continue
		}
		var sum uint64
		for i, col := range fields[1:] {
			n, err := strconv.ParseUint(col, 10, 64)
			if err != nil {
				continue
			}
			sum += n
			// idle (index 3) + iowait (index 4) count as idle time.
			if i == 3 || i == 4 {
				idle += n
			}
		}
		return idle, sum, true
	}
	return 0, 0, false
}

// diskStats returns free bytes and used percent for the filesystem holding
// the app root.
func diskStats(appRoot string) (freeBytes int64, usedPercent int) {
	path := appRoot
	if path == "" {
		path = config.DefaultAppRoot
	}
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		// App root may not exist on a freshly-provisioned box; fall back to /.
		if err := syscall.Statfs("/", &st); err != nil {
			return 0, 0
		}
	}
	bsize := uint64(st.Bsize)
	freeBytes = int64(st.Bavail * bsize)
	if st.Blocks == 0 {
		return freeBytes, 0
	}
	used := st.Blocks - st.Bavail
	usedPercent = clampPercent(float64(used) / float64(st.Blocks) * 100)
	return freeBytes, usedPercent
}

var frankenphpVersionRE = regexp.MustCompile(`v?(\d+\.\d+\.\d+)`)

func frankenphpVersion(ctx context.Context) string {
	out, err := exec.CommandContext(ctx, "frankenphp", "version").Output()
	if err != nil {
		return ""
	}
	if m := frankenphpVersionRE.FindStringSubmatch(string(out)); m != nil {
		return m[1]
	}
	return strings.TrimSpace(string(out))
}

func frankenphpHealthy(ctx context.Context) bool {
	out, err := exec.CommandContext(ctx, "systemctl", "is-active", config.FrankenPHPUnit).Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) == "active"
}

func clampPercent(v float64) int {
	switch {
	case v < 0:
		return 0
	case v > 100:
		return 100
	default:
		return int(v + 0.5)
	}
}
