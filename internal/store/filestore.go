// Package store 提供装载状态档的持久化存储。
//
// 当前实现为容器本地 JSON 文件存储（FileStore）：所有档案保存在
// 单个 JSON 文件内，写入时先写临时文件再原子改名，避免半写文件。
// 全部方法带读写锁，可被多个 HTTP 请求并发调用。
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// ErrNotFound 表示按名字取不到对应档案。
var ErrNotFound = errors.New("装载状态档不存在")

// Record 是一条被持久化的装载状态档，纯数据、不含业务逻辑。
type Record struct {
	Name               string  `json:"name"`
	DisplacementVolume float64 `json:"displacementVolume"`
	KB                 float64 `json:"kb"`
	KG                 float64 `json:"kg"`
	TransverseInertia  float64 `json:"transverseInertia"`
	WaterDensity       float64 `json:"waterDensity"`
}

// Store 是档案存储的抽象，业务层只依赖该接口，便于替换实现与测试。
type Store interface {
	Get(name string) (Record, error)
	Put(rec Record) error
	Delete(name string) error
	List() ([]Record, error)
}

// FileStore 基于 JSON 文件的 Store 实现，并发安全。
type FileStore struct {
	mu       sync.RWMutex
	filePath string
}

// NewFileStore 打开（不存在则创建）path 处的档案文件。
func NewFileStore(path string) (*FileStore, error) {
	if path == "" {
		return nil, errors.New("档案文件路径不能为空")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("创建档案目录失败: %w", err)
	}
	s := &FileStore{filePath: path}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		if err := s.flushLocked(map[string]Record{}); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, fmt.Errorf("检查档案文件失败: %w", err)
	}
	return s, nil
}

// loadLocked 读出全部档案。调用方须持有锁（读或写）。
func (s *FileStore) loadLocked() (map[string]Record, error) {
	data, err := os.ReadFile(s.filePath)
	if err != nil {
		return nil, fmt.Errorf("读取档案文件失败: %w", err)
	}
	recs := map[string]Record{}
	if len(data) > 0 {
		if err := json.Unmarshal(data, &recs); err != nil {
			return nil, fmt.Errorf("解析档案文件失败（文件可能损坏）: %w", err)
		}
	}
	return recs, nil
}

// flushLocked 全量写回。先写同目录临时文件再 rename，保证原子可见。
// 调用方须持有写锁。
func (s *FileStore) flushLocked(recs map[string]Record) error {
	data, err := json.MarshalIndent(recs, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化档案失败: %w", err)
	}
	dir := filepath.Dir(s.filePath)
	tmp, err := os.CreateTemp(dir, ".conditions-*.tmp")
	if err != nil {
		return fmt.Errorf("创建临时档案文件失败: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("写入临时档案文件失败: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("关闭临时档案文件失败: %w", err)
	}
	if err := os.Rename(tmpName, s.filePath); err != nil {
		return fmt.Errorf("替换档案文件失败: %w", err)
	}
	return nil
}

// Get 按名取档；不存在返回 ErrNotFound。
func (s *FileStore) Get(name string) (Record, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	recs, err := s.loadLocked()
	if err != nil {
		return Record{}, err
	}
	rec, ok := recs[name]
	if !ok {
		return Record{}, fmt.Errorf("%w: %q", ErrNotFound, name)
	}
	return rec, nil
}

// Put 建档或覆盖同名档案（upsert）。
func (s *FileStore) Put(rec Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	recs, err := s.loadLocked()
	if err != nil {
		return err
	}
	recs[rec.Name] = rec
	return s.flushLocked(recs)
}

// Delete 删档；档不存在返回 ErrNotFound。
func (s *FileStore) Delete(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	recs, err := s.loadLocked()
	if err != nil {
		return err
	}
	if _, ok := recs[name]; !ok {
		return fmt.Errorf("%w: %q", ErrNotFound, name)
	}
	delete(recs, name)
	return s.flushLocked(recs)
}

// List 返回按名字排序的全部档案。
func (s *FileStore) List() ([]Record, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	recs, err := s.loadLocked()
	if err != nil {
		return nil, err
	}
	out := make([]Record, 0, len(recs))
	for _, rec := range recs {
		out = append(out, rec)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
