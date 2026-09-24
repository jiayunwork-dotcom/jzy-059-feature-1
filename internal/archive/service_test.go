package archive

import (
	"errors"
	"fmt"
	"reflect"
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
	if !reflect.DeepEqual(got, c) {
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

func conditionWithTanks(name string, tanks ...stability.Tank) Condition {
	c := validCondition(name, 2.0)
	c.Tanks = tanks
	return c
}

func partialTank(name string, width, density float64) stability.Tank {
	return stability.Tank{
		Name:          name,
		Length:        4,
		Width:         width,
		LiquidDensity: density,
		FillingStatus: stability.TankStatusPartial,
	}
}

// 液舱随档案存取，并可直接还原出修正后的核算。
func TestService_TanksSavedAndResolved(t *testing.T) {
	svc := newTestService(t)
	tanks := []stability.Tank{
		partialTank("fuel", 2, 900),
		{Name: "ballast", Length: 6, Width: 3, LiquidDensity: 1000, FillingStatus: stability.TankStatusFull},
	}
	c := conditionWithTanks("ship-tanks", tanks...)
	if err := svc.Save(c); err != nil {
		t.Fatalf("建档失败: %v", err)
	}
	got, err := svc.Get("ship-tanks")
	if err != nil {
		t.Fatalf("取档失败: %v", err)
	}
	if !reflect.DeepEqual(got.Tanks, tanks) {
		t.Fatalf("液舱清单存取不一致: %+v vs %+v", got.Tanks, tanks)
	}

	p, resolved, err := svc.ResolveLoading("ship-tanks")
	if err != nil {
		t.Fatalf("还原装态失败: %v", err)
	}
	if len(resolved) != 2 {
		t.Fatalf("应还原 2 个舱，得到 %d", len(resolved))
	}
	r, err := stability.EvaluateLoading(p, resolved, 0)
	if err != nil {
		t.Fatalf("修正后核算失败: %v", err)
	}
	// 满舱扣减为零，只有燃油舱扣减；固体 GM = 4.2。
	wantCorr := 900.0 * (4.0 * 8.0 / 12.0) / (1025.0 * 1000.0)
	if mathDiff(r.FreeSurfaceCorrection, wantCorr) > 1e-12 {
		t.Fatalf("总扣减 %v 与手算 %v 不符", r.FreeSurfaceCorrection, wantCorr)
	}
	if mathDiff(r.SolidGM, 4.2) > 1e-12 {
		t.Fatalf("固体 GM 应为 4.2，得到 %v", r.SolidGM)
	}
	if mathDiff(r.EffectiveGM, 4.2-wantCorr) > 1e-12 {
		t.Fatalf("有效 GM 异常: %v", r.EffectiveGM)
	}
}

// 液舱参数校验在落库之前：尺寸/惯性矩非正、状态非法、密度非正全部拦下，
// 且原因里点得清是哪个舱、哪类问题。
func TestService_RejectsBadTanks(t *testing.T) {
	svc := newTestService(t)
	bad := []struct {
		name string
		tank stability.Tank
		want string
	}{
		{"状态非法", func() stability.Tank {
			x := partialTank("t", 2, 900)
			x.FillingStatus = "weird"
			return x
		}(), "注液状态"},
		{"密度为零", partialTank("t", 2, 0), "密度"},
		{"密度为负", partialTank("t", 2, -5), "密度"},
		{"宽为零", partialTank("t", 0, 900), "自由液面"},
		{"长为负", func() stability.Tank {
			x := partialTank("t", 2, 900)
			x.Length = -1
			return x
		}(), "自由液面"},
		{"直给惯性矩为负", stability.Tank{Name: "t", FreeSurfaceInertia: -1, LiquidDensity: 900, FillingStatus: stability.TankStatusPartial}, "惯性矩"},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			err := svc.Save(conditionWithTanks("bad-ship", tc.tank))
			if err == nil {
				t.Fatalf("非法液舱（%s）应在建档时拦截", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("%s：错误原因应提及 %q，得到 %v", tc.name, tc.want, err)
			}
			if _, ok := err.(*stability.ValidationError); !ok {
				t.Fatalf("应是 *ValidationError，得到 %T", err)
			}
			// 被拦下的档案不得落库。
			if _, err := svc.Get("bad-ship"); !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("非法档案不应落库，得到 %v", err)
			}
		})
	}

	// 第二个舱非法：原因必须定位到 #2。
	err := svc.Save(conditionWithTanks("bad-ship",
		partialTank("ok", 2, 900),
		partialTank("bad", 2, 0)))
	if err == nil || !strings.Contains(err.Error(), "#2") {
		t.Fatalf("应定位到第 2 个舱，得到 %v", err)
	}
}

// 液舱随档案整体覆盖与删除；不同档案的舱互不串扰。
func TestService_TanksIsolationAcrossConditions(t *testing.T) {
	svc := newTestService(t)
	if err := svc.Save(conditionWithTanks("alpha",
		partialTank("a1", 2, 900))); err != nil {
		t.Fatal(err)
	}
	if err := svc.Save(conditionWithTanks("beta",
		partialTank("b1", 3, 1000),
		partialTank("b2", 4, 1025))); err != nil {
		t.Fatal(err)
	}

	a, _ := svc.Get("alpha")
	b, _ := svc.Get("beta")
	if len(a.Tanks) != 1 || a.Tanks[0].Name != "a1" {
		t.Fatalf("alpha 的舱异常: %+v", a.Tanks)
	}
	if len(b.Tanks) != 2 || b.Tanks[0].Name != "b1" || b.Tanks[1].Name != "b2" {
		t.Fatalf("beta 的舱异常: %+v", b.Tanks)
	}

	// 覆盖 alpha 的舱单为空（该档退回纯固体）；beta 不受影响。
	if err := svc.Save(validCondition("alpha", 2.0)); err != nil {
		t.Fatal(err)
	}
	a2, _ := svc.Get("alpha")
	if len(a2.Tanks) != 0 {
		t.Fatalf("覆盖后 alpha 不应再有舱: %+v", a2.Tanks)
	}
	b2, _ := svc.Get("beta")
	if len(b2.Tanks) != 2 {
		t.Fatalf("覆盖 alpha 不应动 beta 的舱: %+v", b2.Tanks)
	}

	// 删 beta：连舱一起消失；alpha 仍在。
	if err := svc.Delete("beta"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Get("beta"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("beta 删除后应 NotFound，得到 %v", err)
	}
	if _, err := svc.Get("alpha"); err != nil {
		t.Fatalf("alpha 不应受影响: %v", err)
	}
}

func mathDiff(a, b float64) float64 {
	d := a - b
	if d < 0 {
		return -d
	}
	return d
}
