package hostinit

import (
	"errors"
	"fmt"
	"os/user"
	"strconv"

	"github.com/mctxyz/syslogger/internal/catalog"
)

// prepareGroup 准备共享写入组，组内业务账户及成员关系由部署方管理。
func (init *initializer) prepareGroup() (int, error) {
	group, err := user.LookupGroup(catalog.Group)
	var missing user.UnknownGroupError
	if errors.As(err, &missing) {
		if _, err := init.command("groupadd", "--system", "--", catalog.Group); err != nil {
			return 0, err
		}
		group, err = user.LookupGroup(catalog.Group)
	}
	if err != nil {
		return 0, fmt.Errorf("查询共享写入组 %s: %w", catalog.Group, err)
	}
	gid, err := strconv.Atoi(group.Gid)
	if err != nil {
		return 0, fmt.Errorf("解析 %s 的 GID: %w", catalog.Group, err)
	}
	init.logger.Printf("[检查写入组] %q GID=%d", catalog.Group, gid)
	return gid, nil
}
