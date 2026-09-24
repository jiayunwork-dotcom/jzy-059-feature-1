package archive

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"shipstability/internal/stability"
	"shipstability/internal/store"
)

func newTestService(t *testing.T) *Service {
	t.Helper()
	s, err := store.NewFileStore(t.TempDir() + "/conditions.json")
	if err != nil {
		t.Fatalf("创建测试存储失败: %v", err)
	}
	return NewService(s)
}

func validCondition(name string, kg float64) Condition {
	return Condition{
		Name:               name,
		DisplacementVolume: 1000,
		KB:                 1.2,
		KG:                 kg,
		TransverseInertia:  5000,
		WaterDensity:       1025,
	}
}

func TestService_SaveGetResolve(t *testing.T) {
	svc := newTestService(t)
	c := validCondition("ship-a", 2.0)
	if err := svc.Save(c); err != nil {
		t.Fatalf("建档失败: %v", err)
	}
	got, err := svc.Get("ship-a")
	if err != nil {
		t.Fatalf("取档失败: %v", err)
	}
	if got != c {
		t.Fatalf("取档内容不一致: %+v vs %+v", got, c)
	}
	p, err := svc.ResolveParams("ship-a")
	if err != nil {
		t.Fatalf("解析参数失败: %v", err)
	}
	if p.TransverseInertia != 5000 || p.DisplacementVolume != 1000 {
		t.Fatalf("解析出的参数异常: %+v", p)
	}

	if _, err := svc.Get("missing"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("取不存在的档应返回 ErrNotFound，得到 %v", err)
	}
}

func TestService_RejectsBadNameAndBadParams(t *testing.T) {
	svc := newTestService(t)
	badNames := []string{"", "a/b", "带中文", strings.Repeat("a", 65), "a b", "../x"}
	for _, n := range badNames {
		c := validCondition(n, 2.0)
		if err := svc.Save(c); err == nil {
			t.Fatalf("非法档名 %q 应被拦截", n)
		}
	}
	c := validCondition("ok-name_1", 2.0)
	c.DisplacementVolume = -1
	if err := svc.Save(c); err == nil {
		t.Fatal("非法浮态参数应被拦截")
	}
}

func TestService_DefaultDensityApplied(t *testing.T) {
	svc := newTestService(t)
	c := validCondition("no-density", 2.0)
	c.WaterDensity = 0
	if err := svc.Save(c); err != nil {
		t.Fatalf("建档失败: %v", err)
	}
	p, err := svc.ResolveParams("no-density")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if p.WaterDensity != stability.DefaultWaterDensity {
		t.Fatalf("应补默认海水密度，得到 %v", p.WaterDensity)
	}
}

func TestSeedDefaults_RectangularBarge(t *testing.T) {
	svc := newTestService(t)
	if err := svc.SeedDefaults(); err != nil {
		t.Fatalf("预置失败: %v", err)
	}
	if err := svc.SeedDefaults(); err != nil { // 幂等
		t.Fatalf("重复预置失败: %v", err)
	}
	c, err := svc.Get(RectangularBargeName)
	if err != nil {
		t.Fatalf("取预置档失败: %v", err)
	}
	p, err := c.ToParams()
	if err != nil {
		t.Fatalf("预置参数非法: %v", err)
	}
	r, err := stability.Evaluate(p, 0)
	if err != nil {
		t.Fatalf("预置档核算失败: %v", err)
	}
	// 手算：BM=4.8，GM=4.05，正稳性。
	if mathDiff(r.BM, 4.8) > 1e-9 || mathDiff(r.GM, 4.05) > 1e-9 {
		t.Fatalf("预置驳船数值与手算不符: BM=%v GM=%v", r.BM, r.GM)
	}
	if r.Stability != stability.StabilityPositive {
		t.Fatalf("预置驳船应正稳，得到 %q", r.Stability)
	}
}

func TestService_ConcurrentIsolation(t *testing.T) {
	svc := newTestService(t)
	const n = 50

	// 1) 不同名字的装载状态并行建档/取档/重算：数据不得串名。
	var wg sync.WaitGroup
	errCh := make(chan error, 4*n)
	for i := 0; i < n; i++ {
		i := i
		name := fmt.Sprintf("ship-%03d", i)
		wg.Add(1)
		go func() {
			defer wg.Done()
			kg := 1.0 + float64(i)*0.01 // 每条船 KG 各不相同
			if err := svc.Save(validCondition(name, kg)); err != nil {
				errCh <- fmt.Errorf("%s 建档: %w", name, err)
				return
			}
			got, err := svc.Get(name)
			if err != nil {
				errCh <- fmt.Errorf("%s 取档: %w", name, err)
				return
			}
			if diff := mathDiff(got.KG, kg); diff > 1e-9 {
				errCh <- fmt.Errorf("%s 数据串名: KG=%v 期望 %v", name, got.KG, kg)
				return
			}
			p, err := svc.ResolveParams(name)
			if err != nil {
				errCh <- fmt.Errorf("%s 解析: %w", name, err)
				return
			}
			r, err := stability.Evaluate(p, 5.0)
			if err != nil {
				errCh <- fmt.Errorf("%s 核算: %w", name, err)
				return
			}
			wantGM := 1.2 + 5.0 - kg
			if diff := mathDiff(r.GM, wantGM); diff > 1e-9 {
				errCh <- fmt.Errorf("%s GM 串算: %v 期望 %v", name, r.GM, wantGM)
			}
		}()
	}

	// 2) 同名档并行重算：每次计算都应基于同一份已存数据，彼此结果一致，
	//    绝不能把别的船的参数混进该名字的结果里。
	if err := svc.Save(validCondition("hot", 3.3)); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p, err := svc.ResolveParams("hot")
			if err != nil {
				errCh <- err
				return
			}
			r, err := stability.Evaluate(p, 7.0)
			if err != nil {
				errCh <- err
				return
			}
			wantGM := 1.2 + 5.0 - 3.3
			if mathDiff(r.GM, wantGM) > 1e-9 {
				errCh <- fmt.Errorf("同名并行计算被污染: GM=%v 期望 %v", r.GM, wantGM)
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Error(err)
	}

	// 全部档案必须齐全且名字互不覆盖。
	list, err := svc.List()
	if err != nil {
		t.Fatalf("列表失败: %v", err)
	}
	if len(list) != n+1 {
		t.Fatalf("应有 %d 条档案，得到 %d（可能发生了同名覆盖/丢失）", n+1, len(list))
	}
}

func mathDiff(a, b float64) float64 {
	d := a - b
	if d < 0 {
		return -d
	}
	return d
}
