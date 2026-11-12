// RunLimited 启动命令并在 exec 窗口对子进程落 rlimit 硬限。
// 与 sandbox_bwrap 的隔离正交：那层管越权，这里管耗尽（见 rlimits.go 注释）。
// 限额落失败不阻断执行（加固项），告警走 stderr。
package tool

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
)

func RunLimited(cmd *exec.Cmd) ([]byte, error) {
	var buf bytes.Buffer
	cmd.Stdout = &buf
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	if err := FromEnv().ApplyTo(cmd.Process.Pid); err != nil {
		fmt.Fprintf(os.Stderr, "warn: rlimit 未生效（继续执行）: %v\n", err)
	}
	err := cmd.Wait()
	return buf.Bytes(), err
}
