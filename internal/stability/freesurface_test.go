package stability

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
)

// 单舱基准：部分注液的矩形液面燃油舱，液面 4 m 长、2 m 宽，
// 燃油密度 900 kg/m^3。
//
//	i  = l·b³/12 = 4·8/12   = 8/3 ≈ 2.6667 m^4
//	Δ  = ρ∇ = 1025·1000     = 1 025 000 kg
//	δGM = ρᵢ·i/Δ = 900·(8/3)/1 025 000 ≈ 0.00234146 m
func partialFuelTank() Tank {
	return Tank{
		Name:          "fuel-1",
		Length:        4,
		Width:         2,
		LiquidDensity: 900,
		FillingStatus: TankStatusPartial,
	}
}

func wantSingleCorrection(t Tank) float64 {
	i := t.Length * math.Pow(t.Width, 3) / 12.0
	return t.LiquidDensity * i / (DefaultWaterDensity * 1000.0)
}

// 不变量 1：挂上一个部分注液舱后，有效 GM 严格低于不挂舱的固体 GM；
// 而固体 GM 本身不因挂舱而改变。
func TestFreeSurface_LowersEffectiveGM_SolidGMUnchanged(t *testing.T) {
	solid, err := Evaluate(baseParams(), 5.0)
	if err != nil {
		t.Fatalf("固体核算失败: %v", err)
	}

	tk := partialFuelTank()
	loaded, err := EvaluateLoading(baseParams(), []Tank{tk}, 5.0)
	if err != nil {
		t.Fatalf("带舱核算失败: %v", err)
	}

	wantCorr := wantSingleCorrection(tk)
	approxEq(t, loaded.SolidGM, solid.GM, "固体 GM 不变")
	approxEq(t, loaded.FreeSurfaceCorrection, wantCorr, "总扣减")
	approxEq(t, loaded.EffectiveGM, solid.GM-wantCorr, "有效 GM")
	approxEq(t, loaded.GM, loaded.EffectiveGM, "GM 字段即有效 GM")
	if !(loaded.EffectiveGM < solid.GM) {
		t.Fatalf("部分注液舱必须严格压低有效 GM: eff=%v solid=%v", loaded.EffectiveGM, solid.GM)
	}
	// BM 与固体核算一致：自由液面不改变船的实际几何与重量分布。
	approxEq(t, loaded.BM, solid.BM, "BM 不变")
	// GZ 与力矩都必须建立在有效 GM 上，而不是固体 GM。
	wantGZ := loaded.EffectiveGM * math.Sin(loaded.AngleRad)
	approxEq(t, loaded.GZ, wantGZ, "GZ 按有效 GM")
	wantMoment := loaded.DisplacementMass * StandardGravity * wantGZ
	approxEq(t, loaded.RightingMoment, wantMoment, "力矩按有效 GZ")
}

// 不变量 2：空舱与满舱没有自由液面，扣减立刻归零；几何与密度照旧回报。
func TestFreeSurface_EmptyAndFullHaveZeroCorrection(t *testing.T) {
	for _, status := range []string{TankStatusEmpty, TankStatusFull} {
		t.Run(status, func(t *testing.T) {
			tk := partialFuelTank()
			tk.FillingStatus = status
			r, err := EvaluateLoading(baseParams(), []Tank{tk}, 3.0)
			if err != nil {
				t.Fatalf("核算失败: %v", err)
			}
			if r.FreeSurfaceCorrection != 0 || r.EffectiveGM != r.SolidGM {
				t.Fatalf("%s 舱扣减应为零: corr=%v eff=%v solid=%v",
					status, r.FreeSurfaceCorrection, r.EffectiveGM, r.SolidGM)
			}
			if len(r.TankCorrections) != 1 || r.TankCorrections[0].Correction != 0 {
				t.Fatalf("%s 舱明细扣减应为零: %+v", status, r.TankCorrections)
			}
			// 液面几何仍如实给出，只是不参与扣减。
			wantI := 4.0 * 8.0 / 12.0
			approxEq(t, r.TankCorrections[0].SurfaceInertia, wantI, "液面惯性矩照常回报")
			// 稳性与纯固体一致。
			solid, _ := Evaluate(baseParams(), 3.0)
			if r.Stability != solid.Stability {
				t.Fatalf("%s 舱不应改变稳性判定", status)
			}
		})
	}

	// 把同一个舱从 partial 改成 empty/full，扣减「立刻」归零——
	// 同一几何、同一密度，只换状态。
	partial := partialFuelTank()
	rp, _ := EvaluateLoading(baseParams(), []Tank{partial}, 0)
	if !(rp.FreeSurfaceCorrection > 0) {
		t.Fatal("部分注液舱应有正扣减")
	}
	for _, status := range []string{TankStatusEmpty, TankStatusFull} {
		tk := partial
		tk.FillingStatus = status
		r, _ := EvaluateLoading(baseParams(), []Tank{tk}, 0)
		if r.FreeSurfaceCorrection != 0 {
			t.Fatalf("状态改为 %s 后扣减必须立刻归零，得到 %v", status, r.FreeSurfaceCorrection)
		}
	}
}

