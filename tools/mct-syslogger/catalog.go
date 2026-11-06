package main

import (
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"strings"
	"time"

	humanize "github.com/dustin/go-humanize"
	"github.com/mctxyz/syslogger/internal/catalog"
	"github.com/mctxyz/syslogger/internal/hourly"
	"github.com/rivo/uniseg"
)

// 目录中的组件时间和小时计数使用默认前景色的弱化亮度。
const colourCatalogDetails = "\x1b[2m"

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
		for index := range instance.Components {
			instance.Components[index].LogCounts = instance.Components[index].LogCounts.Recent(now)
		}
		selected = append(selected, instance)
	}
	if opts.asJSON {
		encoder := json.NewEncoder(output)
		encoder.SetEscapeHTML(false)
		return encoder.Encode(selected)
	}
	// 同层组件在全部筛选结果中共享时间列；先转义再量宽，ANSI 样式在补齐之后单独附加。
	nameWidth := 0
	for _, instance := range selected {
		for _, component := range instance.Components {
			nameWidth = max(nameWidth, uniseg.StringWidth(escaped(component.Name)))
		}
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
			branch, descriptionPrefix := "├── ", "│   "
			if componentIndex == len(instance.Components)-1 {
				branch, descriptionPrefix = "└── ", "    "
			}
			name := escaped(component.Name)
			padding := strings.Repeat(" ", nameWidth-uniseg.StringWidth(name)+2)
			timestamp := component.LastActiveAt.Local().Format("2006-01-02 15:04:05")
			count := catalogHourCounts(component.LogCounts)
			if opts.colour {
				name = colourText(name, colourComponent)
				timestamp = colourText(timestamp, colourCatalogDetails)
				count = colourText(count, colourCatalogDetails)
			}
			line := projectPrefix + prefix + branch + name + padding + timestamp + "  " + count
			if _, err := fmt.Fprintln(output, line); err != nil {
				return err
			}
			if component.Description != "" {
				description := escaped(component.Description)
				indent := projectPrefix + prefix + descriptionPrefix + strings.Repeat(" ", nameWidth+2)
				if _, err := fmt.Fprintln(output, indent+description); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// catalogHourCounts 用时分展示三小时数量，仅在跨日或偏移变化时补充消歧信息。
func catalogHourCounts(window hourly.Window) string {
	layout := "15:04"
	first := window[0].StartTime.Local()
	last := window[len(window)-1].StartTime.Local()
	if first.Format("2006-01-02") != last.Format("2006-01-02") {
		layout = "01-02 15:04"
	}
	_, firstOffset := first.Zone()
	for _, bucket := range window {
		_, offset := bucket.StartTime.Local().Zone()
		if offset != firstOffset {
			// 时区偏移发生变化时，保留偏移以区分夏令时重复小时。
			layout += " -0700"
			break
		}
	}
	hours := make([]string, 0, len(window))
	for _, bucket := range window {
		start := bucket.StartTime.Local().Format(layout)
		count := humanize.BigComma(new(big.Int).SetUint64(bucket.Count))
		hours = append(hours, start+"="+count)
	}
	return "每小时: " + strings.Join(hours, " | ")
}

// catalogLabel 复用日志输出的控制字符转义，为项目和实例保留单行名称及补充信息。
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
