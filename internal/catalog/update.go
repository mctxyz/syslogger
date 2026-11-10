package catalog

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/mctxyz/syslogger/internal/hourly"
)

// Merge 在项目文件锁内将本次成功接收增量加入已有记录，写入成功后调用方消费该增量。
func (s Store) Merge(delta Instance, now time.Time) error {
	delta = delta.Active(now)
	if len(delta.Components) == 0 {
		return nil
	}
	if err := s.CheckRoot(); err != nil {
		return err
	}
	path := s.Path(delta.Project, delta.InstanceID)
	lock, err := s.lockProject(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer lock.Close()
	instance, err := readInstance(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err == nil && (instance.Project != delta.Project || instance.InstanceID != delta.InstanceID) {
		return fmt.Errorf("日志目录 %s 的身份与文件路径不符", path)
	}
	// 历史是否连续由实际接收时刻决定，跨过期点才刷盘的活动仍可延续原记录。
	mergeAt := now.Add(-SyncInterval)
	for _, component := range delta.Components {
		if component.CreatedAt.Before(mergeAt) {
			mergeAt = component.CreatedAt
		}
	}
	instance = instance.Active(mergeAt)
	if len(instance.Components) == 0 {
		instance.CreatedAt = time.Time{}
	}
	instance.Project = delta.Project
	instance.InstanceID = delta.InstanceID
	instance.InstanceDescription = delta.InstanceDescription
	components := make(map[string]Component, len(instance.Components)+len(delta.Components))
	for _, component := range instance.Components {
		component.LogCounts.Merge(hourly.Window{}, now)
		components[component.Name] = component
	}
	for _, incoming := range delta.Components {
		component, exists := components[incoming.Name]
		if !exists {
			component = incoming
			component.LogCounts.Merge(hourly.Window{}, now)
		} else {
			// 宽限内记录仍在存续，保留历史让同组件的跨进程批次可按任意顺序合并。
			component.LogCounts.Merge(incoming.LogCounts, now)
			if incoming.CreatedAt.Before(component.CreatedAt) {
				component.CreatedAt = incoming.CreatedAt
			}
			if !incoming.LastActiveAt.Before(component.LastActiveAt) {
				component.LastActiveAt = incoming.LastActiveAt
				component.Description = incoming.Description
			}
		}
		components[incoming.Name] = component
		if instance.CreatedAt.IsZero() || component.CreatedAt.Before(instance.CreatedAt) {
			instance.CreatedAt = component.CreatedAt
		}
	}
	instance.Components = make([]Component, 0, len(components))
	for _, component := range components {
		instance.Components = append(instance.Components, component)
	}
	// 其他组件可能有尚未刷盘的续期活动，整文件合并与清理共用同一物理过期边界。
	instance = instance.Active(now.Add(-SyncInterval))
	sort.Slice(instance.Components, func(i, j int) bool {
		return instance.Components[i].Name < instance.Components[j].Name
	})
	return s.write(instance)
}

// Prune 与增量合并使用同一项目锁，为一年未活跃记录保留一个同步窗口后再物理移除。
func (s Store) Prune(now time.Time) error {
	if err := s.CheckRoot(); err != nil {
		return err
	}
	projects, err := os.ReadDir(s.Root)
	if err != nil {
		return err
	}
	for _, project := range projects {
		if strings.HasPrefix(project.Name(), projectTemporaryPrefix) || !project.IsDir() {
			continue
		}
		if err := s.pruneProject(filepath.Join(s.Root, project.Name()), now); err != nil {
			return err
		}
	}
	return nil
}

func (s Store) pruneProject(directory string, now time.Time) error {
	lock, err := s.lockProject(directory)
	if err != nil {
		return err
	}
	defer lock.Close()
	files, err := os.ReadDir(directory)
	if err != nil {
		return err
	}
	for _, file := range files {
		if file.IsDir() || !strings.HasSuffix(file.Name(), ".json") {
			continue
		}
		path := filepath.Join(directory, file.Name())
		instance, err := readInstance(path)
		if err != nil {
			return err
		}
		if s.Path(instance.Project, instance.InstanceID) != path {
			return fmt.Errorf("日志目录 %s 的身份与文件路径不符", path)
		}
		active := instance.Active(now.Add(-SyncInterval))
		if len(active.Components) == 0 {
			if err := os.Remove(path); err != nil {
				return err
			}
		} else if len(active.Components) != len(instance.Components) {
			for index := range active.Components {
				active.Components[index].LogCounts.Merge(hourly.Window{}, now)
			}
			if err := s.write(active); err != nil {
				return err
			}
		}
	}
	return nil
}
