// Package metrics 为 HTTP 和文件出口提供相同的三标签文本指标编码。
package metrics

import (
	"sort"
	"strconv"

	"github.com/mctxyz/syslogger/internal/identity"
	"github.com/mctxyz/syslogger/internal/record"
)

// Component 是采集复制出的完整状态及日志计数，管理器自身仅提供状态与时间。
type Component struct {
	Name         string
	Record       record.Record
	HasLogs      bool
	Rejected     uint64
	SendFailures uint64
}

// Encode 排序独立快照，以整数输出两个 Gauge 和两个日志 Counter。
func Encode(id identity.Instance, components []Component) []byte {
	sort.Slice(components, func(i, j int) bool { return components[i].Name < components[j].Name })
	var data []byte
	families := [...]struct{ name, kind, help string }{
		{"syslogger_component_state", "gauge", "组件健康状态：0=未知，1=健康，2=降级，3=不健康。"},
		{"syslogger_component_timestamp_seconds", "gauge", "当前健康记录的汇报时间，单位为 Unix 秒，尚无汇报时为 0。"},
		{"syslogger_component_log_rejected_total", "counter", "入队前拒收的日志调用累计次数。"},
		{"syslogger_component_log_send_failures_total", "counter", "后台发送失败的日志条目累计数。"},
	}
	for index, family := range families {
		data = append(data, "# HELP "+family.name+" "+family.help+"\n# TYPE "+family.name+" "+family.kind+"\n"...)
		for _, component := range components {
			if index >= 2 && !component.HasLogs {
				continue
			}
			data = append(data, family.name...)
			data = append(data, '{')
			data = appendLabel(data, "project", id.Project)
			data = append(data, ',')
			data = appendLabel(data, "instance_id", id.InstanceID)
			data = append(data, ',')
			data = appendLabel(data, "component", component.Name)
			data = append(data, '}', ' ')
			switch index {
			case 0:
				data = strconv.AppendInt(data, int64(component.Record.State), 10)
			case 1:
				var timestamp int64
				if component.Record.Reported {
					timestamp = component.Record.ObservedAt.Unix()
				}
				data = strconv.AppendInt(data, timestamp, 10)
			case 2:
				data = strconv.AppendUint(data, component.Rejected, 10)
			case 3:
				data = strconv.AppendUint(data, component.SendFailures, 10)
			}
			data = append(data, '\n')
		}
	}
	return data
}

func appendLabel(data []byte, key, value string) []byte {
	data = append(data, key...)
	data = append(data, '=', '"')
	for i := 0; i < len(value); i++ {
		switch value[i] {
		case '\\', '"':
			data = append(data, '\\', value[i])
		case '\n':
			data = append(data, '\\', 'n')
		default:
			data = append(data, value[i])
		}
	}
	return append(data, '"')
}
