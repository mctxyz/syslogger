package catalog

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Write 原子替换一个实例的完整清单，并保证目录及文件可供其他用户维护。
func (s Store) Write(instance Instance) error {
	data, err := json.MarshalIndent(instance, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	path := s.Path(instance.Project, instance.InstanceID)
	if err := sharedDirectory(s.Root); err != nil {
		return err
	}
	if err := sharedDirectory(filepath.Dir(path)); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".catalog-*")
	if err != nil {
		return err
	}
	temporary := file.Name()
	defer os.Remove(temporary)
	defer file.Close()
	if _, err := file.Write(data); err != nil {
		return err
	}
	if err := file.Chmod(0666); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}

// sharedDirectory 在并发创建时复用既有目录，显式权限保证不受创建者 umask 限制。
func sharedDirectory(path string) error {
	if err := os.MkdirAll(path, 0777); err != nil {
		return err
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.Mode().Perm() != 0777 {
		return os.Chmod(path, 0777)
	}
	return nil
}

// Read 读取并排序项目和实例；首次发布之前的缺省目录对应空列表。
func (s Store) Read() ([]Instance, error) {
	instances := make([]Instance, 0)
	projects, err := os.ReadDir(s.Root)
	if errors.Is(err, os.ErrNotExist) {
		return instances, nil
	}
	if err != nil {
		return nil, err
	}
	for _, project := range projects {
		if !project.IsDir() {
			continue
		}
		directory := filepath.Join(s.Root, project.Name())
		files, err := os.ReadDir(directory)
		if err != nil {
			return nil, err
		}
		for _, file := range files {
			if file.IsDir() || !strings.HasSuffix(file.Name(), ".json") {
				continue
			}
			path := filepath.Join(directory, file.Name())
			data, err := os.ReadFile(path)
			if err != nil {
				return nil, err
			}
			var instance Instance
			if err := json.Unmarshal(data, &instance); err != nil {
				return nil, fmt.Errorf("读取日志目录 %s: %w", path, err)
			}
			if instance.Project == "" || instance.InstanceID == "" {
				return nil, fmt.Errorf("日志目录 %s 缺少项目或实例身份", path)
			}
			sort.Slice(instance.Components, func(i, j int) bool {
				return instance.Components[i].Name < instance.Components[j].Name
			})
			instances = append(instances, instance)
		}
	}
	sort.Slice(instances, func(i, j int) bool {
		if instances[i].Project != instances[j].Project {
			return instances[i].Project < instances[j].Project
		}
		return instances[i].InstanceID < instances[j].InstanceID
	})
	return instances, nil
}
