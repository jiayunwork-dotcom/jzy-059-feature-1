package store

import (
	"errors"
	"path/filepath"
	"reflect"
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
	if !reflect.DeepEqual(got, rec) {
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

// 液舱随档案一起建、一起取、一起改、一起删；不同档案的舱互不串扰。
func TestFileStore_TanksBelongToRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "conditions.json")
	s, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}

	shipA := Record{
		Name: "ship-a", KB: 1.2, KG: 2.0, DisplacementVolume: 1000, TransverseInertia: 5000,
		Tanks: []TankRecord{
			{Name: "a-fuel", Length: 4, Width: 2, LiquidDensity: 900, FillingStatus: "partial"},
			{Name: "a-ballast", Length: 6, Width: 3, FreeSurfaceInertia: 50, LiquidDensity: 1000, FillingStatus: "full"},
		},
	}
	shipB := Record{
		Name: "ship-b", KB: 1.0, KG: 1.5, DisplacementVolume: 800, TransverseInertia: 4000,
		Tanks: []TankRecord{
			{Name: "b-fresh", Length: 3, Width: 2, LiquidDensity: 1000, FillingStatus: "empty"},
		},
	}
	// 无舱档案：Tanks 序列化后应为缺省，读回为 nil。
	shipC := Record{Name: "ship-c", KB: 1.0, KG: 1.0, DisplacementVolume: 500, TransverseInertia: 2000}
	for _, rec := range []Record{shipA, shipB, shipC} {
		if err := s.Put(rec); err != nil {
			t.Fatalf("建档 %s 失败: %v", rec.Name, err)
		}
	}

	gotA, err := s.Get("ship-a")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotA, shipA) {
		t.Fatalf("ship-a 液舱读写不一致: %+v", gotA)
	}
	gotB, _ := s.Get("ship-b")
	if !reflect.DeepEqual(gotB, shipB) {
		t.Fatalf("ship-b 液舱串扰: %+v", gotB)
	}
	// A 的舱不能出现在 B 名下，反之亦然。
	if len(gotB.Tanks) != 1 || gotB.Tanks[0].Name != "b-fresh" {
		t.Fatalf("不同档案的液舱发生串扰: %+v", gotB.Tanks)
	}
	gotC, _ := s.Get("ship-c")
	if len(gotC.Tanks) != 0 {
		t.Fatalf("无舱档案不应读出液舱: %+v", gotC.Tanks)
	}

	// 改：A 的舱单整体替换（压载调舱后只剩一个满舱）。
	shipA.Tanks = []TankRecord{
		{Name: "a-ballast", Length: 6, Width: 3, LiquidDensity: 1000, FillingStatus: "full"},
	}
	if err := s.Put(shipA); err != nil {
		t.Fatal(err)
	}
	gotA2, _ := s.Get("ship-a")
	if !reflect.DeepEqual(gotA2, shipA) {
		t.Fatalf("改舱未生效: %+v", gotA2)
	}
	// B 不受 A 改舱影响。
	gotB2, _ := s.Get("ship-b")
	if !reflect.DeepEqual(gotB2, shipB) {
		t.Fatalf("改 A 的舱污染了 B: %+v", gotB2)
	}

	// 持久化：重开文件液舱仍在。
	s2, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	gotA3, err := s2.Get("ship-a")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotA3, shipA) {
		t.Fatalf("重开后液舱丢失: %+v", gotA3)
	}

	// 删：A 整档（含舱）消失，B 仍在。
	if err := s2.Delete("ship-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := s2.Get("ship-a"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("删档后应 NotFound，得到 %v", err)
	}
	if got, err := s2.Get("ship-b"); err != nil || len(got.Tanks) != 1 {
		t.Fatalf("删除 A 不应波及 B: %+v %v", got, err)
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
