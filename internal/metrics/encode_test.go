package metrics

import (
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mctxyz/syslogger/internal/identity"
	"github.com/mctxyz/syslogger/internal/record"
)

func metricFixture() []byte {
	return Encode(identity.Instance{Project: "项目", InstanceID: "a\"\\\nb"}, []Component{{Name: "rpc", Record: record.New(3, time.Unix(1792656000, 123)), HasLogs: true, Rejected: 2, SendFailures: 1}, {Name: "a", HasLogs: true}, {Name: "syslogger_manager", Record: record.New(1, time.Unix(100, 0))}})
}

func TestEncodeContract(t *testing.T) {
	text := string(metricFixture())
	samples := 0
	seen := map[string]bool{}
	for _, line := range strings.Split(text, "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		samples++
		labels := parseLabels(t, line)
		if len(labels) != 3 || labels["project"] != "项目" || labels["instance_id"] != "a\"\\\nb" {
			t.Fatalf("三标签身份不符: %s", line)
		}
		end := strings.LastIndex(line, "} ")
		if end < 0 || len(strings.Fields(line[end+2:])) != 1 || seen[line[:end+1]] {
			t.Fatal("样本格式或唯一性不符")
		}
		seen[line[:end+1]] = true
		if labels["component"] == "a" && !strings.HasSuffix(line, "} 0") {
			t.Fatal("初始状态、时间或计数不为零")
		}
	}
	if samples != 10 {
		t.Fatalf("样本数量不符: %d", samples)
	}
	for _, family := range []string{"state", "timestamp_seconds", "log_rejected_total", "log_send_failures_total"} {
		if strings.Count(text, "# HELP syslogger_component_"+family+" ") != 1 || strings.Count(text, "# TYPE syslogger_component_"+family+" ") != 1 {
			t.Fatalf("指标族元数据不符: %s", family)
		}
	}
	if !strings.Contains(text, `component="rpc"} 1792656000`) {
		t.Fatal("记录时间未按整数秒输出")
	}
}

func parseLabels(t *testing.T, line string) map[string]string {
	t.Helper()
	labels := map[string]string{}
	for _, match := range regexp.MustCompile(`([a-z_]+)=("(?:[^"\\]|\\.)*")`).FindAllStringSubmatch(line, -1) {
		value, err := strconv.Unquote(match[2])
		if err != nil {
			t.Fatal(err)
		}
		if _, exists := labels[match[1]]; exists {
			t.Fatal("重复标签")
		}
		labels[match[1]] = value
	}
	return labels
}

func TestPromtoolContract(t *testing.T) {
	path, err := exec.LookPath("promtool")
	if err != nil {
		t.Skip("当前工具链未安装 promtool")
	}
	command := exec.Command(path, "check", "metrics")
	command.Stdin = strings.NewReader(string(metricFixture()))
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("promtool 检查失败: %v\n%s", err, output)
	}
}
