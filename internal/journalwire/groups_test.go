package journalwire

import "testing"

func TestSeverityGroups(t *testing.T) {
	want := [...]Group{ErrorGroup, ErrorGroup, ErrorGroup, ErrorGroup, WarningGroup, InfoGroup, InfoGroup, DebugGroup}
	for severity, group := range want {
		if got := GroupForSeverity(uint8(severity)); got != group {
			t.Fatalf("severity=%d group=%s，期望 %s", severity, got.Name(), group.Name())
		}
		if uint8(severity) > group.MaxSeverity() {
			t.Fatalf("组阈值丢弃了 severity=%d", severity)
		}
	}
	for group := DebugGroup; group < GroupCount; group++ {
		if got := group.SocketPath(); got != "/run/systemd/journal.mct-syslogger-"+group.Name()+"/socket" {
			t.Fatalf("原生入口不符合等级空间: %s", got)
		}
	}
}
