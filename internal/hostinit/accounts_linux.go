package hostinit

import (
	"errors"
	"fmt"
	"os/user"
	"strconv"
	"strings"

	"github.com/mctxyz/syslogger/internal/catalog"
)

func (init *initializer) prepareGroup(account *user.User) (int, error) {
	localAccount, err := init.command("getent", "-s", "files", "passwd", account.Username)
	if err != nil {
		return 0, fmt.Errorf("运行账户必须是 usermod 可管理的本机账户: %w", err)
	}
	fields := strings.Split(strings.TrimSpace(localAccount), ":")
	if len(fields) != 7 || fields[0] != account.Username || fields[2] != account.Uid {
		return 0, fmt.Errorf("运行账户 %s 的本机 UID 与实际身份不一致", account.Username)
	}
	group, err := user.LookupGroup(catalog.Group)
	var missing user.UnknownGroupError
	if errors.As(err, &missing) {
		if _, err := init.command("groupadd", "--system", "--", catalog.Group); err != nil {
			return 0, err
		}
		init.logger.Printf("[创建组] %q", catalog.Group)
		group, err = user.LookupGroup(catalog.Group)
	}
	if err != nil {
		return 0, err
	}
	localGroup, err := init.command("getent", "-s", "files", "group", catalog.Group)
	if err != nil {
		return 0, fmt.Errorf("写入组必须由本机 files 管理: %w", err)
	}
	fields = strings.Split(strings.TrimSpace(localGroup), ":")
	if len(fields) != 4 || fields[0] != catalog.Group || fields[2] != group.Gid {
		return 0, fmt.Errorf("写入组 %s 的本机 GID 与实际身份不一致", catalog.Group)
	}
	gid, err := strconv.Atoi(group.Gid)
	if err != nil {
		return 0, err
	}
	check := func() error {
		members, err := init.command("getent", "group")
		if err != nil {
			return err
		}
		accounts, err := init.command("getent", "passwd")
		if err != nil {
			return err
		}
		return checkGroupMembers(accounts, members, account, group.Gid)
	}
	if err := check(); err != nil {
		return 0, err
	}
	if _, err := init.command("usermod", "--append", "--groups", catalog.Group, "--", account.Username); err != nil {
		return 0, err
	}
	init.logger.Printf("[设置账户组] 账户=%q 附加组=%q", account.Username, catalog.Group)
	if err := check(); err != nil {
		return 0, err
	}
	identities, err := init.command("getent", "initgroups", account.Username)
	if err != nil {
		return 0, err
	}
	fields = strings.Fields(identities)
	if len(fields) < 2 || fields[0] != account.Username {
		return 0, fmt.Errorf("无法确认账户 %s 的有效组身份", account.Username)
	}
	for _, identity := range fields[1:] {
		if identity == group.Gid {
			init.logger.Printf("[检查账户组] 账户=%q 已具备 GID=%d", account.Username, gid)
			return gid, nil
		}
	}
	return 0, fmt.Errorf("账户 %s 尚未获得写入组 %s 的有效身份，请检查 NSS 配置后重试", account.Username, catalog.Group)
}

// checkGroupMembers 同时检查主组和附加组，防止已有组扩大目录写入范围。
func checkGroupMembers(passwd, group string, target *user.User, gid string) error {
	allowed := map[string]bool{target.Username: true}
	for _, line := range strings.Split(strings.TrimSpace(passwd), "\n") {
		fields := strings.Split(line, ":")
		if len(fields) != 7 {
			return fmt.Errorf("无法解析 getent passwd 返回的账户记录")
		}
		if fields[2] == "0" || fields[2] == target.Uid {
			allowed[fields[0]] = true
		} else if fields[3] == gid {
			return fmt.Errorf("账户 %s 通过主组拥有 %s 写权限，请管理员先调整成员", fields[0], catalog.Group)
		}
	}
	found := false
	for _, line := range strings.Split(strings.TrimSpace(group), "\n") {
		fields := strings.Split(line, ":")
		if len(fields) != 4 {
			return fmt.Errorf("无法解析 getent group 返回的组记录")
		}
		if fields[0] == catalog.Group && fields[2] != gid {
			return fmt.Errorf("无法确认 %s 的数值 GID", catalog.Group)
		}
		if fields[2] != gid {
			continue
		}
		if fields[0] == catalog.Group {
			found = true
		}
		for _, name := range strings.Split(fields[3], ",") {
			if name != "" && !allowed[name] {
				return fmt.Errorf("账户 %s 通过附加组 %s 拥有 GID %s 的写权限，请管理员先调整成员", name, fields[0], gid)
			}
		}
	}
	if !found {
		return fmt.Errorf("无法确认 %s 的组成员", catalog.Group)
	}
	return nil
}