// 不变量 3（边界一）：扣减只与液面几何有关，与装液体积无关。
// Tank 类型根本没有装液量字段；这里通过「同几何不同构造」佐证
// 半满与三分之一满扣减相同。
func TestFreeSurface_IndependentOfLiquidVolume(t *testing.T) {
	half := partialFuelTank()
	half.Name = "half-full"
	third := partialFuelTank() // 同样的液面长宽、同样密度，仅名义液位不同
	third.Name = "one-third"

	rh, err := EvaluateLoading(baseParams(), []Tank{half}, 0)
	if err != nil {
		t.Fatal(err)
	}
	rt, err := EvaluateLoading(baseParams(), []Tank{third}, 0)
	if err != nil {
		t.Fatal(err)
	}
	approxEq(t, rt.FreeSurfaceCorrection, rh.FreeSurfaceCorrection, "扣减与装液体积无关")
	approxEq(t, rt.EffectiveGM, rh.EffectiveGM, "有效 GM 与装液体积无关")
}

// 不变量 3（边界二）：几何完全相同的两个舱，液体密度更大的扣减更大；
// 总扣减是各舱扣减的简单相加。
func TestFreeSurface_DensityScalesAndCorrectionsAdd(t *testing.T) {
	light := partialFuelTank()
	light.Name = "light"
	heavy := partialFuelTank()
	heavy.Name = "heavy"
	heavy.LiquidDensity = 1000 // 同几何、更高密度（压载水）

	rl, err := EvaluateLoading(baseParams(), []Tank{light}, 0)
	if err != nil {
		t.Fatal(err)
	}
	rh, err := EvaluateLoading(baseParams(), []Tank{heavy}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !(rh.FreeSurfaceCorrection > rl.FreeSurfaceCorrection) {
		t.Fatalf("密度更大的舱扣减应更大: heavy=%v light=%v",
			rh.FreeSurfaceCorrection, rl.FreeSurfaceCorrection)
	}
	// 密度加倍 ⇒ 扣减严格按比例加倍。
	approxEq(t, rh.FreeSurfaceCorrection, rl.FreeSurfaceCorrection*(1000.0/900.0), "扣减与密度成正比")

	// 两舱同时挂上：总扣减 = 单舱扣减简单相加，与顺序无关。
	both, err := EvaluateLoading(baseParams(), []Tank{light, heavy}, 0)
	if err != nil {
		t.Fatal(err)
	}
	approxEq(t, both.FreeSurfaceCorrection, rl.FreeSurfaceCorrection+rh.FreeSurfaceCorrection,
		"总扣减为各舱之和")
	approxEq(t, both.EffectiveGM, both.SolidGM-both.FreeSurfaceCorrection, "有效 GM")
	if len(both.TankCorrections) != 2 {
		t.Fatalf("应有两条逐舱明细，得到 %d", len(both.TankCorrections))
	}

	reversed, _ := EvaluateLoading(baseParams(), []Tank{heavy, light}, 0)
	approxEq(t, reversed.FreeSurfaceCorrection, both.FreeSurfaceCorrection, "扣减与舱序无关")
}

// 直接给出自由液面惯性矩（非矩形液面）：与给出等价长宽算得一致。
func TestFreeSurface_DirectInertia(t *testing.T) {
	rect := partialFuelTank()
	i := rect.Length * math.Pow(rect.Width, 3) / 12.0

	direct := Tank{
		Name:               "irregular",
		FreeSurfaceInertia: i,
		LiquidDensity:      900,
		FillingStatus:      TankStatusPartial,
	}
	r1, err := EvaluateLoading(baseParams(), []Tank{rect}, 0)
	if err != nil {
		t.Fatal(err)
	}
	r2, err := EvaluateLoading(baseParams(), []Tank{direct}, 0)
	if err != nil {
		t.Fatal(err)
	}
	approxEq(t, r2.FreeSurfaceCorrection, r1.FreeSurfaceCorrection, "直给惯性矩与矩形公式等价")
	approxEq(t, r2.TankCorrections[0].SurfaceInertia, i, "直给惯性矩原样回报")
}

// 核心场景：固体 GM 为正，挂上足够多、足够宽的部分注液舱后，
// 有效 GM 被扣成负，最终判定必须翻成 negative——不能被数值悄悄吞掉。
func TestFreeSurface_FlipsPositiveToNegative(t *testing.T) {
	p := baseParams() // 固体 GM = 4.2
	solid, err := Evaluate(p, 0)
	if err != nil {
		t.Fatal(err)
	}
	if solid.Stability != StabilityPositive {
		t.Fatalf("前置条件应为正稳性，得到 %q", solid.Stability)
	}

	// 三个 8 m 宽、2 m 长、密度 1000 的部分注液舱：
	//	i = 2·512/12 = 85.333 m^4，单舱 δGM = 1000·85.333/1 025 000 ≈ 0.083252 m
	//	三舱合计 ≈ 0.24976 m —— 还不够。改用 20 m 宽：
	//	i = 2·8000/12 = 1333.33 m^4，单舱 δGM = 1.30081 m，三舱 ≈ 3.9024 m，
	//	有效 GM = 4.2 − 3.9024 ≈ 0.2976 仍为正；取 22 m 宽：
	//	i = 2·10648/12 = 1774.67 m^4，单舱 δGM ≈ 1.73139 m，三舱 ≈ 5.1942 m，
	//	有效 GM = 4.2 − 5.1942 ≈ −0.9942 m → 负稳性。
	tanks := []Tank{
		{Name: "wing-port", Length: 2, Width: 22, LiquidDensity: 1000, FillingStatus: TankStatusPartial},
		{Name: "wing-starboard", Length: 2, Width: 22, LiquidDensity: 1000, FillingStatus: TankStatusPartial},
		{Name: "slop", Length: 2, Width: 22, LiquidDensity: 1000, FillingStatus: TankStatusPartial},
	}
	loaded, err := EvaluateLoading(p, tanks, 5.0)
	if err != nil {
		t.Fatalf("带舱核算失败: %v", err)
	}
	if !(loaded.SolidGM > 0) {
		t.Fatalf("固体 GM 应仍为正，得到 %v", loaded.SolidGM)
	}
	if !(loaded.EffectiveGM < 0) {
		t.Fatalf("有效 GM 应被扣成负，得到 %v（扣减 %v）", loaded.EffectiveGM, loaded.FreeSurfaceCorrection)
	}
	if loaded.Stability != StabilityNegative {
		t.Fatalf("最终判定必须落在修正后的值上，得到 %q", loaded.Stability)
	}
	// 正横倾下 GZ/力矩为负：自由液面让船继续倾斜而不是扶正。
	if loaded.GZ >= 0 || loaded.RightingMoment >= 0 {
		t.Fatalf("负有效 GM 船正横倾时 GZ 应为负，得到 GZ=%v M=%v", loaded.GZ, loaded.RightingMoment)
	}

	// 同样三个舱改成满舱：稳性回到正，证明翻负完全来自自由液面。
	full := make([]Tank, len(tanks))
	for i, tk := range tanks {
		tk.FillingStatus = TankStatusFull
		full[i] = tk
	}
	rFull, err := EvaluateLoading(p, full, 5.0)
	if err != nil {
		t.Fatal(err)
	}
	if rFull.Stability != StabilityPositive {
		t.Fatalf("同批舱灌满后应回到正稳性，得到 %q", rFull.Stability)
	}
}

// 不变量：横倾为零时 GZ 与复原力矩恒为零，引入液舱后依然成立，
// 正、负有效稳性皆然。
func TestFreeSurface_ZeroAngleStillZeroGZ(t *testing.T) {
	cases := [][]Tank{
		nil,
		{partialFuelTank()},
	}
	// 再加一组把有效 GM 扣成负的舱。
	negative := []Tank{
		{Length: 2, Width: 22, LiquidDensity: 1000, FillingStatus: TankStatusPartial},
		{Length: 2, Width: 22, LiquidDensity: 1000, FillingStatus: TankStatusPartial},
		{Length: 2, Width: 22, LiquidDensity: 1000, FillingStatus: TankStatusPartial},
	}
	cases = append(cases, negative)
	for i, tanks := range cases {
		r, err := EvaluateLoading(baseParams(), tanks, 0)
		if err != nil {
			t.Fatalf("用例 %d 核算失败: %v", i, err)
		}
		if r.GZ != 0 || r.RightingMoment != 0 {
			t.Fatalf("用例 %d：0° 时 GZ/力矩必须恒为零，得到 GZ=%v M=%v", i, r.GZ, r.RightingMoment)
		}
		if r.AngleRad != 0 || r.AngleDeg != 0 {
			t.Fatalf("用例 %d：0° 角度回报异常", i)
		}
	}
}

// 水密度对修正的影响：δGM = (ρᵢ/ρ水)·i/∇，米制扣减随内外密度比变化——
// 换成淡水后外水密度变小，扣减按 1025/1000 略增。这与纯固体 BM = IT/∇
// （不含密度）不同，必须区分；力矩（牛·米）则仍随水密度同比缩放。
func TestFreeSurface_WaterDensityEntersViaDensityRatio(t *testing.T) {
	tanks := []Tank{partialFuelTank()}
	rSea, err := EvaluateLoading(baseParams(), tanks, 5.0)
	if err != nil {
		t.Fatal(err)
	}
	pFresh := baseParams()
	pFresh.WaterDensity = 1000
	rFresh, err := EvaluateLoading(pFresh, tanks, 5.0)
	if err != nil {
		t.Fatal(err)
	}
	// 固体 GM（只含 BM=IT/∇）不随水密度变。
	approxEq(t, rFresh.SolidGM, rSea.SolidGM, "淡水固体 GM")
	// 扣减含密度比 ρᵢ/ρ水：淡水扣减 = 海水扣减 × 1025/1000。
	approxEq(t, rFresh.FreeSurfaceCorrection,
		rSea.FreeSurfaceCorrection*(DefaultWaterDensity/1000.0), "淡水扣减按密度比增大")
	approxEq(t, rFresh.EffectiveGM,
		rFresh.SolidGM-rFresh.FreeSurfaceCorrection, "淡水有效 GM")
	// 力矩 = ρ水∇·g·GZ_eff，直接与外水密度成正比。
	wantMoment := 1000.0 * 1000.0 * StandardGravity * rFresh.GZ
	approxEq(t, rFresh.RightingMoment, wantMoment, "淡水力矩")
	// 同一液舱放进与舱内同密度的液体环境（ρ水=ρᵢ=900）：δGM = i/∇。
	pSame := baseParams()
	pSame.WaterDensity = 900
	rSame, err := EvaluateLoading(pSame, tanks, 5.0)
	if err != nil {
		t.Fatal(err)
	}
	wantI := 4.0 * 8.0 / 12.0
	approxEq(t, rSame.FreeSurfaceCorrection, wantI/1000.0, "内外同密度时 δGM = i/∇")
}

func TestFreeSurface_ValidationErrors(t *testing.T) {
	good := partialFuelTank()
	cases := map[string]Tank{
		"状态非法":    func() Tank { t := good; t.FillingStatus = "half"; return t }(),
		"状态为空":    func() Tank { t := good; t.FillingStatus = ""; return t }(),
		"密度为零":    func() Tank { t := good; t.LiquidDensity = 0; return t }(),
		"密度为负":    func() Tank { t := good; t.LiquidDensity = -1; return t }(),
		"密度为 NaN": func() Tank { t := good; t.LiquidDensity = math.NaN(); return t }(),
		"长宽都缺":    func() Tank { t := good; t.Length, t.Width = 0, 0; return t }(),
		"只给长不给宽":  func() Tank { t := good; t.Width = 0; return t }(),
		"长为负":     func() Tank { t := good; t.Length = -1; return t }(),
		"宽为负":     func() Tank { t := good; t.Width = -2; return t }(),
		"直给惯性矩为负": func() Tank {
			t := Tank{Name: "x", FreeSurfaceInertia: -3, LiquidDensity: 900, FillingStatus: TankStatusPartial}
			return t
		}(),
		"长宽零且惯性矩也零": func() Tank { t := Tank{Name: "x", LiquidDensity: 900, FillingStatus: TankStatusFull}; return t }(),
		"长为无穷":      func() Tank { t := good; t.Length = math.Inf(1); return t }(),
	}
	for name, tk := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := EvaluateLoading(baseParams(), []Tank{tk}, 0)
			if err == nil {
				t.Fatalf("非法液舱应当被拦截: %s", name)
			}
			ve, ok := err.(*ValidationError)
			if !ok {
				t.Fatalf("应返回 *ValidationError，得到 %T", err)
			}
			// 必须说清是哪个舱（#1）。
			if !strings.Contains(ve.Reason, "#1") {
				t.Fatalf("错误信息应指明舱序号，得到 %q", ve.Reason)
			}
		})
	}

	// 多舱清单里第 3 个舱非法：报错要定位到 #3 而不是笼统失败。
	tanks := []Tank{good, good, good, good}
	tanks[2].LiquidDensity = 0
	_, err := FreeSurfaceCorrection(baseParams(), tanks)
	if err == nil || !strings.Contains(err.Error(), "#3") {
		t.Fatalf("应定位到第 3 个舱，得到 %v", err)
	}

	// 浮态本身非法时照样拦截，即使液舱清单为空。
	if _, err := EvaluateLoading(Params{}, nil, 0); err == nil {
		t.Fatal("空浮态应被拦截")
	}
}

