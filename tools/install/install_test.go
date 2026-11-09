//go:build linux

package install_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"
)

const fakeCLI = `#!/usr/bin/env bash
set -euo pipefail
if [[ "$1" == init && "${!#}" == --help ]]; then
    for argument in "$@"; do
        if [[ "$argument" == --user=* && "$MOCK_FIXED_USER" == 1 ]]; then
            exit "$MOCK_ACCOUNT_STATUS"
        fi
    done
    if [[ $# -gt 2 && "$MOCK_FOUR_GROUPS" == 0 ]]; then
        exit 2
    fi
    exit "$MOCK_HELP_STATUS"
fi
printf '%s\0' "$@" > "$MOCK_LOG/init"
exit "$MOCK_INIT_STATUS"
`

const fakeCurl = `#!/usr/bin/env bash
set -euo pipefail
output=
url=
while [[ $# -gt 0 ]]; do
    case "$1" in
        --output)
            output=$2
            shift 2
            ;;
        --write-out|--proto|--proto-redir)
            shift 2
            ;;
        --fail|--location|--silent|--show-error)
            shift
            ;;
        *)
            url=$1
            shift
            ;;
    esac
done
printf '%s\n' "$url" >> "$MOCK_LOG/curl"
if [[ "$url" == https://github.com/mctxyz/syslogger/releases/latest ]]; then
    printf '%s' "$MOCK_RELEASE_URL"
    exit 0
fi
if [[ "$url" != https://github.com/mctxyz/syslogger/releases/download/test-release/* ]]; then
    exit 22
fi
if [[ "$MOCK_DOWNLOAD_FAIL" == 1 ]]; then
    exit 22
fi
cp -- "$MOCK_ASSETS/${url##*/}" "$output"
if [[ -n "$MOCK_SIGNAL" && "$url" != */SHA256SUMS ]]; then
    kill -s "$MOCK_SIGNAL" "$PPID"
fi
`

const fakeInstall = `#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "$MOCK_LOG/install"
case "$MOCK_INSTALL_FAIL:${!#}" in
    docs:*/tools/README.md|binary:*/bin/mct-syslogger)
        exit 1
        ;;
esac
exec "$REAL_INSTALL" "$@"
`

type installerFixture struct {
	prefix   string
	assets   string
	work     string
	log      string
	bin      string
	settings map[string]string
}

func writeFile(t *testing.T, path, contents string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), mode); err != nil {
		t.Fatal(err)
	}
}

func newFixture(t *testing.T) *installerFixture {
	t.Helper()
	directory := t.TempDir()
	f := &installerFixture{
		prefix: filepath.Join(directory, "install with spaces"),
		assets: filepath.Join(directory, "assets"),
		work:   filepath.Join(directory, "work"),
		log:    filepath.Join(directory, "log"),
		bin:    filepath.Join(directory, "bin"),
		settings: map[string]string{
			"MOCK_OS":             "Linux",
			"MOCK_ARCH":           "x86_64",
			"MOCK_UID":            "1000",
			"MOCK_RELEASE_URL":    "https://github.com/mctxyz/syslogger/releases/tag/test-release",
			"MOCK_HELP_STATUS":    "0",
			"MOCK_FOUR_GROUPS":    "1",
			"MOCK_FIXED_USER":     "1",
			"MOCK_ACCOUNT_STATUS": "2",
			"MOCK_APP_EXISTS":     "1",
			"MOCK_INIT_STATUS":    "0",
			"MOCK_DOWNLOAD_FAIL":  "0",
			"MOCK_SUDO_FAIL":      "0",
			"MOCK_INSTALL_FAIL":   "",
			"MOCK_SIGNAL":         "",
		},
	}
	for _, path := range []string{f.assets, f.work, f.log, f.bin} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	realInstall, err := exec.LookPath("install")
	if err != nil {
		t.Fatal(err)
	}
	f.settings["REAL_INSTALL"] = realInstall
	commands := map[string]string{
		"curl": fakeCurl,
		"uname": `#!/usr/bin/env bash
case "$1" in
    -s)
        printf '%s\n' "$MOCK_OS"
        ;;
    -m)
        printf '%s\n' "$MOCK_ARCH"
        ;;
esac
`,
		"id": `#!/usr/bin/env bash
printf '%s\n' "$MOCK_UID"
`,
		"getent": `#!/usr/bin/env bash
printf '%s\n' "$*" >> "$MOCK_LOG/getent"
[[ $# -eq 4 && "$1" == -s && "$2" == files && "$3" == passwd && "$4" == app && "$MOCK_APP_EXISTS" == 1 ]]
`,
		"sudo": `#!/usr/bin/env bash
set -euo pipefail
printf 'sudo\n' >> "$MOCK_LOG/sudo"
if [[ "$MOCK_SUDO_FAIL" == 1 ]]; then
    exit 1
fi
exec "$@"
`,
		"install": fakeInstall,
	}
	for name, contents := range commands {
		writeFile(t, filepath.Join(f.bin, name), contents, 0o755)
	}
	f.writeAssets(t, false)
	return f
}

