package stability

import (
	"math"
	"testing"
)

// 测试用基准参数：∇=1000 m^3, KB=1.2m, KG=2.0m, IT=5000 m^4。
//
//	BM = 5.0 m，GM = 1.2 + 5.0 − 2.0 = 4.2 m（正稳性）。
func baseParams() Params {
	return Params{
		DisplacementVolume: 1000,
		KB:                 1.2,
		KG:                 2.0,
		TransverseInertia:  5000,
		WaterDensity:       0, // 触发默认海水密度
	}
}

const tol = 1e-9

func approxEq(t *testing.T, got, want float64, label string) {
	t.Helper()
	if math.Abs(got-want) > tol*math.Max(1, math.Abs(want)) {
		t.Fatalf("%s = %.12f, 期望 %.12f", label, got, want)
	}
}

func TestEvaluate_PositiveStabilityCore(t *testing.T) {
	r, err := Evaluate(baseParams(), 5.0)
	if err != nil {
		t.Fatalf("核算失败: %v", err)
	}
	approxEq(t, r.BM, 5.0, "BM")
	approxEq(t, r.GM, 4.2, "GM")
	if r.Stability != StabilityPositive {
		t.Fatalf("初稳性判定 = %q, 期望 positive", r.Stability)
	}
	// GZ = GM·sin(5°)；5° = π/36 弧度。
	wantGZ := 4.2 * math.Sin(DegreesToRadians(5.0))
	approxEq(t, r.GZ, wantGZ, "GZ")
	// 角度必须同时以度和弧度回报，且弧度确由度换算而来。
	approxEq(t, r.AngleDeg, 5.0, "angleDeg")
	approxEq(t, r.AngleRad, math.Pi/36, "angleRad")
	// 排水质量与复原力矩：Δ = ρ∇，M = Δ·g·GZ。
	approxEq(t, r.DisplacementMass, DefaultWaterDensity*1000, "排水质量")
	wantMoment := DefaultWaterDensity * 1000 * StandardGravity * wantGZ
	approxEq(t, r.RightingMoment, wantMoment, "复原力矩")
	if !r.SmallAngle || r.Warning != "" {
		t.Fatalf("5° 应属小倾角且无越界提醒，得到 smallAngle=%v warning=%q", r.SmallAngle, r.Warning)
	}
}

func TestEvaluate_NegativeStabilityWhenKGTooHigh(t *testing.T) {
	p := baseParams()
	p.KG = 100.0 // 远超 KB + BM = 6.2
	r, err := Evaluate(p, 5.0)
	if err != nil {
		t.Fatalf("核算失败: %v", err)
	}
	approxEq(t, r.GM, 1.2+5.0-100.0, "GM")
	if r.Stability != StabilityNegative {
		t.Fatalf("KG 超过 KB+BM 时应判 negative，得到 %q", r.Stability)
	}
	// GM 为负时 GZ 与横倾角反向：正横倾角下产生继续倾斜的力矩（负值）。
	if r.GZ >= 0 || r.RightingMoment >= 0 {
		t.Fatalf("负稳性船正横倾时 GZ/力矩应为负（加剧横倾），得到 GZ=%v M=%v", r.GZ, r.RightingMoment)
	}
}

func TestEvaluate_ZeroAngleGivesZeroGZ(t *testing.T) {
	// 正、负稳性两条船在 0° 横倾时 GZ 都必须恒为零。
	for _, kg := range []float64{2.0, 100.0} {
		p := baseParams()
		p.KG = kg
		r, err := Evaluate(p, 0.0)
		if err != nil {
			t.Fatalf("核算失败: %v", err)
		}
		if r.GZ != 0 || r.RightingMoment != 0 {
			t.Fatalf("横倾 0° 时 GZ 与力矩应恒为零，得到 GZ=%v M=%v (KG=%v)", r.GZ, r.RightingMoment, kg)
		}
	}
}

// 不变关系：只把 IT 加倍，BM 与 GM 同步增加相同的增量 Δ = IT/∇（基准 BM）。
func TestInvariant_DoubleIT(t *testing.T) {
	r1, _ := Evaluate(baseParams(), 7.0)
	p2 := baseParams()
	p2.TransverseInertia *= 2
	r2, err := Evaluate(p2, 7.0)
	if err != nil {
		t.Fatalf("核算失败: %v", err)
	}
	deltaBM := r2.BM - r1.BM
	deltaGM := r2.GM - r1.GM
	approxEq(t, deltaBM, r1.BM, "BM 增量应等于原 BM")
	approxEq(t, deltaGM, deltaBM, "GM 增量应与 BM 增量相同")
	// GZ 增量 = ΔGM·sinφ。
	approxEq(t, r2.GZ-r1.GZ, deltaGM*math.Sin(r1.AngleRad), "GZ 增量")
}

