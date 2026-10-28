package main

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/mctxyz/syslogger/internal/catalog"
)

// showCatalog 读取静态名称目录，按原文身份筛选，查询过程独立于 journal。
func showCatalog(store catalog.Store, opts options, output io.Writer) error {
	instances, err := store.Read()
	if err != nil {
		return err
	}
	selected := make([]catalog.Instance, 0, len(instances))
	for _, instance := range instances {
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
	for index, instance := range selected {
		if index == 0 || selected[index-1].Project != instance.Project {
			if _, err := fmt.Fprintln(output, catalogLabel(instance.Project, "", colourProject, opts.colour)); err != nil {
				return err
			}
		}
		lastInstance := index == len(selected)-1 || selected[index+1].Project != instance.Project
		branch, prefix := "├── ", "│   "
		if lastInstance {
			branch, prefix = "└── ", "    "
		}
		if _, err := fmt.Fprintln(output, branch+catalogLabel(instance.InstanceID, instance.InstanceDescription, colourInstance, opts.colour)); err != nil {
			return err
		}
		for componentIndex, component := range instance.Components {
			branch := "├── "
			if componentIndex == len(instance.Components)-1 {
				branch = "└── "
			}
			label := catalogLabel(component.Name, component.Description, colourComponent, opts.colour)
			if _, err := fmt.Fprintln(output, prefix+branch+label); err != nil {
				return err
			}
		}
	}
	return nil
}

// catalogLabel 复用日志输出的控制字符转义，描述与身份在树形目录中保持单行。
func catalogLabel(name, description, style string, colour bool) string {
	label := escaped(name)
	if colour {
		label = colourText(label, style)
	}
	if description != "" {
		label += "  " + escaped(description)
	}
	return label
}