func (f *installerFixture) writeAssets(t *testing.T, symlink bool) {
	t.Helper()
	linkTarget := filepath.Join(f.assets, "link-target")
	if symlink {
		writeFile(t, linkTarget, fakeCLI, 0o755)
	}
	var checksums strings.Builder
	for _, arch := range []string{"amd64", "arm64"} {
		name := "mct-syslogger_linux_" + arch
		var data bytes.Buffer
		gzipWriter := gzip.NewWriter(&data)
		writer := tar.NewWriter(gzipWriter)
		files := map[string]string{
			"mct-syslogger":                fakeCLI,
			"tools/README.md":              "使用说明",
			"tools/THIRD_PARTY_NOTICES.md": "第三方许可",
			"docs/journal.md":              "专用 journal 部署说明",
		}
		for path, contents := range files {
			header := &tar.Header{Name: name + "/" + path, Mode: 0o644, Size: int64(len(contents))}
			if path == "mct-syslogger" {
				header.Mode = 0o755
				if symlink {
					header.Typeflag = tar.TypeSymlink
					header.Linkname = linkTarget
					header.Size = 0
				}
			}
			if err := writer.WriteHeader(header); err != nil {
				t.Fatal(err)
			}
			if header.Typeflag != tar.TypeSymlink {
				if _, err := writer.Write([]byte(contents)); err != nil {
					t.Fatal(err)
				}
			}
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		if err := gzipWriter.Close(); err != nil {
			t.Fatal(err)
		}
		asset := name + ".tar.gz"
		if err := os.WriteFile(filepath.Join(f.assets, asset), data.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&checksums, "%x  ./%s\n", sha256.Sum256(data.Bytes()), asset)
	}
	writeFile(t, filepath.Join(f.assets, "SHA256SUMS"), checksums.String(), 0o644)
}

func (f *installerFixture) environment() []string {
	environment := append(os.Environ(),
		"PATH="+f.bin+":"+os.Getenv("PATH"),
		"TMPDIR="+f.work,
		"MOCK_LOG="+f.log,
		"MOCK_ASSETS="+f.assets,
	)
	for name, value := range f.settings {
		environment = append(environment, name+"="+value)
	}
	return environment
}

// run 使用真实控制终端驱动从管道读入的脚本；系统命令替身只操作临时目录。
func (f *installerFixture) run(t *testing.T, input string, tty bool, args ...string) (string, int) {
	t.Helper()
	path, err := filepath.Abs("install.sh")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var cmd *exec.Cmd
	if tty {
		script, err := exec.LookPath("script")
		if err != nil {
			t.Fatal("PTY 测试需要 util-linux 的 script 命令")
		}
		command := "cat '" + strings.ReplaceAll(path, "'", "'\\''") + "' | bash -s --"
		for _, argument := range args {
			command += " '" + strings.ReplaceAll(argument, "'", "'\\''") + "'"
		}
		cmd = exec.CommandContext(ctx, script, "--quiet", "--return", "--echo", "never", "--command", command, "/dev/null")
	} else {
		cmd = exec.CommandContext(ctx, "bash", append([]string{path}, args...)...)
		// 新会话确保无控制终端，而不只是把 stdin 改为管道。
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	}
	cmd.Env = f.environment()
	cmd.Stdin = strings.NewReader(input)
	cmd.WaitDelay = time.Second
	output, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("交互脚本超时: %s", output)
	}
	status := 0
	if err != nil {
		var exitError *exec.ExitError
		if !errors.As(err, &exitError) {
			t.Fatalf("启动安装脚本: %v, %s", err, output)
		}
		status = exitError.ExitCode()
	}
	entries, err := os.ReadDir(f.work)
	if err != nil || len(entries) != 0 {
		t.Fatalf("下载临时目录未清理: %v, %v\n%s", entries, err, output)
	}
	return string(output), status
}

