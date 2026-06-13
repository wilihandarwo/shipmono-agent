//go:build !linux

package executor

// newLinuxExecutor is unreachable off Linux: config.resolveSimulate forces
// simulation on any non-Linux host, so New never takes the real-executor
// branch. The stub exists only so factory.go compiles everywhere.
func newLinuxExecutor(Deps) Executor {
	panic("shipmono-agent: the real Linux executor is only available on linux/amd64 and linux/arm64")
}
