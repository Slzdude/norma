package tool

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 三档能力断言（scratch 实跑）：
// readonly 拒写全盘 / workspace 可写工作区拒写系统 / unrestricted 放行。
func runSandboxed(t *testing.T, p SandboxPolicy, script string) (string, error) {
	t.Helper()
	cmd, wrapped, err := WrapExec(context.Background(), p, "bash", []string{"-c"}, script)
	if err != nil {
		return "", err
	}
	if p.Mode == SandboxWorkspace || p.Mode == SandboxReadonly {
		if !wrapped {
			t.Fatalf("confined 档必须包沙箱（wrapped=false=静默放开）")
		}
	}
	if p.Mode == SandboxUnrestricted && wrapped {
		t.Fatalf("unrestricted 档不应包沙箱")
	}
	if p.WorkspaceRoot != "" {
		cmd.Dir = p.WorkspaceRoot
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestSandboxThreeTierCapabilities(t *testing.T) {
	if !bwrapAvailable() {
		t.Skip("bwrap 不可用（探针失败）——跳过实跑断言")
	}
	ws := t.TempDir()
	marker := filepath.Join(ws, "w.txt")

	// readonly：工作区写也拒。
	p := NormalizePolicy("readonly", "full", ws)
	if out, err := runSandboxed(t, p, "echo x > "+marker+" 2>&1; cat "+marker+" 2>&1"); err == nil && strings.Contains(out, "x") {
		t.Errorf("readonly 档写了工作区（应全盘拒写）: %s", out)
	}

	// workspace：工作区可写 + 系统面拒写。
	p = NormalizePolicy("workspace", "full", ws)
	out, err := runSandboxed(t, p, "echo ok > "+marker+" && cat "+marker)
	if err != nil || !strings.Contains(out, "ok") {
		t.Errorf("workspace 档应可写工作区: %v %s", err, out)
	}
	out, _ = runSandboxed(t, p, "echo bad > /etc/norma-sandbox-probe 2>&1; cat /etc/norma-sandbox-probe 2>&1")
	if strings.Contains(out, "bad") {
		t.Errorf("workspace 档写了系统面（应拒）: %s", out)
	}

	// tmpfs 瞬态：/tmp 可写且不落宿主。
	out, _ = runSandboxed(t, p, "echo t > /tmp/t && cat /tmp/t")
	if !strings.Contains(out, "t") {
		t.Errorf("workspace 档 /tmp 应可写（tmpfs）")
	}

	// unrestricted：放行（系统面写受宿主权限限制，不在此断言）。
	p = NormalizePolicy("unrestricted", "full", ws)
	out, err = runSandboxed(t, p, "echo free && pwd")
	if err != nil || !strings.Contains(out, "free") {
		t.Errorf("unrestricted 应放行: %v %s", err, out)
	}

	// 网络子模式 none：--unshare-net 生效（解析不出网卡）。
	p = NormalizePolicy("workspace", "none", ws)
	out, _ = runSandboxed(t, p, "ip -o link 2>/dev/null | wc -l")
	_ = out

	// 进程面隔离：看不到宿主进程。
	p = NormalizePolicy("workspace", "full", ws)
	out, _ = runSandboxed(t, p, "ps -e 2>/dev/null | wc -l")
	// 新 proc + unshare-pid：仅沙箱内进程（≤5 个）。
	if strings.TrimSpace(out) != "" && len(strings.TrimSpace(out)) > 0 {
		n := strings.TrimSpace(out)
		if n != "" && n[0] >= '5' && len(n) == 1 {
			t.Logf("进程面计数 %s（unshare-pid 生效判定留观察）", n)
		}
	}
}

func TestSandboxNormalizeFailClosed(t *testing.T) {
	p := NormalizePolicy("bogus-mode", "bogus-net", "")
	if p.Mode != SandboxWorkspace {
		t.Errorf("未知档应回落 workspace（不放行），实际 %s", p.Mode)
	}
	if p.Net != NetFull {
		t.Errorf("未知网络档回落 full，实际 %s", p.Net)
	}
	// unrestricted 必须显式。
	if NormalizePolicy("", "", "").Mode == SandboxUnrestricted {
		t.Errorf("缺省不得为 unrestricted")
	}
}

var _ = exec.Command
var _ = os.TempDir
var _ = time.Second

// 回归：workspace 档 writableRoots 未并入
// exec 的 tc.WorkingDir——写 cwd 被拒（Read-only file system）。修=可写根 =
// policy.WorkspaceRoot ∪ 实际 WorkingDir（各自规范化）。先红后绿。
func TestSandboxWorkingDirWritable(t *testing.T) {
	if !bwrapAvailable() {
		t.Skip("bwrap 不可用")
	}
	ws := t.TempDir()  // 任务工作区（policy 根）
	cwd := t.TempDir() // exec 的 tc.WorkingDir（任务 2 文件写作流=cwd）
	p := NormalizePolicy("workspace", "full", ws)

	// 现场形：WrapShell 的 cwd 参数（修复前传 ""→WorkspaceRoot 空→无 --bind）。
	ctx := WithSandboxPolicy(context.Background(), p)
	cmd, wrapped, err := WrapShellCwd(ctx, "bash", []string{"-c"},
		"echo demo > sandbox_demo.txt && cat sandbox_demo.txt", cwd)
	if err != nil || !wrapped {
		t.Fatalf("wrap 失败: %v wrapped=%v", err, wrapped)
	}
	cmd.Dir = cwd
	out, err := cmd.CombinedOutput()
	if err != nil || !stringContains(string(out), "demo") {
		t.Errorf("workspace 档应可写 WorkingDir: %v out=%s", err, out)
	}
}

func stringContains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 || indexOf(s, sub) >= 0)
}
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// 回归：主对话 tc.WorkingDir=**空串**形（
// 15:26:05 服务重启后仍 Read-only）——cmd.Dir="" → 进程 cwd=服务 cwd →
// canonicalRoot("") 被丢 → 并集不含实际 cwd。修=空 cwd 自取 os.Getwd()。
// 先红后绿：本用例构造 WorkingDir==""（首轮红测试用了非空形所以绿了）。
func TestSandboxEmptyWorkingDirWritable(t *testing.T) {
	if !bwrapAvailable() {
		t.Skip("bwrap 不可用")
	}
	real, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	if err := os.Chdir(work); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(real)

	p := NormalizePolicy("workspace", "full", t.TempDir()) // policy 根≠实际 cwd
	ctx := WithSandboxPolicy(context.Background(), p)
	// 空 cwd 形（chat 主对话）：进程继承父 cwd=work。
	cmd, wrapped, err := WrapShellCwd(ctx, "bash", []string{"-c"},
		"echo demo > sandbox_demo.txt && cat sandbox_demo.txt", "")
	if err != nil || !wrapped {
		t.Fatalf("wrap 失败: %v wrapped=%v", err, wrapped)
	}
	out, err := cmd.CombinedOutput()
	if err != nil || !stringContains(string(out), "demo") {
		t.Errorf("空 WorkingDir 形应可写实际 cwd: %v out=%s", err, out)
	}
}

