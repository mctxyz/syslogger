package hostinit

import (
	"fmt"
	"os/user"
	"strconv"

	"github.com/mctxyz/syslogger/internal/catalog"
)

// prepareAccount 解析已有 app 账户的数值身份，资源创建不改变系统账户配置。
func (init *initializer) prepareAccount() (int, int, error) {
	account, err := user.Lookup(catalog.User)
	if err != nil {
		return 0, 0, fmt.Errorf("需要已有的本机账户 %s: %w", catalog.User, err)
	}
	uid, err := strconv.Atoi(account.Uid)
	if err != nil {
		return 0, 0, fmt.Errorf("解析 %s 的 UID: %w", catalog.User, err)
	}
	gid, err := strconv.Atoi(account.Gid)
	if err != nil {
		return 0, 0, fmt.Errorf("解析 %s 的主 GID: %w", catalog.User, err)
	}
	init.logger.Printf("[检查运行账户] %q UID=%d GID=%d", catalog.User, uid, gid)
	return uid, gid, nil
}
