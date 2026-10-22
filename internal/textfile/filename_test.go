package textfile

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"testing"

	"github.com/mctxyz/syslogger/internal/identity"
)

func TestFilenameIdentityAndFraming(t *testing.T) {
	id := identity.Instance{Project: "项目", InstanceID: "../x/y"}
	name := Filename(id)
	if !regexp.MustCompile(`^syslogger-[0-9a-f]{64}\.prom$`).MatchString(name) || Filename(id) != name {
		t.Fatal("文件名不稳定或不安全")
	}
	var framed []byte
	for _, value := range []string{id.Project, id.InstanceID} {
		framed = append(framed, 0, 0, 0, 0, 0, 0, 0, byte(len(value)))
		framed = append(framed, value...)
	}
	digest := sha256.Sum256(framed)
	if name != "syslogger-"+hex.EncodeToString(digest[:])+".prom" {
		t.Fatal("长度前缀哈希不符")
	}
	for _, other := range []identity.Instance{{Project: "other", InstanceID: id.InstanceID}, {Project: id.Project, InstanceID: "common"}} {
		if Filename(other) == name {
			t.Fatal("不同身份使用相同路径")
		}
	}
}