// 带舱名时错误信息带舱名，便于设计者定位是燃油舱还是压载舱。
func TestFreeSurface_ValidationErrorNamesTank(t *testing.T) {
	tk := partialFuelTank()
	tk.Name = "service-daily"
	tk.Width = 0
	_, err := EvaluateLoading(baseParams(), []Tank{tk}, 0)
	if err == nil || !strings.Contains(err.Error(), "service-daily") {
		t.Fatalf("错误信息应带舱名，得到 %v", err)
	}
}

// 带舱扫描：整条 GZ 曲线都必须建立在有效 GM 上，头部把三笔账写明。
func TestFreeSurface_ScanCurveUsesEffectiveGM(t *testing.T) {
	tanks := []Tank{partialFuelTank()}
	res, err := Scan(ScanParams{
		Params:   baseParams(),
		Tanks:    tanks,
		StartDeg: 0,
		EndDeg:   10,
		StepDeg:  2,
	})
	if err != nil {
		t.Fatalf("带舱扫描失败: %v", err)
	}
	solid, _ := Evaluate(baseParams(), 0)
	wantCorr := wantSingleCorrection(partialFuelTank())
	approxEq(t, res.SolidGM, solid.GM, "扫描头部固体 GM")
	approxEq(t, res.FreeSurfaceCorrection, wantCorr, "扫描头部总扣减")
	approxEq(t, res.EffectiveGM, solid.GM-wantCorr, "扫描头部有效 GM")
	approxEq(t, res.GM, res.EffectiveGM, "GM 即有效 GM")
	if res.Stability != StabilityPositive {
		t.Fatalf("该算例仍应为正稳性，得到 %q", res.Stability)
	}
	if len(res.Points) != 6 {
		t.Fatalf("应有 6 个点，得到 %d", len(res.Points))
	}
	for i, pt := range res.Points {
		deg := float64(i * 2)
		approxEq(t, pt.AngleDeg, deg, "点角度")
		wantGZ := res.EffectiveGM * math.Sin(DegreesToRadians(deg))
		approxEq(t, pt.GZ, wantGZ, "曲线逐点按有效 GM")
		solidGZ := solid.GM * math.Sin(DegreesToRadians(deg))
		if i > 0 && !(pt.GZ < solidGZ) {
			t.Fatalf("点 %d 的带舱 GZ 必须严格低于刚体曲线", i)
		}
		wantMoment := DefaultWaterDensity * 1000 * StandardGravity * wantGZ
		approxEq(t, pt.Moment, wantMoment, "点力矩按有效 GZ")
	}
	// 首点 0°：GZ 恒为零，哪怕有效 GM 已被扣成负。
	if res.Points[0].GZ != 0 || res.Points[0].Moment != 0 {
		t.Fatalf("0° 首点必须为零: %+v", res.Points[0])
	}
}

