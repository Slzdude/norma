package tool

// 执行沙箱能力层。
// **与 intercept 双层不互替**：本层=能力面（能不能写/联网），intercept=行为面
// （该不该执行）——各自 fail-closed。
//
// 基形：
//
//	--ro-bind / /      全盘只读基底（写=显式授予）
//	--bind <ws> <ws>   工作区可写面（单源规范化：data/tasks/<id>）
//	--tmpfs /tmp       瞬态临时区
//	--unshare-pid --proc /proc --die-with-parent --new-session
//
// 模式梯三档：readonly / workspace（默认）/ unrestricted（显式+审批才开）。

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

// SandboxMode 能力档（三档梯）。
type SandboxMode string

const (
	SandboxReadonly     SandboxMode = "readonly"
	SandboxWorkspace    SandboxMode = "workspace"
	SandboxUnrestricted SandboxMode = "unrestricted"
)

// NetMode 网络子模式（none=隔离；allowlist=按名单（bwrap 单体面不足，显式回落
// none+提示，非静默）；full=宿主网络）。
type NetMode string

const (
	NetNone      NetMode = "none"
	NetAllowlist NetMode = "allowlist"
	NetFull      NetMode = "full"
)

// SandboxPolicy 是一次执行的能力面解析结果（三层配置面合成产物：
// 调用级 > 任务级 > 全局默认——合成在 server/guard 侧完成，本层只消费）。
// canonicalRoot 规范化单根（Abs+EvalSymlinks；失败=原样，缺根保守处理）。
func canonicalRoot(path string) string {
	if path == "" {
		return ""
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	// EvalSymlinks 对不存在路径返回错——**不丢根**（回退 abs=仍授予，
	// 缺根时的取舍：执行目录必须可写是基本预期，宁授勿拒
	// 仅限显式命名的根；空串才丢）。
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		return real
	}
	return abs
}

type SandboxPolicy struct {
	Mode          SandboxMode
	Net           NetMode
	WorkspaceRoot string
	ExtraWritable []string // exec 实际 WorkingDir 等并入面（∪ 可写根）
	Escalated     bool     // 调用级扩权标记（审批链留痕用）
	Justification string   // 扩权理由（必填，activity 留痕）
}

// WritableRoots 返回规范化后的可写根并集（workspaceRoot ∪ ExtraWritable，去重）。
// 「执行目录必须可写」是基本预期。
func (p SandboxPolicy) WritableRoots() []string {
	seen := map[string]bool{}
	out := []string{}
	for _, r := range append([]string{p.WorkspaceRoot}, p.ExtraWritable...) {
		c := canonicalRoot(r)
		if c == "" || seen[c] {
			continue
		}
		seen[c] = true
		out = append(out, c)
	}
	return out
}

// NormalizePolicy 归一（非法值回落安全档=fail-closed；unrestricted 保留须显式）。
func NormalizePolicy(mode, net, workspaceRoot string) SandboxPolicy {
	p := SandboxPolicy{Mode: SandboxWorkspace, Net: NetFull}
	switch SandboxMode(mode) {
	case SandboxReadonly, SandboxUnrestricted:
		p.Mode = SandboxMode(mode)
	case "":
		// 缺省=workspace（默认档）
	default:
		p.Mode = SandboxWorkspace // 未知值回落默认档（不放行）
	}
	switch NetMode(net) {
	case NetNone, NetAllowlist:
		p.Net = NetMode(net)
	case NetFull, "":
		p.Net = NetFull
	default:
		p.Net = NetFull
	}
	p.WorkspaceRoot = canonicalRoot(workspaceRoot) // 规范化（realpath）
	return p
}

// bwrapAvailable 功能探针（有界超时+缓存）：bwrap 可用性实跑验证。
var (
	bwrapProbeOnce sync.Once
	bwrapProbeOK   bool
)

// probeTimeout 有界探针超时（zero would mean unbounded——禁止 0）。
const probeTimeout = 3 * time.Second

func bwrapAvailable() bool {
	bwrapProbeOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
		defer cancel()
		// 基形探针：只读 profile 下跑 true。
		cmd := exec.CommandContext(ctx, "bwrap",
			"--ro-bind", "/", "/", "--dev", "/dev", "--unshare-pid", "--proc", "/proc",
			"--die-with-parent", "--", "true")
		bwrapProbeOK = cmd.Run() == nil
	})
	return bwrapProbeOK
}

// BwrapProbeForced 清缓存（测试用）。
func BwrapProbeForced(ok bool) {
	bwrapProbeOnce = sync.Once{}
	bwrapProbeOnce.Do(func() {})
	bwrapProbeOK = ok
}

