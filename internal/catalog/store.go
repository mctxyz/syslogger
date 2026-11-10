package catalog

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// write 在项目文件锁内原子替换实例清单，临时文件与目标位于同一目录。
func (s Store) write(instance Instance) error {
	data, err := json.MarshalIndent(instance, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	path := s.Path(instance.Project, instance.InstanceID)
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
	if err := s.checkGroup(file); err != nil {
		return err
	}
	if err := file.Chmod(FileMode); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(temporary, path)
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
		if strings.HasPrefix(project.Name(), projectTemporaryPrefix) || !project.IsDir() {
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
			instance, err := readInstance(path)
			if errors.Is(err, os.ErrNotExist) {
				// 清理任务可在目录枚举之后删除过期文件，读取方跳过已消失的记录。
				continue
			}
			if err != nil {
				return nil, err
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

func readInstance(path string) (Instance, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Instance{}, err
	}
	var instance Instance
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&instance); err != nil {
		return Instance{}, fmt.Errorf("读取日志目录 %s: %w", path, err)
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			err = fmt.Errorf("必须仅包含一个 JSON 对象")
		}
		return Instance{}, fmt.Errorf("读取日志目录 %s: %w", path, err)
	}
	if instance.Project == "" || instance.InstanceID == "" {
		return Instance{}, fmt.Errorf("日志目录 %s 缺少项目或实例身份", path)
	}
	return instance, nil
}
