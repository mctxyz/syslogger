package journalwire

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"io"
	"strings"
	"testing"

	"github.com/mctxyz/syslogger/internal/identity"
)

// readFields 在测试中独立解码原生字段，验证发送器生成的实际字节。
func readFields(t *testing.T, data []byte) map[string]string {
	t.Helper()
	reader := bufio.NewReader(bytes.NewReader(data))
	fields := map[string]string{}
	for {
		line, err := reader.ReadString('\n')
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		line = strings.TrimSuffix(line, "\n")
		if name, value, plain := strings.Cut(line, "="); plain {
			fields[name] = value
			continue
		}
		var length uint64
		if err := binary.Read(reader, binary.LittleEndian, &length); err != nil {
			t.Fatal(err)
		}
		value := make([]byte, length)
		if _, err := io.ReadFull(reader, value); err != nil {
			t.Fatal(err)
		}
		if delimiter, err := reader.ReadByte(); err != nil || delimiter != '\n' {
			t.Fatalf("二进制字段缺少结束分隔符: %v", err)
		}
		fields[line] = string(value)
	}
	return fields
}

func TestIdentityFieldsAndMessagePreservation(t *testing.T) {
	id := identity.Instance{Project: "项目\nA", InstanceID: "实例\x00\nB"}
	header := Header(id, "rpc\n读取")
	before := bytes.Clone(header)
	body := []byte(`{"message":"test","fields":{"large":18446744073709551615}}`)
	for severity := uint8(0); severity < 8; severity++ {
		fields := readFields(t, Message(header, body, severity))
		if fields[ProjectField] != id.Project || fields[InstanceField] != id.InstanceID || fields[ComponentField] != "rpc\n读取" {
			t.Fatalf("原生身份原文被改变: %+v", fields)
		}
		if fields["SYSLOG_IDENTIFIER"] != identity.SyslogTag(id.Project) || fields["SYSLOG_FACILITY"] != "1" || fields["SYSLOG_PID"] == "" {
			t.Fatalf("syslog 兼容字段不符: %+v", fields)
		}
		if fields["PRIORITY"] != string('0'+severity) || fields["MESSAGE"] != string(body) {
			t.Fatalf("严重程度或 JSON 原文改变: %+v", fields)
		}
	}
	if !bytes.Equal(header, before) {
		t.Fatal("固定协议头被后续消息覆盖")
	}
}

func TestBinaryMessageField(t *testing.T) {
	body := []byte("line one\nline two\x00")
	fields := readFields(t, Message(nil, body, 6))
	if fields["MESSAGE"] != string(body) {
		t.Fatal("二进制消息值没有保持完整")
	}
}