// 不变关系：只抬高 KG，GM 随之线性下降；越过 KB+BM 后转负。
func TestInvariant_KGLowersGMLinearlyThenNegative(t *testing.T) {
	const delta = 0.37
	r1, _ := Evaluate(baseParams(), 3.0)
	p2 := baseParams()
	p2.KG += delta
	r2, _ := Evaluate(p2, 3.0)
	approxEq(t, r1.GM-r2.GM, delta, "GM 应随 KG 等量下降")
	approxEq(t, r1.GZ-r2.GZ, delta*math.Sin(r1.AngleRad), "GZ 应随 GM 下降")

	// KG 恰为 KB+BM 临界，再高一点即负稳性。
	critical := baseParams()
	critical.KG = 1.2 + 5.0
	rc, err := Evaluate(critical, 3.0)
	if err != nil {
		t.Fatalf("核算失败: %v", err)
	}
	if rc.Stability != StabilityNeutral {
		t.Fatalf("GM≈0 应判 neutral，得到 %q (GM=%v)", rc.Stability, rc.GM)
	}
	critical.KG += 0.01
	rn, _ := Evaluate(critical, 3.0)
	if rn.Stability != StabilityNegative {
		t.Fatalf("KG 超过 KB+BM 应判 negative，得到 %q", rn.Stability)
	}
}

// 不变关系：改变水密度只影响力矩的牛顿数值，不影响以米计的 BM/GM/GZ。
func TestInvariant_DensityOnlyAffectsMoment(t *testing.T) {
	rSea, _ := Evaluate(baseParams(), 8.0)
	pFresh := baseParams()
	pFresh.WaterDensity = 1000
	rFresh, err := Evaluate(pFresh, 8.0)
	if err != nil {
		t.Fatalf("核算失败: %v", err)
	}
	approxEq(t, rFresh.GM, rSea.GM, "GM（米）")
	approxEq(t, rFresh.BM, rSea.BM, "BM（米）")
	approxEq(t, rFresh.GZ, rSea.GZ, "GZ（米）")
	// 力矩比 = 密度比（排水质量也随之改变）。
	approxEq(t, rFresh.RightingMoment, rSea.RightingMoment*(1000.0/DefaultWaterDensity), "淡水力矩")
	approxEq(t, rFresh.DisplacementMass, 1000*1000.0, "淡水排水质量")
}

// 专门盯住角度单位换算：Evaluate 必须先把度换成弧度再进 sin。
// 若误把度直接传给 math.Sin，10° 处会差出一截。
func TestAngleConversion_DegreesNotFedToSin(t *testing.T) {
	const deg = 10.0
	r, err := Evaluate(baseParams(), deg)
	if err != nil {
		t.Fatalf("核算失败: %v", err)
	}
	wantRad := deg * math.Pi / 180.0
	approxEq(t, r.AngleRad, wantRad, "10° 对应弧度")
	wantGZCorrect := r.GM * math.Sin(wantRad)
	wrongGZ := r.GM * math.Sin(deg) // 经典错误：把度当弧度
	approxEq(t, r.GZ, wantGZCorrect, "GZ（弧度换算正确）")
	if math.Abs(r.GZ-wrongGZ) < 0.01 {
		t.Fatalf("GZ 与「度直接进 sin」的错误结果几乎相等，怀疑未做单位换算")
	}
	// 10° 恰为小倾角边界（含等号），不应越界提醒。
	if !r.SmallAngle || r.Warning != "" {
		t.Fatalf("10° 恰好是边界，应视为小倾角，得到 smallAngle=%v warning=%q", r.SmallAngle, r.Warning)
	}

	// 10.5° 越过边界：照常给值，但必须附越界提醒。
	rOver, err := Evaluate(baseParams(), 10.5)
	if err != nil {
		t.Fatalf("核算失败: %v", err)
	}
	if rOver.SmallAngle || rOver.Warning == "" {
		t.Fatalf("10.5° 应越界且有提醒，得到 smallAngle=%v warning=%q", rOver.SmallAngle, rOver.Warning)
	}
	approxEq(t, rOver.GZ, rOver.GM*math.Sin(DegreesToRadians(10.5)), "超界仍按公式给值")

	// 负角度同样换算，且 GZ 符号随角度反转。
	rNeg, _ := Evaluate(baseParams(), -10.0)
	approxEq(t, rNeg.AngleRad, -wantRad, "−10° 对应弧度")
	approxEq(t, rNeg.GZ, -wantGZCorrect, "−10° 的 GZ")
}

func TestValidation_RejectsIllegalParams(t *testing.T) {
	cases := map[string]func(Params) Params{
		"排水体积为零":    func(p Params) Params { p.DisplacementVolume = 0; return p },
		"排水体积为负":    func(p Params) Params { p.DisplacementVolume = -5; return p },
		"惯性矩为零":     func(p Params) Params { p.TransverseInertia = 0; return p },
		"惯性矩为负":     func(p Params) Params { p.TransverseInertia = -1; return p },
		"KB 为负":     func(p Params) Params { p.KB = -0.1; return p },
		"KG 为负":     func(p Params) Params { p.KG = -1; return p },
		"水密度为负":     func(p Params) Params { p.WaterDensity = -1025; return p },
		"排水体积为 NaN": func(p Params) Params { p.DisplacementVolume = math.NaN(); return p },
		"KB 为正无穷":   func(p Params) Params { p.KB = math.Inf(1); return p },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			p := mutate(baseParams())
			if _, err := Evaluate(p, 5.0); err == nil {
				t.Fatalf("非法参数应当被拦截: %s", name)
			} else if _, ok := err.(*ValidationError); !ok {
				t.Fatalf("应返回 *ValidationError，得到 %T: %v", err, err)
			}
		})
	}
	// 角度本身为 NaN 也必须拦截。
	if _, err := Evaluate(baseParams(), math.NaN()); err == nil {
		t.Fatal("NaN 横倾角应被拦截")
	}
}
