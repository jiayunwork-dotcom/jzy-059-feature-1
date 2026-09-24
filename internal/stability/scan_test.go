package stability

import (
	"math"
	"testing"
)

func TestScan_GZCrisesFromZero(t *testing.T) {
	res, err := Scan(ScanParams{
		Params:   baseParams(),
		StartDeg: 0,
		EndDeg:   10,
		StepDeg:  1,
	})
	if err != nil {
		t.Fatalf("扫描失败: %v", err)
	}
	if len(res.Points) != 11 {
		t.Fatalf("0°~10° 步长 1° 应有 11 个点，得到 %d", len(res.Points))
	}
	// 首点 0°：GZ 必须为零。
	if res.Points[0].AngleDeg != 0 || res.Points[0].GZ != 0 {
		t.Fatalf("曲线首点应为 (0°, 0)，得到 (%v, %v)", res.Points[0].AngleDeg, res.Points[0].GZ)
	}
	// 正稳性船：GZ 随横倾增大单调上升；每个点都要与公式逐点对得上。
	for i, pt := range res.Points {
		wantAngle := float64(i)
		approxEq(t, pt.AngleDeg, wantAngle, "点角度")
		approxEq(t, pt.AngleRad, DegreesToRadians(wantAngle), "点弧度")
		wantGZ := res.GM * math.Sin(pt.AngleRad)
		approxEq(t, pt.GZ, wantGZ, "逐点 GZ")
		wantMoment := DefaultWaterDensity * 1000 * StandardGravity * wantGZ
		approxEq(t, pt.Moment, wantMoment, "逐点力矩")
		if !pt.SmallAngle {
			t.Fatalf("0°~10° 区间内所有点都应标记为小倾角，点 %v 异常", pt.AngleDeg)
		}
		if i > 0 && pt.GZ <= res.Points[i-1].GZ {
			t.Fatalf("GZ 应随横倾增大而上升，点 %d 处倒退", i)
		}
	}
	if res.Warning != "" {
		t.Fatalf("区间未越界不应有提醒，得到 %q", res.Warning)
	}
	// 扫描头部带回的 BM/GM/判定与单点一致。
	approxEq(t, res.BM, 5.0, "扫描 BM")
	approxEq(t, res.GM, 4.2, "扫描 GM")
	if res.Stability != StabilityPositive {
		t.Fatalf("扫描判定应为 positive，得到 %q", res.Stability)
	}
}

func TestScan_EndpointsAndPerPointWarning(t *testing.T) {
	// 不能整除的步长：8°/3° = 2.67，点为 0、3、6，末点保留 6°（不越出 8°）。
	res, err := Scan(ScanParams{
		Params:   baseParams(),
		StartDeg: 0,
		EndDeg:   8,
		StepDeg:  3,
	})
	if err != nil {
		t.Fatalf("扫描失败: %v", err)
	}
	if len(res.Points) != 3 {
		t.Fatalf("应有 3 个点，得到 %d", len(res.Points))
	}
	if res.Points[2].AngleDeg != 6 {
		t.Fatalf("末点应为 6°，得到 %v", res.Points[2].AngleDeg)
	}

	// 区间越过 10°：整体有提醒，且每个超界点逐点标记 smallAngle=false。
	over, err := Scan(ScanParams{
		Params:   baseParams(),
		StartDeg: 0,
		EndDeg:   20,
		StepDeg:  10,
	})
	if err != nil {
		t.Fatalf("扫描失败: %v", err)
	}
	if len(over.Points) != 3 || over.Points[2].AngleDeg != 20 {
		t.Fatalf("点列异常: %+v", over.Points)
	}
	if over.Warning == "" {
		t.Fatal("越界扫描应附整体提醒")
	}
	if over.Points[0].SmallAngle != true || over.Points[1].SmallAngle != true || over.Points[2].SmallAngle != false {
		t.Fatalf("0°、10° 应为小倾角，20° 应越界: %+v", over.Points)
	}
}