func (f *installerFixture) input(confirmation string) string {
	return f.prefix + "\n" + confirmation + "\n"
}

func (f *installerFixture) initInput(capacity, confirmation string) string {
	return f.prefix + "\n" + capacity + "\n\n\n\n" + confirmation + "\n"
}

func readLog(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func (f *installerFixture) assertNoInit(t *testing.T) {
	t.Helper()
	if got := readLog(t, filepath.Join(f.log, "init")); got != "" {
		t.Fatalf("意外执行 init: %q", got)
	}
}

// assertInstalled 核对两种安装模式共有的文件、下载版本及提权行为。
func (f *installerFixture) assertInstalled(t *testing.T, arch string) {
	t.Helper()
	for path, mode := range map[string]os.FileMode{
		"bin/mct-syslogger":                                    0o755,
		"share/doc/mct-syslogger/tools/README.md":              0o644,
		"share/doc/mct-syslogger/tools/THIRD_PARTY_NOTICES.md": 0o644,
		"share/doc/mct-syslogger/docs/journal.md":              0o644,
	} {
		info, err := os.Stat(filepath.Join(f.prefix, path))
		if err != nil || info.Mode().Perm() != mode {
			t.Fatalf("安装文件或模式不符: %s, %v", path, err)
		}
	}
	requests := strings.Split(strings.TrimSpace(readLog(t, filepath.Join(f.log, "curl"))), "\n")
	wantRequests := []string{
		"https://github.com/mctxyz/syslogger/releases/latest",
		"https://github.com/mctxyz/syslogger/releases/download/test-release/mct-syslogger_linux_" + arch + ".tar.gz",
		"https://github.com/mctxyz/syslogger/releases/download/test-release/SHA256SUMS",
	}
	if !reflect.DeepEqual(requests, wantRequests) {
		t.Fatalf("下载未固定同一 tag: %q", requests)
	}
	usedSudo := readLog(t, filepath.Join(f.log, "sudo")) != ""
	if usedSudo != (f.settings["MOCK_UID"] != "0") {
		t.Fatalf("提权路径不符: %v", usedSudo)
	}
}

func TestInstallerUpdatesWithoutInitialization(t *testing.T) {
	for _, test := range []struct {
		name      string
		machine   string
		arch      string
		uid       string
		installed bool
	}{
		{name: "first-install-without-app", machine: "x86_64", arch: "amd64", uid: "1000"},
		{name: "update-existing-install", machine: "aarch64", arch: "arm64", uid: "0", installed: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newFixture(t)
			f.settings["MOCK_ARCH"] = test.machine
			f.settings["MOCK_UID"] = test.uid
			f.settings["MOCK_APP_EXISTS"] = "0"
			f.settings["MOCK_INIT_STATUS"] = "42"
			target := filepath.Join(f.prefix, "bin/mct-syslogger")
			document := filepath.Join(f.prefix, "share/doc/mct-syslogger/tools/README.md")
			if test.installed {
				for _, path := range []string{target, document} {
					if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
						t.Fatal(err)
					}
				}
				writeFile(t, target, "old binary", 0o755)
				writeFile(t, document, "old documentation", 0o644)
			}
			output, status := f.run(t, f.input("y"), true)
			if status != 0 {
				t.Fatalf("默认安装或更新失败: %d\n%s", status, output)
			}
			f.assertNoInit(t)
			f.assertInstalled(t, test.arch)
			if got := readLog(t, filepath.Join(f.log, "getent")); got != "" {
				t.Fatalf("默认更新不应查询初始化账户: %q", got)
			}
			if readLog(t, target) != fakeCLI || readLog(t, document) != "使用说明" {
				t.Fatal("默认模式未更新命令及配套文档")
			}
		})
	}
}

