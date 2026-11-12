//go:build !linux

// 非 Linux 平台的 rlimit stub：prlimit(2) 是 Linux 专有，macOS/Windows
// 上限额不可用——返回 nil 保持工具可用（限额是加固项不是功能项）。macOS 若需要
// 可走 launchd rlimits 或 shell ulimit 包装，出现需求再实现。
package tool

// Rlimits 与 Linux 版同形（配置面跨平台一致），此处不生效。
type Rlimits struct {
	CPUSeconds   uint64
	AddressSpace uint64
	OpenFiles    uint64
	Processes    uint64
}

// DefaultRlimits 同 Linux 默认值（仅作配置回显一致）。
func DefaultRlimits() Rlimits {
	return Rlimits{CPUSeconds: 300, AddressSpace: 2 << 30, OpenFiles: 4096, Processes: 256}
}

// FromEnv 同 Linux 解析（仅作配置回显一致）。
func FromEnv() Rlimits { return DefaultRlimits() }

// ApplyTo 非 Linux 恒 nil（不生效、不报错）。
func (r Rlimits) ApplyTo(_ int) error { return nil }
