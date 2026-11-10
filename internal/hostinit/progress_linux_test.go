package hostinit

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mctxyz/syslogger/internal/catalog"
	"github.com/mctxyz/syslogger/internal/journalwire"
)

func testInitializer(output io.Writer) *initializer {
	return &initializer{
		ctx:    context.Background(),
		logger: log.New(output, "", 0),
	}
}

func TestDirectoryProgressMatchesActualCreation(t *testing.T) {
	var output bytes.Buffer
	init := testInitializer(&output)
	path := filepath.Join(t.TempDir(), "catalog")
	if err := init.prepareCatalog(path, os.Geteuid(), os.Getegid()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), fmt.Sprintf("[创建目录] %q", path)) {
		t.Fatalf("未报告实际创建的目录: %s", &output)
	}
	if !strings.Contains(output.String(), fmt.Sprintf("[设置目录权限] %q mode=2775", path)) {
		t.Fatalf("没有使用 Unix 权限表示: %s", &output)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode()&(os.ModePerm|os.ModeSetgid) != catalog.DirectoryMode {
		t.Fatalf("过程输出与实际权限不一致: %v, %v", info, err)
	}
	output.Reset()
	if err := init.prepareCatalog(path, os.Geteuid(), os.Getegid()); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "[创建目录]") || !strings.Contains(output.String(), fmt.Sprintf("[复用目录] %q", path)) {
		t.Fatalf("重复初始化误报创建: %s", &output)
	}
	if strings.Contains(output.String(), "[设置") {
		t.Fatalf("正确权限被重复修改: %s", &output)
	}
	file := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(file, []byte("保留"), 0600); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if directory, _, err := init.openDirectory(file, 0755); err == nil {
		directory.Close()
		t.Fatal("将普通文件作为目录打开")
	}
	if strings.Contains(output.String(), "[复用目录]") || strings.Contains(output.String(), "[创建目录]") {
		t.Fatalf("打开失败仍声称目录可复用: %s", &output)
	}
}

func TestConfigProgressDistinguishesActualActions(t *testing.T) {
	var output bytes.Buffer
	init := testInitializer(&output)
	path := filepath.Join(t.TempDir(), "journal.conf.d", configName)
	if err := init.writeJournalConfig(path, journalConfig("5G")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), fmt.Sprintf("[创建配置] %q", path)) {
		t.Fatalf("创建配置没有对应输出: %s", &output)
	}
	output.Reset()
	if err := init.writeJournalConfig(path, journalConfig("10G")); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "[创建配置]") || !strings.Contains(output.String(), fmt.Sprintf("[更新配置] %q", path)) {
		t.Fatalf("更新配置误报为创建: %s", &output)
	}
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), "SystemMaxUse=10G") {
		t.Fatalf("配置未实际更新: %s, %v", data, err)
	}
	admin := "[Journal]\nCompress=no\n"
	if err := os.WriteFile(path, []byte(admin), 0644); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if err := init.writeJournalConfig(path, journalConfig("5G")); err == nil {
		t.Fatal("覆盖了非受管配置")
	}
	if strings.Contains(output.String(), "[创建配置]") || strings.Contains(output.String(), "[更新配置]") {
		t.Fatalf("写入失败仍报告完成: %s", &output)
	}
	data, err = os.ReadFile(path)
	if err != nil || string(data) != admin {
		t.Fatalf("管理员配置被改写: %q, %v", data, err)
	}
}

func TestCommandProgressPreservesArgumentsAndErrors(t *testing.T) {
	var output bytes.Buffer
	init := testInitializer(&output)
	directory := t.TempDir()
	program := filepath.Join(directory, "query-probe")
	if err := os.WriteFile(program, []byte("#!/bin/sh\nprintf 'query-result-not-for-progress\\n'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory)
	args := []string{"argument with spaces", "line\nbreak"}
	data, err := init.command("query-probe", args...)
	if err != nil || data != "query-result-not-for-progress\n" {
		t.Fatalf("命令返回值改变: %q, %v", data, err)
	}
	if strings.Contains(output.String(), "query-result-not-for-progress") || !strings.Contains(output.String(), fmt.Sprintf("参数=%q", args)) {
		t.Fatalf("查询结果泄露或 argv 未转义: %s", &output)
	}
	if strings.Count(output.String(), "\n") != 1 {
		t.Fatalf("参数中的换行破坏过程记录: %q", output.String())
	}
	if err := os.WriteFile(program, []byte("#!/bin/sh\nprintf 'failure-detail\\n'\nexit 7\n"), 0700); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if _, err := init.command("query-probe"); err == nil || !strings.Contains(err.Error(), "failure-detail") {
		t.Fatalf("命令失败详情未沿原路径传递: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	init.ctx = ctx
	if _, err := init.command("query-probe"); !errors.Is(err, context.Canceled) {
		t.Fatalf("命令取消原因被掩盖: %v", err)
	}
}

func TestActivationProgressDoesNotClaimReadinessOnFailure(t *testing.T) {
	var output bytes.Buffer
	init := testInitializer(&output)
	directory := t.TempDir()
	program := `#!/bin/sh
if [ "$1" = add-wants ]; then
    printf 'Created symlink for test socket.\n'
fi
if [ "$1" = is-active ]; then
    exit 1
fi
`
	if err := os.WriteFile(filepath.Join(directory, "systemctl"), []byte(program), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory)
	if err := init.activateJournal(journalwire.InfoGroup); err == nil {
		t.Fatal("假 socket 未就绪仍报告激活成功")
	}
	if !strings.Contains(output.String(), "Created symlink for test socket.") {
		t.Fatalf("丢弃了 systemctl 的链接创建信息: %s", &output)
	}
	if !strings.Contains(output.String(), "systemd-journald@mct-syslogger-info.service") || !strings.Contains(output.String(), `"restart"`) {
		t.Fatalf("缺少实际重启命令: %s", &output)
	}
	if strings.Contains(output.String(), "[检查日志入口]") || strings.Contains(output.String(), "初始化完成") {
		t.Fatalf("失败后仍报告就绪或完成: %s", &output)
	}
}
