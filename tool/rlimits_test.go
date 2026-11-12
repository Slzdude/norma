//go:build linux

package tool

import (
	"os/exec"
	"testing"

	"golang.org/x/sys/unix"
)

// TestRlimitsApplyToChild 契约：ApplyTo 落的是**子进程**的硬限（prlimit 语义），
// 不是宿主进程——这是 rlimits.go 实现的关键区分（setrlimit 会误伤宿主）。
func TestRlimitsApplyToChild(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Skipf("无法启动 sleep 子进程: %v", err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()

	// 先记录宿主进程自身的 NOFILE，验证 ApplyTo 不动宿主。
	var hostBefore unix.Rlimit
	if err := unix.Prlimit(0, unix.RLIMIT_NOFILE, nil, &hostBefore); err != nil {
		t.Fatalf("读宿主 RLIMIT_NOFILE: %v", err)
	}

	r := Rlimits{CPUSeconds: 42, AddressSpace: 1 << 30, OpenFiles: 77, Processes: 9}
	if err := r.ApplyTo(cmd.Process.Pid); err != nil {
		t.Fatalf("ApplyTo: %v", err)
	}

	// 子进程侧读回断言。
	for _, tc := range []struct {
		name string
		res  int
		want uint64
	}{
		{"CPU", unix.RLIMIT_CPU, 42},
		{"AS", unix.RLIMIT_AS, 1 << 30},
		{"NOFILE", unix.RLIMIT_NOFILE, 77},
		{"NPROC", unix.RLIMIT_NPROC, 9},
	} {
		var got unix.Rlimit
		if err := unix.Prlimit(cmd.Process.Pid, tc.res, nil, &got); err != nil {
			t.Fatalf("读子进程 %s: %v", tc.name, err)
		}
		if got.Cur != tc.want {
			t.Errorf("子进程 %s.Cur = %d, want %d", tc.name, got.Cur, tc.want)
		}
	}

	// 宿主进程不得被改动。
	var hostAfter unix.Rlimit
	if err := unix.Prlimit(0, unix.RLIMIT_NOFILE, nil, &hostAfter); err != nil {
		t.Fatalf("读宿主 RLIMIT_NOFILE: %v", err)
	}
	if hostAfter.Cur != hostBefore.Cur || hostAfter.Max != hostBefore.Max {
		t.Errorf("宿主 RLIMIT_NOFILE 被误改: %v → %v", hostBefore, hostAfter)
	}
}

// TestRlimitsZeroValueKeepsInherited 契约：零值项不设（沿用继承值）。
func TestRlimitsZeroValueKeepsInherited(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Skipf("无法启动 sleep 子进程: %v", err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()
	var before unix.Rlimit
	if err := unix.Prlimit(cmd.Process.Pid, unix.RLIMIT_NOFILE, nil, &before); err != nil {
		t.Fatal(err)
	}
	if err := (Rlimits{}).ApplyTo(cmd.Process.Pid); err != nil {
		t.Fatalf("空限额应 no-op: %v", err)
	}
	var after unix.Rlimit
	if err := unix.Prlimit(cmd.Process.Pid, unix.RLIMIT_NOFILE, nil, &after); err != nil {
		t.Fatal(err)
	}
	if after.Cur != before.Cur {
		t.Errorf("零值项不应改动继承值: %d → %d", before.Cur, after.Cur)
	}
}