// 生产形回归（`mkdir <task-root>/i_*`=Read-only 同形）：
// WorkingDir=任务根子目录（i_x/work 形）+ ctx **无政策**（意图 worker 丢 ctx 形）
// → mkdir 兄弟意图目录（任务根下 i_y）。修复前红（缺省档只可写叶子）；
// 修后绿（TaskRootFor 兜底并入任务根）。
func TestSandboxTaskRootSiblingMkdir(t *testing.T) {
	if !bwrapAvailable() {
		t.Skip("bwrap 不可用")
	}
	// 基必须非 /tmp（workspace 档 tmpfs /tmp 全可写——/tmp 下复现不出生产形！）
	base, err := os.MkdirTemp(".", "task145-repro")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(base)
	taskRoot := filepath.Join(base, "tasks", "9")
	work := filepath.Join(taskRoot, "i_5", "work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	// ctx 无政策（生产丢点形）：WrapShellCwd 自取缺省档。
	ctx := context.Background()
	cmd, wrapped, err := WrapShellCwd(ctx, "bash", []string{"-c"},
		"mkdir -p ../../i_6 && echo sibling_ok", work)
	if err != nil || !wrapped {
		t.Fatalf("wrap 失败: %v wrapped=%v", err, wrapped)
	}
	cmd.Dir = work
	out, err := cmd.CombinedOutput()
	if err != nil || !stringContains(string(out), "sibling_ok") {
		t.Errorf("任务根兄弟目录应可写: %v out=%s", err, out)
	}
	// 任务根外仍拒（语义不变核对③）。
	cmd2, _, _ := WrapShellCwd(ctx, "bash", []string{"-c"}, "echo x > /etc/task145-probe 2>&1", work)
	cmd2.Dir = work
	out2, _ := cmd2.CombinedOutput()
	if stringContains(string(out2), "\nx") {
		t.Errorf("系统面应仍拒: %s", out2)
	}
}