func TestInstallerInstallsAndInitializes(t *testing.T) {
	for _, test := range []struct {
		name     string
		machine  string
		arch     string
		uid      string
		capacity string
	}{
		{"amd64-sudo-default", "x86_64", "amd64", "1000", ""},
		{"arm64-root-custom", "aarch64", "arm64", "0", "10G"},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newFixture(t)
			f.settings["MOCK_ARCH"] = test.machine
			f.settings["MOCK_UID"] = test.uid
			if test.arch == "arm64" {
				path := filepath.Join(f.assets, "SHA256SUMS")
				writeFile(t, path, strings.ReplaceAll(readLog(t, path), "./mct-syslogger", "mct-syslogger"), 0o644)
			}
			output, status := f.run(t, f.initInput(test.capacity, "y"), true, "--init")
			if status != 0 {
				t.Fatalf("安装失败: %d\n%s", status, output)
			}
			capacity := test.capacity
			if capacity == "" {
				capacity = "1G"
			}
			got := strings.Split(strings.TrimSuffix(readLog(t, filepath.Join(f.log, "init")), "\x00"), "\x00")
			want := []string{"init", "--journal-debug-max-use=" + capacity, "--journal-info-max-use=2G", "--journal-warning-max-use=1G", "--journal-error-max-use=1G"}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("初始化参数不符: got=%q want=%q", got, want)
			}
			f.assertInstalled(t, test.arch)
		})
	}
}

func TestInstallerStopsBeforeSystemChanges(t *testing.T) {
	for _, name := range []string{"cancel", "cancel-init", "cancel-default-prefix", "no-tty", "eof", "unsupported-os", "unsupported-arch", "missing-app", "download", "bad-digest", "missing-digest", "duplicate-digest", "old-release", "single-space-release", "selectable-user-release", "unexpected-account-probe", "symlink", "release-url", "term", "interrupt"} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			input, tty := f.input("y"), true
			var args []string
			expectedStatus := -1
			switch name {
			case "cancel":
				input = f.input("n")
				expectedStatus = 0
			case "cancel-init":
				args = []string{"--init"}
				input = f.initInput("", "n")
				expectedStatus = 0
			case "cancel-default-prefix":
				input = "\nn\n"
				expectedStatus = 0
			case "no-tty":
				tty = false
			case "eof":
				input = "\x04"
			case "unsupported-os":
				f.settings["MOCK_OS"] = "Darwin"
			case "unsupported-arch":
				f.settings["MOCK_ARCH"] = "riscv64"
			case "missing-app":
				f.settings["MOCK_APP_EXISTS"] = "0"
				args = []string{"--init"}
			case "download":
				f.settings["MOCK_DOWNLOAD_FAIL"] = "1"
			case "bad-digest":
				writeFile(t, filepath.Join(f.assets, "SHA256SUMS"), strings.Repeat("0", 64)+"  ./mct-syslogger_linux_amd64.tar.gz\n", 0o644)
			case "missing-digest":
				writeFile(t, filepath.Join(f.assets, "SHA256SUMS"), strings.Repeat("0", 64)+"  ./unrelated.tar.gz\n", 0o644)
			case "duplicate-digest":
				path := filepath.Join(f.assets, "SHA256SUMS")
				text := readLog(t, path)
				writeFile(t, path, text+text, 0o644)
			case "old-release":
				f.settings["MOCK_HELP_STATUS"] = "2"
			case "single-space-release":
				f.settings["MOCK_FOUR_GROUPS"] = "0"
			case "selectable-user-release":
				f.settings["MOCK_FIXED_USER"] = "0"
			case "unexpected-account-probe":
				f.settings["MOCK_ACCOUNT_STATUS"] = "127"
			case "symlink":
				f.writeAssets(t, true)
			case "release-url":
				f.settings["MOCK_RELEASE_URL"] = "https://other.invalid/releases/tag/test-release"
			case "term":
				f.settings["MOCK_SIGNAL"] = "TERM"
				expectedStatus = 143
			case "interrupt":
				f.settings["MOCK_SIGNAL"] = "INT"
				expectedStatus = 130
			}
			output, status := f.run(t, input, tty, args...)
			if expectedStatus >= 0 {
				if status != expectedStatus {
					t.Fatalf("退出码不符: got=%d want=%d\n%s", status, expectedStatus, output)
				}
			} else if status == 0 {
				t.Fatalf("异常路径未失败: %s", output)
			}
			f.assertNoInit(t)
			if name == "cancel-default-prefix" && !strings.Contains(output, "/usr/local/bin/mct-syslogger") {
				t.Fatalf("默认安装路径不符: %s", output)
			}
			if got := readLog(t, filepath.Join(f.log, "install")); got != "" {
				t.Fatalf("确认前或校验失败后写入系统: %q", got)
			}
		})
	}
}

