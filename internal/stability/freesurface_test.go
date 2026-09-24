package stability

import (
	"math"
	"strings"
	"testing"
)

// 自由液面修正测试。基准船沿用 baseParams()：
//
//	∇ = 1000 m^3，BM = 5 m，固体 GM = 4.2 m，
//	Δ = ρ_w·∇ = 1025·1000 = 1,025,000 kg。
func baseDisplacementMass() float64 {
	return DefaultWaterDensity * 1000
}

func rectangularTank(name string, length, width, rho float64, status string) Tank {
	return Tank{Name: name, Length: length, Width: width, LiquidDensity: rho, Status: status}
}

// 单舱基本数值：i = l·b³/12，δ = ρ_i·i/Δ；固体 GM 不被挂舱改变。
func TestFreeSurface_SingleTankCorrection(t *testing.T) {
	const l, b, rho = 10.0, 4.0, 850.0
	wantInertia := l * b * b * b / 12.0 // 53.333...
	wantDelta := rho * wantInertia / baseDisplacementMass()

	tank := rectangularTank("燃油舱", l, b, rho, TankStatusPartial)
	if err := tank.Validate(0); err != nil {
		t.Fatalf("合法部分注液舱不应被拦截: %v", err)
	}
	r, err := EvaluateLoaded(baseParams(), []Tank{tank}, 5.0)
	if err != nil {
		t.Fatalf("带舱核算失败: %v", err)
	}

	approxEq(t, r.BM, 5.0, "BM 与挂舱无关")
	// 不变量：按固体算的 GM 不因挂舱改变。
	approxEq(t, r.SolidGM, 4.2, "solidGM")
	// 总扣减与单舱扣减相等。
	approxEq(t, r.FreeSurfaceCorrection, wantDelta, "总扣减 Σδ")
	if len(r.TankCorrections) != 1 {
		t.Fatalf("应有 1 条舱明细，得到 %d", len(r.TankCorrections))
	}
	d := r.TankCorrections[0]
	approxEq(t, d.FreeSurfaceInertia, wantInertia, "舱惯性矩")
	approxEq(t, d.Correction, wantDelta, "舱扣减")
	// 不变量：修正后有效 GM 严格低于固体 GM。
	approxEq(t, r.EffectiveGM, 4.2-wantDelta, "effectiveGM")
	if !(r.EffectiveGM < r.SolidGM) {
		t.Fatalf("有效 GM 必须严格小于固体 GM: %v < %v 不成立", r.EffectiveGM, r.SolidGM)
	}
	// 判定与 GZ 都站在有效 GM 上。
	approxEq(t, r.GM, r.EffectiveGM, "对外 GM 即有效 GM")
	wantGZ := r.EffectiveGM * math.Sin(DegreesToRadians(5.0))
	approxEq(t, r.GZ, wantGZ, "GZ 必须用有效 GM")
	if r.Stability != StabilityPositive {
		t.Fatalf("扣减后仍为正，应判 positive，得到 %q", r.Stability)
	}
}

// 不变量：同一几何的舱，部分注液产生扣减；改成空舱或满舱，扣减立刻归零。
func TestFreeSurface_EmptyAndFullGiveZeroCorrection(t *testing.T) {
	tank := rectangularTank("压载舱", 10, 4, 1025, TankStatusPartial)
	part, err := EvaluateLoaded(baseParams(), []Tank{tank}, 3.0)
	if err != nil {
		t.Fatal(err)
	}
	if part.FreeSurfaceCorrection <= 0 {
		t.Fatal("部分注液舱扣减应为正")
	}

	for _, status := range []string{TankStatusEmpty, TankStatusFull} {
		tk := tank
		tk.Status = status
		r, err := EvaluateLoaded(baseParams(), []Tank{tk}, 3.0)
		if err != nil {
			t.Fatalf("%s 舱核算失败: %v", status, err)
		}
		if r.FreeSurfaceCorrection != 0 || r.EffectiveGM != r.SolidGM {
			t.Fatalf("%s 舱扣减应恒为零: δ=%v eff=%v solid=%v",
				status, r.FreeSurfaceCorrection, r.EffectiveGM, r.SolidGM)
		}
		if len(r.TankCorrections) != 1 || r.TankCorrections[0].Correction != 0 {
			t.Fatalf("%s 舱明细扣减应为零: %+v", status, r.TankCorrections)
		}
		// GZ 也要回到固体 GM 那条线。
		approxEq(t, r.GZ, 4.2*math.Sin(r.AngleRad), status+" 舱 GZ")
	}
}