// WrapExec 把 shell 命令包进沙箱（三 exec 点共用入口）。
// 返回 (cmd, wrapped)：wrapped=false 表示未包（unrestricted 或探针失败回落）——
// 调用方须据 Escalated/回落标志显式提示，**禁止静默放开**。
func WrapExec(ctx context.Context, p SandboxPolicy, shell string, flags []string, command string) (*exec.Cmd, bool, error) {
	switch p.Mode {
	case SandboxUnrestricted:
		// 显式档：直跑（调用方须已走审批链+留痕）。
		args := append(append([]string{}, flags...), command)
		return exec.CommandContext(ctx, shell, args...), false, nil
	case SandboxReadonly, SandboxWorkspace:
		if !bwrapAvailable() {
			return nil, false, fmt.Errorf("bwrap 不可用：沙箱无法建立（拒绝静默放开——请显式走 unrestricted+审批）")
		}
		args := []string{
			"--ro-bind", "/", "/",
			"--dev", "/dev",
			"--unshare-pid", "--proc", "/proc",
			"--die-with-parent", "--new-session",
		}
		if p.Mode == SandboxWorkspace {
			args = append(args, "--tmpfs", "/tmp")
			for _, root := range p.WritableRoots() { // ws ∪ WorkingDir 并集
				args = append(args, "--bind", root, root)
			}
		}
		if p.Net == NetNone {
			args = append(args, "--unshare-net") // 网络隔离子模式
		}
		// NetAllowlist：bwrap 单体无名单能力——按 none 处理并由调用方提示（非静默）。
		if p.Net == NetAllowlist {
			args = append(args, "--unshare-net")
		}
		args = append(args, "--", shell)
		args = append(args, flags...)
		args = append(args, command)
		return exec.CommandContext(ctx, "bwrap", args...), true, nil
	}
	return nil, false, fmt.Errorf("未知沙箱档 %q", p.Mode)
}

var _ = os.Getenv

// ==== 调用面：策略经 context 传递（server/guard 侧合成三层配置后注入）====

type ctxKeySandbox struct{}

// WithSandboxPolicy 注入策略（调用点=server 任务执行面/调用级扩权面）。
func WithSandboxPolicy(ctx context.Context, p SandboxPolicy) context.Context {
	return context.WithValue(ctx, ctxKeySandbox{}, p)
}

// PolicyFromContext 取策略（缺省=workspace 档 + cwd 可写面——fail-closed 安全缺省）。
func PolicyFromContext(ctx context.Context, cwd string) SandboxPolicy {
	if p, ok := ctx.Value(ctxKeySandbox{}).(SandboxPolicy); ok {
		// exec 实际 WorkingDir 并入可写面（∪ 并集，各自规范化）——
		// 「执行目录必须可写」是基本预期（写 cwd 不应被 Read-only 拒）。
		if cwd != "" {
			p.ExtraWritable = append(append([]string{}, p.ExtraWritable...), cwd)
		}
		if p.WorkspaceRoot == "" {
			p.WorkspaceRoot = cwd
		}
		return p
	}
	return NormalizePolicy(string(SandboxWorkspace), string(NetFull), cwd)
}

// WrapShell 是三 exec 点共用便捷入口（policy 从 ctx 取；cwd=实际 WorkingDir
// 并入可写根）。
func WrapShell(ctx context.Context, shell string, flags []string, command string) (*exec.Cmd, bool, error) {
	return WrapShellCwd(ctx, shell, flags, command, "")
}

// WrapShellCwd 同 WrapShell，cwd=exec 的实际 WorkingDir（可写根并集成员）。
// 空 cwd（chat 主对话形）自取 os.Getwd()——cmd.Dir="" 进程继承父 cwd，
// 并集必须含之。
func WrapShellCwd(ctx context.Context, shell string, flags []string, command string, cwd string) (*exec.Cmd, bool, error) {
	if cwd == "" {
		if wd, err := os.Getwd(); err == nil {
			cwd = wd
		}
	}
	p := PolicyFromContext(ctx, cwd)
	// 兜底：任务根并入可写面（ctx 丢政策也不塌——全链覆盖）。
	if root := TaskRootFor(cwd); root != "" {
		p.ExtraWritable = append(append([]string{}, p.ExtraWritable...), root)
	}
	return WrapExec(ctx, p, shell, flags, command)
}

// ==== 任务根推导兜底（ctx 丢失也不塌）====

// TaskRootFor 从执行目录推导任务根（data/tasks/<id> 形）：向上找 tasks/<数字> 祖先。
// 生产实证（3 起 `mkdir data/tasks/9/i_*`=Read-only）：意图 worker 链丢政策 ctx
// →缺省档只可写 WorkingDir 叶子→在任务根下建兄弟意图目录被拒。兜底=可写面
// 恒含任务根（执行目录必须可写的基本预期扩展到任务工作区整面）。
func TaskRootFor(path string) string {
	cur := canonicalRoot(path)
	for i := 0; i < 8 && cur != "" && cur != "/" && cur != "."; i++ {
		base := filepath.Base(cur)
		parent := filepath.Dir(cur)
		if base == "tasks" {
			return "" // tasks 本身不是任务根
		}
		if filepath.Base(parent) == "tasks" {
			// tasks/<id> 形（id=任意段——数字/字符串任务号皆可）
			return cur
		}
		cur = parent
	}
	return ""
}
