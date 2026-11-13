//go:build linux

package tool

import (
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// holdOrphan backgrounds a memory holder from a subshell that exits at once:
// the holder is reparented to pid 1 and so leaves the task's ppid tree, but
// stays in its process group — the set treeKill actually kills. The trailing
// ":" in each branch stops the shell from exec-ing the final sleep in place,
// which would drop the allocation the test depends on.
const holdOrphan = `( { x=a; while [ ${#x} -lt 200000000 ]; do x="$x$x"; done; sleep 30; :; } & ) ; sleep 30; :`

// forkShared allocates once and then forks five subshells that hold the same
// pages copy-on-write. Summing per-process RSS counts those pages six times
// (~880 MiB measured); the real footprint is ~145 MiB.
const forkShared = `x=a; while [ ${#x} -lt 100000000 ]; do x="$x$x"; done; for i in 1 2 3 4 5; do { sleep 25; :; } & done; sleep 25; :`

// A leaker whose parent exits must still be caught. Measuring the ppid tree
// instead of the process group loses it the moment init adopts it: the walk
// reports the few MiB of the surviving shell while the orphan holds hundreds,
// and the guard never fires on the shape a shell script produces every time it
// starts its real work with "cmd &".
func TestMemGuardCatchesOrphanedDescendant(t *testing.T) {
	t.Setenv("AGENT_CORE_TASK_MEM_CAP", "64m")
	m := newTestManager(t)
	task, err := m.Spawn(SpawnSpec{Command: holdOrphan})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	m.Background(task.ID)
	info := waitTerminalIn(t, m, task.ID, 30*time.Second)
	if info.Status != TaskFailed || info.Reason != terminateMemory {
		t.Fatalf("status=%s reason=%q, want failed/memory — the orphan left the ppid tree but not the process group", info.Status, info.Reason)
	}
}

// The inverse: a parent that forks workers shares its pages, and summing RSS
// over the group multiplies them by the number of holders. Killing on that sum
// would take down ordinary multiprocessing work and tell the model to rewrite a
// command that never leaked, so a group over the cheap bound must be confirmed
// against Pss before it dies.
func TestMemGuardSparesSharedPages(t *testing.T) {
	// 目标形状：sum(RSS) 越上限、sum(Pss) 不越——COW 页被每个持有者各计一遍，
	// 按 sum(RSS) 杀会误杀普通的 multiprocessing 负载。
	//
	// 上限取 600m 是量出来的，不是拍的。工作负载先倍增拼接到 1.3 亿字符再 fork：
	//   · 独占阶段（无共享）峰值 RSS≈Pss≈425MiB —— 倍增拼接时旧串与新串同时在堆上，
	//     峰值约为稳定值(131MiB)的 3.3 倍。**此阶段 RSS 与 Pss 不可分，两层判定
	//     分不出来，双双越线就会被正确击杀**，所以工作负载的独占峰值必须低于上限。
	//   · fork 5 个子 shell 后：sumRSS≈786MiB（6×131）越上限，sumPss≈129MiB 不越。
	// 故上限须落在 (425, 786) 之间；600m 两侧各留 ~30% 余量。
	//
	// 注：阈值从 memCapBytes() 派生而非硬编码字面量——否则改上限时会静默失效。
	t.Setenv("AGENT_CORE_TASK_MEM_CAP", "600m")
	m := newTestManager(t)
	task, err := m.Spawn(SpawnSpec{Command: forkShared})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	m.Background(task.ID)

	// Wait for the two measures to actually diverge: while the parent is still
	// growing its string alone there is nothing shared yet and RSS == Pss, so
	// polling on RSS alone would sample before the forks exist.
	// 与 memGuardPass 同一口径：量足迹（root 组 ∪ 后代组），不是量 root 自己
	// ——沙箱可能把执行体放进另一个组，直接拿 cmd.Process.Pid 当 pgid 只会
	// 量到包装进程，RSS 永远涨不到阈值。
	cap := memCapBytes()
	under := cap / 4 * 3 // Pss 须留足余量，别贴着上限判「不越」
	root := task.cmd.Process.Pid
	var rss, pss int64
	var ok bool
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		// 每轮重算足迹：后代是在运行中才陆续出现的，一次取快照会漏。
		pgids := descendantPgrps([]int{root})[root]
		rss = 0
		byPgrp := pgrpRSS(pgids)
		for _, pg := range pgids {
			rss += byPgrp[pg]
		}
		pss, ok = pgrpPSSAll(pgids)
		if ok && rss > cap && pss < under {
			break
		}
		if info, done := m.Get(task.ID); done && info.Status.Terminal() {
			// 上限已选在独占峰值之上（见函数头），所以共享建立前就被杀
			// 不再是「独占阶段越线」的正常情况——这里报失败是对的。
			t.Fatalf("task died at %s/%s before the shared allocation was resident (rss=%dMiB pss=%dMiB cap=%dMiB)",
				info.Status, info.Reason, rss>>20, pss>>20, cap>>20)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !ok || rss <= cap || pss >= under {
		t.Skipf("workload did not land between the two measures on this host (rss=%dMiB pss=%dMiB cap=%dMiB ok=%v)",
			rss>>20, pss>>20, cap>>20, ok)
	}

	// Several guard passes with the group sitting over the cheap bound.
	time.Sleep(3 * memSampleInterval)
	info, _ := m.Get(task.ID)
	if info.Status.Terminal() {
		t.Fatalf("killed a group whose real footprint (%d MiB Pss) is under the %s cap; only its double-counted RSS (%d MiB) was over",
			pss>>20, humanBytes(memCapBytes()), rss>>20)
	}
	m.Kill(task.ID)
}

// pgrpRSS must see every member of the group, including one the task's own
// parent-child links no longer reach.
func TestPgrpRSSCoversWholeGroup(t *testing.T) {
	m := newTestManager(t)
	task, err := m.Spawn(SpawnSpec{Command: holdOrphan})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	defer m.Kill(task.ID)
	pgid := task.cmd.Process.Pid

	var measured, want int64
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		measured = pgrpRSS([]int{pgid})[pgid]
		want = groupRSSViaProc(t, pgid)
		if want > 100<<20 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if want <= 100<<20 {
		t.Skipf("holder never became resident on this host (%d MiB)", want>>20)
	}
	// Both read /proc a moment apart, so allow drift; the failure this guards
	// against is an order-of-magnitude miss, not a few MiB.
	if diff := want - measured; diff > 32<<20 || diff < -(32<<20) {
		t.Fatalf("pgrpRSS = %d MiB, independent group sum = %d MiB", measured>>20, want>>20)
	}
}

// groupRSSViaProc sums VmRSS over the process group, reading /proc/<pid>/status
// rather than the stat field pgrpRSS parses, so the two do not share a bug.
func groupRSSViaProc(t *testing.T, pgid int) int64 {
	t.Helper()
	entries, err := os.ReadDir("/proc")
	if err != nil {
		t.Fatalf("read /proc: %v", err)
	}
	var total int64
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		g, err := syscall.Getpgid(pid)
		if err != nil || g != pgid {
			continue
		}
		b, err := os.ReadFile(procFile(pid, "status"))
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(b), "\n") {
			if !strings.HasPrefix(line, "VmRSS:") {
				continue
			}
			f := strings.Fields(line)
			if len(f) >= 2 {
				if kb, err := strconv.ParseInt(f[1], 10, 64); err == nil {
					total += kb << 10
				}
			}
			break
		}
	}
	return total
}

// The derived default must be a usable cap, not a constant and not something
// that would kill ordinary work on the machine running the tests.
func TestDefaultMemCapFollowsHostBudget(t *testing.T) {
	budget, ok := hostMemBudget()
	if !ok {
		t.Skip("host memory budget unreadable")
	}
	if budget < minMemCap {
		t.Skipf("host budget %s is below the floor", humanBytes(budget))
	}
	got := defaultMemCap()
	if got > budget {
		t.Fatalf("cap %s exceeds the budget %s it is meant to protect", humanBytes(got), humanBytes(budget))
	}
	if got < minMemCap {
		t.Fatalf("cap %s is below the floor %s", humanBytes(got), humanBytes(minMemCap))
	}
}
