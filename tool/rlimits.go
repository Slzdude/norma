//go:build linux

// Package tool —— exec 点资源限额。
//
// 为什么在 exec 点加 setrlimit：agent 驱动的外部命令（rg/npm 乃至 bash 工具）
// 没有资源上限时，一条失控命令（死循环吃满内存/进程炸弹）能拖垮整个宿主进程。
// rlimit 与 bwrap 沙箱**正交**：sandbox_bwrap.go 管文件系统/命名空间隔离（防越权），
// 这里管资源用量（防耗尽），两者叠加使用。
//
// 实现：Start 之后对子进程 pid 调 prlimit(2)（不是 setrlimit——那会改宿主进程）。
// 竞态窗口=Start 返回到 prlimit 落地的毫秒级；严格方案是中间 helper 自设后 exec，
// 每条命令多一次 fork，当前量级不值得，出现「限额偶发不生效」实证再升级。
//
// 配置：默认值即刻生效；NORMA_AGENT_RLIMIT_CPU/AS/NOFILE/NPROC 可覆盖；
// config.json 的 agent.rlimits 节可经 FromValues 在服务端注入（配置面预留）。
package tool

import (
	"fmt"
	"os"
	"strconv"

	"golang.org/x/sys/unix"
)

// Rlimits 是子进程四类硬上限。零值=不设该项（沿用继承值）。
type Rlimits struct {
	CPUSeconds   uint64 // RLIMIT_CPU：CPU 秒（失控死循环的兜底）
	AddressSpace uint64 // RLIMIT_AS：字节（内存炸弹的兜底）
	OpenFiles    uint64 // RLIMIT_NOFILE：fd 数（fd 泄漏的兜底）
	Processes    uint64 // RLIMIT_NPROC：子进程数（进程炸弹的兜底）
}

// DefaultRlimits 是 exec 点的默认上限。取值理由：
//   - CPU 300s：单条工具命令合理上限（重编译/安装类走显式覆盖）；
//   - AS 2GiB：与常见容器内存硬限同量级，宿主进程侧再留余量；
//   - NOFILE 4096：并发扫描类工具常见 fd 需求的上界；
//   - NPROC 256：足以支撑并行编译/测试，远低于 fork 炸弹水位。
func DefaultRlimits() Rlimits {
	return Rlimits{CPUSeconds: 300, AddressSpace: 2 << 30, OpenFiles: 4096, Processes: 256}
}

// FromEnv 从 NORMA_AGENT_RLIMIT_CPU/AS/NOFILE/NPROC（十进制秒/字节/个数）覆盖默认值。
func FromEnv() Rlimits {
	r := DefaultRlimits()
	parse := func(name string, dst *uint64) {
		if v, err := strconv.ParseUint(os.Getenv(name), 10, 64); err == nil && v > 0 {
			*dst = v
		}
	}
	parse("NORMA_AGENT_RLIMIT_CPU", &r.CPUSeconds)
	parse("NORMA_AGENT_RLIMIT_AS", &r.AddressSpace)
	parse("NORMA_AGENT_RLIMIT_NOFILE", &r.OpenFiles)
	parse("NORMA_AGENT_RLIMIT_NPROC", &r.Processes)
	return r
}

// ApplyTo 把硬限落到 pid 对应的子进程（prlimit(2)）。限额是加固项：失败不阻断
// 执行，由调用方记日志——缺限额不该让工具整个不可用。
func (r Rlimits) ApplyTo(pid int) error {
	set := func(resource int, limit uint64) error {
		if limit == 0 {
			return nil
		}
		lim := unix.Rlimit{Cur: limit, Max: limit}
		if err := unix.Prlimit(pid, resource, &lim, nil); err != nil {
			return fmt.Errorf("prlimit(pid=%d, res=%d, lim=%d): %w", pid, resource, limit, err)
		}
		return nil
	}
	for _, s := range []struct {
		res int
		lim uint64
	}{
		{unix.RLIMIT_CPU, r.CPUSeconds},
		{unix.RLIMIT_AS, r.AddressSpace},
		{unix.RLIMIT_NOFILE, r.OpenFiles},
		{unix.RLIMIT_NPROC, r.Processes},
	} {
		if err := set(s.res, s.lim); err != nil {
			return err
		}
	}
	return nil
}
