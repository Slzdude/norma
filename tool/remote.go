package tool

import (
	"context"
	"time"
)

// RemoteRunner executes a shell command on a remote executor instead of the
// local host (分布式执行器架构). It is implemented by the host (the server
// package wraps the gRPC dispatch + per-session sticky executor claim) —
// norma/tool only depends on this narrow interface, so this package stays free
// of any gRPC/store dependency.
//
// A tool opts in by checking ToolContext.Remote itself (see Bash for the
// reference implementation) — most tools (business/store-backed queries) never
// look at it and always run locally, which is the correct default: only tools
// whose whole job is "run an external command/binary" make sense to run on a
// different host.
type RemoteRunner interface {
	// Run dispatches req to the executor claimed for this session and blocks
	// until the result arrives, ctx is cancelled, or req.Timeout elapses
	// (whichever comes first). It is the host's job to resolve "which
	// executor" from the session id embedded in the runner instance handed to
	// a given ToolContext — this interface itself is stateless per call.
	Run(ctx context.Context, req RemoteRunRequest) (RemoteRunResult, error)

	// EnsurePackage makes sure the named plugin package (name@version) is
	// present and verified on the executor claimed for this session —
	// triggering a distribution fetch if it is missing, and blocking until the
	// package is ready, the fetch fails, or ctx is done. Call it before any
	// Run whose Command references "{pkg_dir}" (see RemoteRunRequest); the
	// host resolves that placeholder on the executor side once this returns
	// successfully — norma/tool never needs to know the executor's local
	// filesystem layout.
	EnsurePackage(ctx context.Context, name, version string) error
}

// RemoteRunRequest mirrors exec.Command's shape closely enough that Bash (and
// later script-kind custom tools) can delegate to it with minimal translation.
type RemoteRunRequest struct {
	Command string
	// Env holds extra "KEY=VALUE" entries, same semantics as ToolContext.Env.
	Env []string
	// Files are relative-path → content pairs materialized in the session's
	// sandbox directory on the executor BEFORE Command runs (used by
	// script-kind tools that write a temp script file first).
	Files   map[string][]byte
	Stdin   string
	Timeout time.Duration
	// Background mirrors Bash's run_in_background. RemoteRunner implementations
	// are not required to support it — the v1 host implementation returns a
	// clear error instead of silently running it locally or losing the sticky
	// invariant (see runBashRemote).
	Background bool
	// PackageName/PackageVersion, when both non-empty, tell the host that
	// Command references the literal placeholder "{pkg_dir}" and depends on
	// that plugin package being present on the claimed executor. Call
	// RemoteRunner.EnsurePackage with the same name/version before Run — Run
	// itself does not trigger provisioning, it assumes EnsurePackage already
	// succeeded.
	PackageName    string
	PackageVersion string
}

// RemoteRunResult is the outcome of one RemoteRunner.Run call.
type RemoteRunResult struct {
	ExitCode int
	Stdout   string
	Stderr   string
	// Error is a transport/exec-level failure (couldn't start the process,
	// executor disconnected mid-call, etc.) — distinct from a non-zero ExitCode,
	// which is the command's own outcome.
	Error    string
	TimedOut bool
}
