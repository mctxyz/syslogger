package main

import (
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/mctxyz/syslogger/internal/catalog"
)

// showCatalog 读取名称目录并过滤一年未活跃组件，身份筛选与展示独立于 journal。
func showCatalog(store catalog.Store, opts options, output io.Writer) error {
	instances, err := store.Read()
	if err != nil {
		return err
	}
	selected := make([]catalog.Instance, 0, len(instances))
	now := time.Now().UTC()
	for _, instance := range instances {
		instance = instance.Active(now)
		if len(instance.Components) == 0 {
			continue
		}
		if len(opts.projects) > 0 && !contains(opts.projects, instance.Project) {
			continue
		}
		if len(opts.instances) > 0 && !contains(opts.instances, instance.InstanceID) {
			continue
		}
		if len(opts.components) > 0 {
			components := make([]catalog.Component, 0)
			for _, component := range instance.Components {
				if contains(opts.components, component.Name) {
					components = append(components, component)
				}
			}
			if len(components) == 0 {
				continue
			}
			instance.Components = components
		}
		selected = append(selected, instance)
	}
	if opts.asJSON {
		encoder := json.NewEncoder(output)
		encoder.SetEscapeHTML(false)
		return encoder.Encode(selected)
	}
	if len(selected) > 0 {
		if _, err := fmt.Fprintln(output, "mct-syslogger"); err != nil {
			return err
		}
	}
	// 读取结果按项目排序，筛选保持原顺序，最后一条所属项目就是最后一个分组。
	for index, instance := range selected {
		projectBranch, projectPrefix := "├── ", "│   "
		if instance.Project == selected[len(selected)-1].Project {
			projectBranch, projectPrefix = "└── ", "    "
		}
		if index == 0 || selected[index-1].Project != instance.Project {
			label := catalogLabel(instance.Project, "", colourProject, opts.colour)
			if _, err := fmt.Fprintln(output, projectBranch+label); err != nil {
				return err
			}
		}
		lastInstance := index == len(selected)-1 || selected[index+1].Project != instance.Project
		branch, prefix := "├── ", "│   "
		if lastInstance {
			branch, prefix = "└── ", "    "
		}
		label := catalogLabel(instance.InstanceID, instance.InstanceDescription, colourInstance, opts.colour)
		if _, err := fmt.Fprintln(output, projectPrefix+branch+label); err != nil {
			return err
		}
		for componentIndex, component := range instance.Components {
			branch := "├── "
			if componentIndex == len(instance.Components)-1 {
				branch = "└── "
			}
			// 数量直接使用 uint64 十进制，最近入队时间沿用日志可读输出的系统时区与秒级格式。
			details := fmt.Sprintf("%d  %s", component.LogCount, component.LastActiveAt.Local().Format("2006-01-02 15:04:05"))
			if component.Description != "" {
				details += "  " + component.Description
			}
			label := catalogLabel(component.Name, details, colourComponent, opts.colour)
			if _, err := fmt.Fprintln(output, projectPrefix+prefix+branch+label); err != nil {
				return err
			}
		}
	}
	return nil
}

// catalogLabel 复用日志输出的控制字符转义，身份与补充信息在树形目录中保持单行。
func catalogLabel(name, details, style string, colour bool) string {
	label := escaped(name)
	if colour {
		label = colourText(label, style)
	}
	if details != "" {
		label += "  " + escaped(details)
	}
	return label
}