// 不变量：扣减只取决于液面几何与液体密度——结构上不存在液位/装载量字段。
// 这里用直接给惯性矩与给长宽两种等价给法，以及"同几何、同密度的两个舱
// 扣减必然相等"把它钉死；无论三分之一满还是半满，只要液面形状相同。
func TestFreeSurface_DependsOnlyOnSurfaceGeometryAndDensity(t *testing.T) {
	byDims := rectangularTank("淡水舱", 6, 3, 1000, TankStatusPartial)
	byInertia := Tank{
		Name:               "淡水舱-直给",
		FreeSurfaceInertia: RectangularSurfaceInertia(6, 3),
		LiquidDensity:      1000,
		Status:             TankStatusPartial,
	}
	r1, err := EvaluateLoaded(baseParams(), []Tank{byDims}, 0)
	if err != nil {
		t.Fatal(err)
	}
	r2, err := EvaluateLoaded(baseParams(), []Tank{byInertia}, 0)
	if err != nil {
		t.Fatal(err)
	}
	approxEq(t, r2.FreeSurfaceCorrection, r1.FreeSurfaceCorrection, "直给惯性矩与给长宽等价")

	// 同几何同密度的"半满舱"和"三分之一满舱"在建模上没有区别——
	// 扣减必须一模一样（不存在任何能让它们不同的输入字段）。
	half := rectangularTank("半满", 6, 3, 1000, TankStatusPartial)
	third := rectangularTank("三分之一满", 6, 3, 1000, TankStatusPartial)
	rh, _ := EvaluateLoaded(baseParams(), []Tank{half}, 0)
	rt, _ := EvaluateLoaded(baseParams(), []Tank{third}, 0)
	approxEq(t, rt.FreeSurfaceCorrection, rh.FreeSurfaceCorrection, "扣减与装载量无关")
}