func TestInstallerInstallFailureDoesNotRunOldBinary(t *testing.T) {
	for _, failure := range []string{"sudo", "docs", "binary", "target-directory"} {
		for _, initialize := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/init=%v", failure, initialize), func(t *testing.T) {
				f := newFixture(t)
				target := filepath.Join(f.prefix, "bin/mct-syslogger")
				if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
					t.Fatal(err)
				}
				if failure == "target-directory" {
					if err := os.Mkdir(target, 0o755); err != nil {
						t.Fatal(err)
					}
				} else {
					writeFile(t, target, "#!/usr/bin/env bash\nprintf old > \"$MOCK_LOG/init\"\n", 0o755)
				}
				if failure == "sudo" {
					f.settings["MOCK_SUDO_FAIL"] = "1"
				} else {
					f.settings["MOCK_INSTALL_FAIL"] = failure
				}
				input := f.input("y")
				var args []string
				if initialize {
					input = f.initInput("", "y")
					args = []string{"--init"}
				}
				output, status := f.run(t, input, true, args...)
				if status == 0 {
					t.Fatalf("安装失败仍返回成功: %s", output)
				}
				f.assertNoInit(t)
			})
		}
	}
}

func TestInstallerInputAndInitFailure(t *testing.T) {
	f := newFixture(t)
	f.settings["MOCK_INIT_STATUS"] = "42"
	capacity := "5G; printf injected"
	input := "relative\n" + f.prefix + "\n" + capacity + "\n\n\n\ny\n"
	output, status := f.run(t, input, true, "--init")
	if status != 42 {
		t.Fatalf("init 失败状态丢失: %d\n%s", status, output)
	}
	want := "init\x00--journal-debug-max-use=" + capacity + "\x00--journal-info-max-use=2G\x00--journal-warning-max-use=1G\x00--journal-error-max-use=1G\x00"
	if got := readLog(t, filepath.Join(f.log, "init")); got != want {
		t.Fatalf("输入被执行或参数被拆分: %q", got)
	}
	if _, err := os.Stat(filepath.Join(f.prefix, "bin/mct-syslogger")); err != nil {
		t.Fatal("init 失败后没有保留已安装命令", err)
	}
}

func TestInstallerHelpWithoutTerminal(t *testing.T) {
	f := newFixture(t)
	output, status := f.run(t, "", false, "--help")
	if status != 0 || output == "" {
		t.Fatalf("无终端帮助失败: %d, %s", status, output)
	}
	if got := readLog(t, filepath.Join(f.log, "curl")); got != "" {
		t.Fatal("帮助路径触发下载")
	}
	f.assertNoInit(t)
}

func TestInstallerRejectsArgumentsBeforeDownload(t *testing.T) {
	for _, args := range [][]string{
		{"--unknown"},
		{"--init", "--unknown"},
		{"--help", "--init"},
		{"--init", "--init"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			f := newFixture(t)
			output, status := f.run(t, "", false, args...)
			if status == 0 {
				t.Fatalf("无效参数未拒绝: %s", output)
			}
			f.assertNoInit(t)
			for _, name := range []string{"curl", "install", "getent"} {
				if got := readLog(t, filepath.Join(f.log, name)); got != "" {
					t.Fatalf("无效参数触发了 %s: %q", name, got)
				}
			}
		})
	}
}

func TestDocumentedBootstrapPropagatesDownloadFailure(t *testing.T) {
	f := newFixture(t)
	text := readLog(t, "../README.md")
	start := strings.Index(text, "```bash\n")
	if start < 0 {
		t.Fatal("缺少一条命令安装示例")
	}
	block := strings.SplitN(text[start+len("```bash\n"):], "```", 2)[0]
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", "-c", block)
	cmd.Env = f.environment()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.WaitDelay = time.Second
	output, err := cmd.CombinedOutput()
	var exitError *exec.ExitError
	if ctx.Err() != nil || !errors.As(err, &exitError) || exitError.ExitCode() != 22 {
		t.Fatalf("下载失败未被正确反馈: %v, %s", err, output)
	}
	if got := readLog(t, filepath.Join(f.log, "curl")); got != "https://raw.githubusercontent.com/mctxyz/syslogger/main/tools/install/install.sh\n" {
		t.Fatalf("安装入口未请求脚本: %q", got)
	}
	f.assertNoInit(t)
	if got := readLog(t, filepath.Join(f.log, "install")); got != "" {
		t.Fatalf("入口下载失败后执行了安装: %q", got)
	}
}
