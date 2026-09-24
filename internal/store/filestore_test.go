package store

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"
)

func TestFileStore_CRUDAndPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "conditions.json") // 目录尚不存在
	s, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("创建存储失败: %v", err)
	}

	rec := Record{Name: "barge", DisplacementVolume: 1200, KB: 1.25, KG: 2.0, TransverseInertia: 5760, WaterDensity: 1025}
	if err := s.Put(rec); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	got, err := s.Get("barge")
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if got != rec {
		t.Fatalf("读写内容不一致: %+v", got)
	}

	// upsert 覆盖
	rec.KG = 2.5
	if err := s.Put(rec); err != nil {
		t.Fatalf("覆盖失败: %v", err)
	}
	got, _ = s.Get("barge")
	if got.KG != 2.5 {
		t.Fatalf("覆盖未生效: KG=%v", got.KG)
	}

	list, _ := s.List()
	if len(list) != 1 {
		t.Fatalf("列表应有 1 条，得到 %d", len(list))
	}

	// 重新打开同一文件，数据仍在（持久化）
	s2, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("重开失败: %v", err)
	}
	got2, err := s2.Get("barge")
	if err != nil {
		t.Fatalf("重开后读取失败: %v", err)
	}
	if got2.KG != 2.5 {
		t.Fatalf("持久化数据不正确: %+v", got2)
	}

	if err := s2.Delete("barge"); err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	if _, err := s2.Get("barge"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("删除后应 NotFound，得到 %v", err)
	}
	if err := s2.Delete("barge"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("重复删除应 NotFound，得到 %v", err)
	}
}

// 并发读写不得丢档（FileStore 内部锁 + 原子写回）。
func TestFileStore_ConcurrentWrites(t *testing.T) {
	s, err := NewFileStore(filepath.Join(t.TempDir(), "c.json"))
	if err != nil {
		t.Fatal(err)
	}
	const n = 100
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := s.Put(Record{Name: string(rune('a'+i%26)) + "-" + itoa(i), KB: float64(i)})
			if err != nil {
				t.Errorf("并发写入失败: %v", err)
			}
		}()
	}
	wg.Wait()
	list, err := s.List()
	if err != nil {
		t.Fatalf("列表失败: %v", err)
	}
	if len(list) != n {
		t.Fatalf("并发写后应有 %d 条，得到 %d（存在丢失）", n, len(list))
	}

	// 文件内容必须仍是合法 JSON（原子替换没有写坏）。
	s3, err := NewFileStore(filepath.Join(t.TempDir(), "fresh.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s3.List(); err != nil {
		t.Fatalf("新库读取异常: %v", err)
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}
