// Package textfile 负责健康事实的指标编码、稳定实例文件名及原子文件交付。
package textfile

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"

	"github.com/mctxyz/syslogger/internal/identity"
)

// Filename 对逐字段长度前缀编码计算完整 SHA-256，身份原文参与哈希而不进入路径。
func Filename(id identity.Instance) string {
	hash := sha256.New()
	var size [8]byte
	for _, value := range []string{id.Project, id.InstanceID} {
		binary.BigEndian.PutUint64(size[:], uint64(len(value)))
		_, _ = hash.Write(size[:])
		_, _ = hash.Write([]byte(value))
	}
	return "syslogger-" + hex.EncodeToString(hash.Sum(nil)) + ".prom"
}