// 负稳性船的曲线：从 0° 出发后 GZ 向下走（力矩使船继续倾斜）。
func TestScan_NegativeStabilityCurveGoesDown(t *testing.T) {
	p := baseParams()
	p.KG = 100
	res, err := Scan(ScanParams{Params: p, StartDeg: 0, EndDeg: 5, StepDeg: 1})
	if err != nil {
		t.Fatalf("扫描失败: %v", err)
	}
	if res.Stability != StabilityNegative {
		t.Fatalf("应判 negative，得到 %q", res.Stability)
	}
	for i := 1; i < len(res.Points); i++ {
		if res.Points[i].GZ >= res.Points[i-1].GZ {
			t.Fatalf("负稳性船 GZ 应随正横倾下降，点 %d 异常", i)
		}
	}
}

func TestScan_Validation(t *testing.T) {
	good := ScanParams{Params: baseParams(), StartDeg: 0, EndDeg: 10, StepDeg: 1}
	cases := map[string]func(*ScanParams){
		"步长为零":   func(s *ScanParams) { s.StepDeg = 0 },
		"步长为负":   func(s *ScanParams) { s.StepDeg = -1 },
		"终点小于起点": func(s *ScanParams) { s.EndDeg = -5 },
		"体积非法":   func(s *ScanParams) { s.DisplacementVolume = 0 },
		"点数超上限":  func(s *ScanParams) { s.StartDeg, s.EndDeg, s.StepDeg = 0, MaxScanPoints+1, 1 },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			s := good
			mutate(&s)
			if _, err := Scan(s); err == nil {
				t.Fatalf("非法扫描参数应被拦截: %s", name)
			}
		})
	}
}

// 矩形驳船：IT = B³L/12 与手算对得上，GM 为正；
// 只把船宽加倍，IT 变 8 倍、BM 大幅抬升（∇ 同时翻倍 → BM 变 4 倍）。
func TestBarge_InertiaAndDoubleBeam(t *testing.T) {
	g := BargeGeometry{Beam: 12, Length: 40, Draft: 2.5, KG: 2.0}
	p, err := g.ToParams()
	if err != nil {
		t.Fatalf("驳船参数生成失败: %v", err)
	}
	wantIT := 12.0 * 12.0 * 12.0 * 40.0 / 12.0 // B³L/12 = 5760
	approxEq(t, p.TransverseInertia, wantIT, "IT = B³L/12")
	approxEq(t, p.DisplacementVolume, 12*40*2.5, "∇ = BLd")
	approxEq(t, p.KB, 1.25, "KB = d/2")

	r, err := Evaluate(p, 0)
	if err != nil {
		t.Fatalf("核算失败: %v", err)
	}
	approxEq(t, r.BM, 5760.0/1200.0, "BM") // 4.8
	approxEq(t, r.GM, 1.25+4.8-2.0, "GM")  // 4.05
	if r.Stability != StabilityPositive {
		t.Fatalf("预置驳船应为正稳性，得到 %q", r.Stability)
	}

	// 只把船宽加倍（其余不变）：
	//   IT' = (2B)³L/12 = 8·IT
	//   ∇' = 2B·L·d = 2∇，BM' = 8IT/2∇ = 4·BM
	g2 := g
	g2.Beam *= 2
	p2, err := g2.ToParams()
	if err != nil {
		t.Fatalf("加宽驳船参数生成失败: %v", err)
	}
	approxEq(t, p2.TransverseInertia, 8*p.TransverseInertia, "船宽加倍 IT×8")
	approxEq(t, p2.DisplacementVolume, 2*p.DisplacementVolume, "∇×2")
	r2, _ := Evaluate(p2, 0)
	approxEq(t, r2.BM, 4*r.BM, "船宽加倍 BM×4")
	if r2.BM <= r.BM {
		t.Fatal("船宽加倍后横稳心半径应大幅抬升")
	}
}

func TestBarge_RejectsNonPositiveDimensions(t *testing.T) {
	bad := []BargeGeometry{
		{Beam: 0, Length: 40, Draft: 2.5, KG: 2},
		{Beam: 12, Length: -1, Draft: 2.5, KG: 2},
		{Beam: 12, Length: 40, Draft: 0, KG: 2},
		{Beam: 12, Length: 40, Draft: 2.5, KG: -0.1},
	}
	for i, g := range bad {
		if _, err := g.ToParams(); err == nil {
			t.Fatalf("非法驳船尺度 #%d 应被拦截", i)
		}
	}
}
