package stability

import (
	"math"
	"testing"
)

// 带液舱扫描：整条 GZ 曲线都必须建立在修正后的有效 GM 上。
func TestScanLoaded_CurveUsesEffectiveGM(t *testing.T) {
	tank := rectangularTank("燃油舱", 10, 4, 850, TankStatusPartial)
	sp := ScanParams{Params: baseParams(), Tanks: []Tank{tank}, StartDeg: 0, EndDeg: 10, StepDeg: 2}
	res, err := ScanLoaded(sp)
	if err != nil {
		t.Fatalf("带舱扫描失败: %v", err)
	}
	wantInertia := 10.0 * math.Pow(4, 3) / 12.0
	wantDelta := 850.0 * wantInertia / baseDisplacementMass()
	approxEq(t, res.SolidGM, 4.2, "固体 GM")
	approxEq(t, res.FreeSurfaceCorrection, wantDelta, "总扣减")
	approxEq(t, res.EffectiveGM, 4.2-wantDelta, "有效 GM")
	approxEq(t, res.GM, res.EffectiveGM, "头部 GM 为有效值")
	if res.Stability != StabilityPositive {
		t.Fatalf("应判 positive，得到 %q", res.Stability)
	}
	if len(res.Points) != 6 {
		t.Fatalf("应有 6 个点，得到 %d", len(res.Points))
	}
	for i, pt := range res.Points {
		deg := float64(i * 2)
		approxEq(t, pt.AngleDeg, deg, "点角度")
		// 逐点核对：GZ = GM_eff·sinφ，绝不许用固体 GM。
		wantGZ := res.EffectiveGM * math.Sin(pt.AngleRad)
		approxEq(t, pt.GZ, wantGZ, "带舱逐点 GZ")
		solidGZ := 4.2 * math.Sin(pt.AngleRad)
		if i > 0 && pt.GZ >= solidGZ {
			t.Fatalf("点 %v 的 GZ 必须严格低于刚体乐观曲线: %v vs %v", deg, pt.GZ, solidGZ)
		}
		wantMoment := baseDisplacementMass() * StandardGravity * wantGZ
		approxEq(t, pt.Moment, wantMoment, "带舱逐点力矩")
	}
	// 首点 0°：GZ 恒为零，不因液舱改变。
	if res.Points[0].GZ != 0 || res.Points[0].Moment != 0 {
		t.Fatalf("0° 首点 GZ/力矩应为零: %+v", res.Points[0])
	}
}

// 固体正稳性、挂舱后转负：扫描判定与整条曲线都翻成负稳性形态。
func TestScanLoaded_FlipsNegativeAcrossCurve(t *testing.T) {
	p := baseParams()
	p.KG = 6.0 // 固体 GM = 0.2
	tank := rectangularTank("宽舱", 20, 10, 1000, TankStatusPartial)
	res, err := ScanLoaded(ScanParams{Params: p, Tanks: []Tank{tank}, StartDeg: 0, EndDeg: 6, StepDeg: 2})
	if err != nil {
		t.Fatal(err)
	}
	if res.SolidGM <= 0 {
		t.Fatalf("固体 GM 应为正，得到 %v", res.SolidGM)
	}
	if res.EffectiveGM >= 0 || res.Stability != StabilityNegative {
		t.Fatalf("挂舱后应判 negative: eff=%v label=%q", res.EffectiveGM, res.Stability)
	}
	for i := 1; i < len(res.Points); i++ {
		if res.Points[i].GZ >= res.Points[i-1].GZ {
			t.Fatalf("负有效 GM 曲线应随正横倾向下走，点 %d 异常", i)
		}
	}
}

// 无舱扫描（原 Scan 入口）与刚体结果完全一致，修正字段为零。
func TestScan_NoTanksUnchanged(t *testing.T) {
	res, err := Scan(ScanParams{Params: baseParams(), StartDeg: 0, EndDeg: 10, StepDeg: 1})
	if err != nil {
		t.Fatal(err)
	}
	approxEq(t, res.GM, 4.2, "GM")
	approxEq(t, res.SolidGM, 4.2, "solidGM")
	approxEq(t, res.EffectiveGM, 4.2, "effectiveGM")
	if res.FreeSurfaceCorrection != 0 || res.TankCorrections != nil {
		t.Fatalf("无舱不应出现修正: δ=%v details=%v", res.FreeSurfaceCorrection, res.TankCorrections)
	}
	// 显式给空液舱切片走 ScanLoaded 同样无修正。
	res2, err := ScanLoaded(ScanParams{Params: baseParams(), Tanks: []Tank{}, StartDeg: 0, EndDeg: 4, StepDeg: 2})
	if err != nil {
		t.Fatal(err)
	}
	if res2.FreeSurfaceCorrection != 0 || res2.GM != 4.2 {
		t.Fatalf("空舱清单应等同刚体: δ=%v GM=%v", res2.FreeSurfaceCorrection, res2.GM)
	}
}

// 空舱/满舱不影响曲线；把唯一舱从 partial 改成 empty/full 后曲线立刻
// 回到固体 GM 那条。
func TestScanLoaded_EmptyFullTanksNoCorrection(t *testing.T) {
	partial := rectangularTank("舱", 10, 4, 1000, TankStatusPartial)
	rp, err := ScanLoaded(ScanParams{Params: baseParams(), Tanks: []Tank{partial}, StartDeg: 0, EndDeg: 10, StepDeg: 5})
	if err != nil {
		t.Fatal(err)
	}
	if rp.FreeSurfaceCorrection <= 0 {
		t.Fatal("部分注液舱应有正扣减")
	}
	for _, status := range []string{TankStatusEmpty, TankStatusFull} {
		tk := partial
		tk.Status = status
		r, err := ScanLoaded(ScanParams{Params: baseParams(), Tanks: []Tank{tk}, StartDeg: 0, EndDeg: 10, StepDeg: 5})
		if err != nil {
			t.Fatal(err)
		}
		if r.FreeSurfaceCorrection != 0 || r.GM != 4.2 {
			t.Fatalf("%s 舱不应有扣减: δ=%v GM=%v", status, r.FreeSurfaceCorrection, r.GM)
		}
		for i, pt := range r.Points {
			want := 4.2 * math.Sin(pt.AngleRad)
			approxEq(t, pt.GZ, want, status+" 曲线点 "+itoaDeg(i))
		}
	}
}

// 扫描前非法液舱同样被拦截。
func TestScanLoaded_RejectsBadTank(t *testing.T) {
	bad := rectangularTank("坏舱", 0, 4, 850, TankStatusPartial)
	if _, err := ScanLoaded(ScanParams{
		Params:   baseParams(),
		Tanks:    []Tank{bad},
		StartDeg: 0, EndDeg: 10, StepDeg: 2,
	}); err == nil {
		t.Fatal("非法液舱应在扫描前被拦截")
	}
}

func itoaDeg(i int) string {
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