// 负有效稳性船的带舱扫描：头部判定为负，正横倾段 GZ 向下走。
func TestFreeSurface_ScanNegativeCurveGoesDown(t *testing.T) {
	tanks := []Tank{
		{Length: 2, Width: 22, LiquidDensity: 1000, FillingStatus: TankStatusPartial},
		{Length: 2, Width: 22, LiquidDensity: 1000, FillingStatus: TankStatusPartial},
		{Length: 2, Width: 22, LiquidDensity: 1000, FillingStatus: TankStatusPartial},
	}
	res, err := Scan(ScanParams{
		Params:   baseParams(),
		Tanks:    tanks,
		StartDeg: 0,
		EndDeg:   6,
		StepDeg:  2,
	})
	if err != nil {
		t.Fatalf("扫描失败: %v", err)
	}
	if !(res.SolidGM > 0) || res.Stability != StabilityNegative || !(res.EffectiveGM < 0) {
		t.Fatalf("固体正、有效负的预期不符: solid=%v eff=%v stability=%q",
			res.SolidGM, res.EffectiveGM, res.Stability)
	}
	if res.Points[0].GZ != 0 {
		t.Fatal("0° GZ 必须为零")
	}
	for i := 1; i < len(res.Points); i++ {
		if res.Points[i].GZ >= res.Points[i-1].GZ {
			t.Fatalf("负有效稳性曲线应下行，点 %d 异常", i)
		}
	}
}