// 不变量：几何完全相同的两舱，密度更大者扣减更大；
// 总扣减是各舱扣减的简单相加，没有耦合。
func TestFreeSurface_DensityOrderingAndAdditivity(t *testing.T) {
	light := rectangularTank("滑油", 8, 3, 900, TankStatusPartial)
	heavy := rectangularTank("压载水", 8, 3, 1025, TankStatusPartial)
	rl, err := EvaluateLoaded(baseParams(), []Tank{light}, 0)
	if err != nil {
		t.Fatal(err)
	}
	rh, err := EvaluateLoaded(baseParams(), []Tank{heavy}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !(rh.FreeSurfaceCorrection > rl.FreeSurfaceCorrection) {
		t.Fatalf("密度更大的舱扣减应更大: %v vs %v", rh.FreeSurfaceCorrection, rl.FreeSurfaceCorrection)
	}
	// 比例即密度比（几何、排水质量相同）。
	approxEq(t, rh.FreeSurfaceCorrection, rl.FreeSurfaceCorrection*(1025.0/900.0), "扣减与密度成正比")

	both, err := EvaluateLoaded(baseParams(), []Tank{light, heavy}, 0)
	if err != nil {
		t.Fatal(err)
	}
	wantTotal := rl.FreeSurfaceCorrection + rh.FreeSurfaceCorrection
	approxEq(t, both.FreeSurfaceCorrection, wantTotal, "总扣减为各舱简单相加")
	approxEq(t, both.TankCorrections[0].Correction, rl.FreeSurfaceCorrection, "舱 1 明细")
	approxEq(t, both.TankCorrections[1].Correction, rh.FreeSurfaceCorrection, "舱 2 明细")
	approxEq(t, both.EffectiveGM, 4.2-wantTotal, "有效 GM = 固体 GM − 总和")
	// 空舱/满舱混在清单里不改变总和。
	mixed := []Tank{
		light,
		rectangularTank("空舱占位", 8, 3, 1025, TankStatusEmpty),
		heavy,
		rectangularTank("满舱占位", 8, 3, 1025, TankStatusFull),
	}
	rm, err := EvaluateLoaded(baseParams(), mixed, 0)
	if err != nil {
		t.Fatal(err)
	}
	approxEq(t, rm.FreeSurfaceCorrection, wantTotal, "空/满舱不参与累加")
}

// 关键场景：固体 GM 为正的船，挂上足够宽的部分注液舱后有效 GM 转负，
// 判定必须翻成 negative，且正横倾下 GZ 变成加剧倾斜的负值。
func TestFreeSurface_PositiveShipFlipsNegative(t *testing.T) {
	// 固体 GM = 1.2 + 5.0 − 6.0 = 0.2 m（正稳性，但余量很小）。
	p := baseParams()
	p.KG = 6.0
	solid, err := Evaluate(p, 5.0)
	if err != nil {
		t.Fatal(err)
	}
	if solid.Stability != StabilityPositive {
		t.Fatalf("挂舱前应为正稳性，得到 %q", solid.Stability)
	}

	// 宽 10 m、长 20 m 的部分注液舱：i = 20·10³/12 ≈ 1666.7 m^4，
	// δ = 1000·1666.7/1,025,000 ≈ 1.626 m，远超 0.2 m 的正余量。
	tank := rectangularTank("宽压载舱", 20, 10, 1000, TankStatusPartial)
	loaded, err := EvaluateLoaded(p, []Tank{tank}, 5.0)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.EffectiveGM >= 0 {
		t.Fatalf("有效 GM 应被扣成负值，得到 %v", loaded.EffectiveGM)
	}
	if loaded.Stability != StabilityNegative {
		t.Fatalf("最终判定必须落在修正后的值上：期望 negative，得到 %q", loaded.Stability)
	}
	if loaded.GZ >= 0 || loaded.RightingMoment >= 0 {
		t.Fatalf("负有效 GM 下正横倾的 GZ/力矩应为负，得到 GZ=%v M=%v", loaded.GZ, loaded.RightingMoment)
	}
	// 固体值仍然为正——扣减没有被悄悄吞进固体计算。
	approxEq(t, loaded.SolidGM, 0.2, "固体 GM")
	if loaded.FreeSurfaceCorrection < loaded.SolidGM {
		t.Fatal("本例中总扣减必须大于固体 GM")
	}
}

// 不变量：横倾 0° 时 GZ 与复原力矩恒为零，挂不挂舱、正不稳性皆然。
func TestFreeSurface_ZeroAngleStillZeroGZ(t *testing.T) {
	tanks := []Tank{rectangularTank("w1", 20, 10, 1000, TankStatusPartial)}
	cases := []struct {
		name string
		kg   float64
	}{
		{"正稳性", 2.0},
		{"临界小余量", 6.0},
		{"固体本就负稳性", 100},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := baseParams()
			p.KG = tc.kg
			r, err := EvaluateLoaded(p, tanks, 0)
			if err != nil {
				t.Fatal(err)
			}
			if r.GZ != 0 || r.RightingMoment != 0 {
				t.Fatalf("0° 时 GZ/力矩必须恒为零: GZ=%v M=%v", r.GZ, r.RightingMoment)
			}
		})
	}
}

// 不带任何液舱时，新路径与原 Evaluate 逐值一致，修正字段为恒等零值。
func TestFreeSurface_NoTanksIdenticalToSolidPath(t *testing.T) {
	solid, err := Evaluate(baseParams(), 8.0)
	if err != nil {
		t.Fatal(err)
	}
	for _, tanks := range [][]Tank{nil, {}} {
		r, err := EvaluateLoaded(baseParams(), tanks, 8.0)
		if err != nil {
			t.Fatal(err)
		}
		approxEq(t, r.BM, solid.BM, "BM")
		approxEq(t, r.GM, solid.GM, "GM")
		approxEq(t, r.SolidGM, solid.GM, "solidGM")
		approxEq(t, r.EffectiveGM, solid.GM, "effectiveGM")
		if r.FreeSurfaceCorrection != 0 || r.TankCorrections != nil {
			t.Fatalf("无舱时扣减应为 0、明细应为空: δ=%v details=%v",
				r.FreeSurfaceCorrection, r.TankCorrections)
		}
		approxEq(t, r.GZ, solid.GZ, "GZ")
		approxEq(t, r.RightingMoment, solid.RightingMoment, "Moment")
		if r.Stability != solid.Stability {
			t.Fatalf("无舱时判定应与固体路径一致: %q vs %q", r.Stability, solid.Stability)
		}
	}
}

