package restart

import (
	"os"
	"os/exec"
	"runtime"
	"syscall"

	"github.com/bestruirui/octopus/internal/utils/shutdown"
	"github.com/charmbracelet/log"
)

// Restart 按正常退出流程关闭各组件后, 以同一可执行文件原地重启进程。
// 先 Shutdown 是为了让统计缓存、请求日志正文落盘、数据库句柄都正常收尾, 否则重启等于丢一批数据。
// 关闭后监听端口已释放, 新进程可以直接重新 bind。
func Restart() {
	log.Warnf("restarting process")
	shutdown.Shutdown()

	execPath, err := os.Executable()
	if err != nil {
		log.Errorf("restart failed: resolve executable: %v", err)
		exitForSupervisor()
		return
	}

	log.Infof("restarting: %q %q", execPath, os.Args[1:])

	if runtime.GOOS == "windows" {
		// Windows 没有 exec 语义, 只能起新进程再退出自己。
		cmd := exec.Command(execPath, os.Args[1:]...)
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Start(); err != nil {
			log.Errorf("restart failed: start new process: %v", err)
		}
		os.Exit(0)
	}

	if err := syscall.Exec(execPath, os.Args, os.Environ()); err != nil {
		log.Errorf("restart failed: exec %s: %v", execPath, err)
		exitForSupervisor()
	}
}

// exitForSupervisor 在无法原地重启时直接退出, 交给 systemd 等进程管理器按 Restart 策略拉起。
func exitForSupervisor() {
	os.Exit(1)
}