// 扫描的液舱校验：非法舱在扫描前被拦下。
func TestFreeSurface_ScanRejectsBadTank(t *testing.T) {
	bad := partialFuelTank()
	bad.FillingStatus = "sloshing"
	_, err := Scan(ScanParams{
		Params:   baseParams(),
		Tanks:    []Tank{bad},
		StartDeg: 0, EndDeg: 5, StepDeg: 1,
	})
	if err == nil {
		t.Fatal("非法液舱的扫描应被拦截")
	}
}

// JSON 兼容性：不带液舱时响应里绝不出现修正字段；带液舱时三笔账齐全，
func TestResult_JSONShape(t *testing.T) {
	solid, _ := Evaluate(baseParams(), 0)
	data, err := json.Marshal(solid)
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	for _, key := range []string{"solidGm", "freeSurfaceCorrection", "effectiveGm", "tankCorrections"} {
		if strings.Contains(s, key) {
			t.Fatalf("不带液舱的响应不应含 %q: %s", key, s)
		}
	}

	loaded, err := EvaluateLoading(baseParams(), []Tank{partialFuelTank()}, 0)
	if err != nil {
		t.Fatal(err)
	}
	data, err = json.Marshal(loaded)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"solidGm", "freeSurfaceCorrection", "effectiveGm", "tankCorrections"} {
		if _, ok := got[key]; !ok {
			t.Fatalf("带液舱响应必须含 %q: %s", key, data)
		}
	}
	if math.Abs(got["gm"].(float64)-got["effectiveGm"].(float64)) > 1e-12 {
		t.Fatal("gm 应等于 effectiveGm")
	}

	// 临界：构造总扣减恰好等于固体 GM 的液舱（直给惯性矩），有效 GM=0，
	// 字段仍须出现。
	delta := DefaultWaterDensity * 1000 * 4.2 / 800.0 // 使 ρᵢi/Δ = 4.2
	edge := Tank{Name: "edge", FreeSurfaceInertia: delta, LiquidDensity: 800, FillingStatus: TankStatusPartial}
	rEdge, err := EvaluateLoading(baseParams(), []Tank{edge}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(rEdge.EffectiveGM) > 1e-9 || rEdge.Stability != StabilityNeutral {
		t.Fatalf("临界应中性: eff=%v stability=%q", rEdge.EffectiveGM, rEdge.Stability)
	}
	data, err = json.Marshal(rEdge)
	var edgeBody map[string]any
	if err := json.Unmarshal(data, &edgeBody); err != nil {
		t.Fatal(err)
	}
	if v, ok := edgeBody["effectiveGm"].(float64); !ok || v != 0 {
		t.Fatalf("有效 GM 为 0 也必须出现在 JSON 中: %s", data)
	}

	// 扫描结果同样：无舱不含修正字段，带舱三笔账齐全。
	plainScan, _ := Scan(ScanParams{Params: baseParams(), StartDeg: 0, EndDeg: 2, StepDeg: 1})
	ps, _ := json.Marshal(plainScan)
	for _, key := range []string{"solidGm", "freeSurfaceCorrection", "effectiveGm", "tankCorrections"} {
		if strings.Contains(string(ps), key) {
			t.Fatalf("不带液舱的扫描响应不应含 %q: %s", key, ps)
		}
	}
	loadedScan, _ := Scan(ScanParams{
		Params: baseParams(), Tanks: []Tank{partialFuelTank()},
		StartDeg: 0, EndDeg: 2, StepDeg: 1,
	})
	ls, _ := json.Marshal(loadedScan)
	var scanBody map[string]any
	if err := json.Unmarshal(ls, &scanBody); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"solidGm", "freeSurfaceCorrection", "effectiveGm", "tankCorrections"} {
		if _, ok := scanBody[key]; !ok {
			t.Fatalf("带液舱扫描响应必须含 %q: %s", key, ls)
		}
	}
}