// δ 的分母是排水质量 Δ=ρ_w∇：换淡水，扣减按密度比放大（以米计）。
func TestFreeSurface_CorrectionScalesWithDisplacementMass(t *testing.T) {
	sea := baseParams() // 1025
	fresh := baseParams()
	fresh.WaterDensity = 1000
	tank := rectangularTank("舱", 10, 4, 850, TankStatusPartial)
	rs, err := EvaluateLoaded(sea, []Tank{tank}, 0)
	if err != nil {
		t.Fatal(err)
	}
	rf, err := EvaluateLoaded(fresh, []Tank{tank}, 0)
	if err != nil {
		t.Fatal(err)
	}
	approxEq(t, rf.FreeSurfaceCorrection, rs.FreeSurfaceCorrection*(1025.0/1000.0),
		"排水质量变小，扣减按反比放大")
}

// 液舱参数校验：几何/惯性矩不为正、状态非法、密度不为正、两种几何给法
// 冲突或全缺，都必须在计算之前挡下并指出是哪个舱、哪个字段。
func TestFreeSurface_TankValidation(t *testing.T) {
	good := func() Tank { return rectangularTank("舱", 10, 4, 850, TankStatusPartial) }
	cases := map[string]func(Tank) Tank{
		"长度为零":       func(t Tank) Tank { t.Length = 0; return t },
		"宽度为负":       func(t Tank) Tank { t.Width = -1; return t },
		"长度 NaN":     func(t Tank) Tank { t.Length = math.NaN(); return t },
		"直给惯性矩为负":    func(t Tank) Tank { t.Length, t.Width = 0, 0; t.FreeSurfaceInertia = -3; return t },
		"直给惯性矩为无穷":   func(t Tank) Tank { t.Length, t.Width = 0, 0; t.FreeSurfaceInertia = math.Inf(1); return t },
		"长宽与惯性矩同时给出": func(t Tank) Tank { t.FreeSurfaceInertia = 50; return t },
		"几何与惯性矩都不给":  func(t Tank) Tank { t.Length, t.Width = 0, 0; return t },
		"密度为零":       func(t Tank) Tank { t.LiquidDensity = 0; return t },
		"密度为负":       func(t Tank) Tank { t.LiquidDensity = -1; return t },
		"密度 NaN":     func(t Tank) Tank { t.LiquidDensity = math.NaN(); return t },
		"状态取值不在允许集合": func(t Tank) Tank { t.Status = "half-full"; return t },
		"状态为空":       func(t Tank) Tank { t.Status = ""; return t },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			tank := mutate(good())
			if _, err := EvaluateLoaded(baseParams(), []Tank{tank}, 0); err == nil {
				t.Fatalf("非法液舱应当被拦截: %s", name)
			} else if _, ok := err.(*ValidationError); !ok {
				t.Fatalf("应返回 *ValidationError，得到 %T: %v", err, err)
			}
		})
	}

	// 多舱时错误必须能定位到具体舱位。
	tanks := []Tank{
		good(),
		rectangularTank("好舱", 10, 4, 850, TankStatusFull),
		rectangularTank("坏舱", 0, 4, 850, TankStatusPartial),
	}
	_, err := EvaluateLoaded(baseParams(), tanks, 0)
	if err == nil {
		t.Fatal("第 3 个舱非法应拦下整次核算")
	}
	if msg := err.Error(); !strings.Contains(msg, "#3") || !strings.Contains(msg, "坏舱") {
		t.Fatalf("错误信息应指出舱位与舱名，得到 %q", msg)
	}
}
