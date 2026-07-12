package main

import (
	"testing"
)

// main() 只调用 cmd.Execute()。rootCmd 无子命令时打印 help 并返回 nil，
// 不触发 osExit。直接调用 main() 覆盖该行。
func TestMain_HelpPath(t *testing.T) {
	// rootCmd 默认无 args → Execute 打印 usage 返回 nil。
	// 注意：Execute 写 stdout/stderr，这里不捕获（只验证不 panic/不退出）。
	main()
}
