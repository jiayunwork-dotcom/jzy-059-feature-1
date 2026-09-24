package archive

import (
	"errors"
	"testing"

	"shipstability/internal/stability"
	"shipstability/internal/store"
)

func partialFuelTank() stability.Tank {
	return stability.Tank{
		Name:          "燃油舱",
		Length:        10,
		Width:         4,
		LiquidDensity: 850,
		Status:        stability.TankStatusPartial,
	}
}

// 液舱清单是档案的一部分：建档、取档、解析时连同液舱一起还原。
func TestService_TanksRoundTrip(t *testing.T) {
	svc := newTestService(t)
	c := validCondition("ship-tanks", 6.0)
	c.Tanks = []stability.Tank{
		partialFuelTank(),
		{Name: "压载泵舱", FreeSurfaceInertia: 60, LiquidDensity: 1025, Status: stability.TankStatusFull},
		{Length: 6, Width: 2, LiquidDensity: 1000, Status: stability.TankStatusEmpty},
	}
	if err := svc.Save(c); err != nil {
		t.Fatalf("带舱建档失败: %v", err)
	}
	got, err := svc.Get("ship-tanks")
	if err != nil {
		t.Fatalf("取档失败: %v", err)
	}
	if len(got.Tanks) != 3 {
		t.Fatalf("应还原 3 个液舱，得到 %d", len(got.Tanks))
	}
	if got.Tanks[0].Name != "燃油舱" || got.Tanks[0].Width != 4 ||
		got.Tanks[0].LiquidDensity != 850 || got.Tanks[0].Status != stability.TankStatusPartial {
		t.Fatalf("部分注液舱还原失真: %+v", got.Tanks[0])
	}
	if got.Tanks[1].FreeSurfaceInertia != 60 || got.Tanks[1].Status != stability.TankStatusFull {
		t.Fatalf("满舱（直给惯性矩）还原失真: %+v", got.Tanks[1])
	}

	p, tanks, err := svc.Resolve("ship-tanks")
	if err != nil {
		t.Fatalf("Resolve 失败: %v", err)
	}
	if len(tanks) != 3 {
		t.Fatalf("Resolve 应带 3 个舱，得到 %d", len(tanks))
	}
	r, err := stability.EvaluateLoaded(p, tanks, 5.0)
	if err != nil {
		t.Fatalf("带舱核算失败: %v", err)
	}
	// 只有燃油舱参与扣减（另两个为满舱/空舱）。
	wantI := 10.0 * 4 * 4 * 4 / 12.0
	wantDelta := 850.0 * wantI / (1025.0 * 1000.0)
	if mathDiff(r.FreeSurfaceCorrection, wantDelta) > 1e-9 {
		t.Fatalf("总扣减 = %v，期望 %v", r.FreeSurfaceCorrection, wantDelta)
	}
	solidGM := 1.2 + 5.0 - 6.0
	if mathDiff(r.SolidGM, solidGM) > 1e-9 {
		t.Fatalf("固体 GM = %v，期望 %v", r.SolidGM, solidGM)
	}

	// 删档后连同液舱一起消失。
	if err := svc.Delete("ship-tanks"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Get("ship-tanks"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("删档后应 NotFound，得到 %v", err)
	}
}

// 不带液舱的档案取回来不应凭空冒出液舱。
func TestService_NoTanksStaysEmpty(t *testing.T) {
	svc := newTestService(t)
	if err := svc.Save(validCondition("bare", 2.0)); err != nil {
		t.Fatal(err)
	}
	got, err := svc.Get("bare")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Tanks) != 0 {
		t.Fatalf("无舱档案不应出现液舱: %+v", got.Tanks)
	}
	_, tanks, err := svc.Resolve("bare")
	if err != nil {
		t.Fatal(err)
	}
	if len(tanks) != 0 {
		t.Fatalf("Resolve 无舱档案不应带舱: %+v", tanks)
	}
}

// 建档时液舱参数先过校验：不合规直接拦下，并指出舱位与字段。
func TestService_RejectsBadTanks(t *testing.T) {
	svc := newTestService(t)
	badCases := []struct {
		name string
		tank stability.Tank
	}{
		{"zero-width", stability.Tank{Name: "t", Length: 10, Width: 0, LiquidDensity: 850, Status: stability.TankStatusPartial}},
		{"neg-density", stability.Tank{Name: "t", Length: 10, Width: 4, LiquidDensity: -1, Status: stability.TankStatusPartial}},
		{"bad-status", stability.Tank{Name: "t", Length: 10, Width: 4, LiquidDensity: 850, Status: "weird"}},
		{"neg-inertia", stability.Tank{Name: "t", FreeSurfaceInertia: -2, LiquidDensity: 850, Status: stability.TankStatusPartial}},
		{"conflict", stability.Tank{Name: "t", Length: 10, Width: 4, FreeSurfaceInertia: 50, LiquidDensity: 850, Status: stability.TankStatusPartial}},
	}
	for _, bc := range badCases {
		t.Run(bc.name, func(t *testing.T) {
			c := validCondition("bad-tank", 2.0)
			c.Tanks = []stability.Tank{bc.tank}
			err := svc.Save(c)
			if err == nil {
				t.Fatalf("非法液舱（%s）建档应被拦截", bc.name)
			}
			if _, ok := err.(*stability.ValidationError); !ok {
				t.Fatalf("应是 *ValidationError，得到 %T: %v", err, err)
			}
			// 被拦下的档案不得落库。
			if _, err := svc.Get("bad-tank"); !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("被拦截的档案不应落库，得到 %v", err)
			}
		})
	}
}

// 不同档案的液舱互不串扰：A 船挂宽舱被扣到负，B 船无舱仍为正，
// 互相取档/核算都见不到对方的舱。
func TestService_TanksIsolatedAcrossConditions(t *testing.T) {
	svc := newTestService(t)

	shipA := validCondition("ship-a", 6.0) // 固体 GM = 0.2
	shipA.Tanks = []stability.Tank{
		{Name: "宽压载舱", Length: 20, Width: 10, LiquidDensity: 1000, Status: stability.TankStatusPartial},
	}
	if err := svc.Save(shipA); err != nil {
		t.Fatal(err)
	}
	if err := svc.Save(validCondition("ship-b", 6.0)); err != nil { // 同 KG 但无舱
		t.Fatal(err)
	}

	pA, tanksA, err := svc.Resolve("ship-a")
	if err != nil {
		t.Fatal(err)
	}
	rA, err := stability.EvaluateLoaded(pA, tanksA, 5.0)
	if err != nil {
		t.Fatal(err)
	}
	if rA.Stability != stability.StabilityNegative || len(tanksA) != 1 {
		t.Fatalf("ship-a 应带 1 舱且判负: label=%q tanks=%d", rA.Stability, len(tanksA))
	}

	pB, tanksB, err := svc.Resolve("ship-b")
	if err != nil {
		t.Fatal(err)
	}
	if len(tanksB) != 0 {
		t.Fatalf("ship-b 不应见到 ship-a 的舱: %+v", tanksB)
	}
	rB, err := stability.EvaluateLoaded(pB, tanksB, 5.0)
	if err != nil {
		t.Fatal(err)
	}
	if rB.Stability != stability.StabilityPositive || rB.FreeSurfaceCorrection != 0 {
		t.Fatalf("ship-b 应无扣减且正稳: label=%q δ=%v", rB.Stability, rB.FreeSurfaceCorrection)
	}

	// PUT 覆盖 ship-a：清空液舱后应恢复正稳性（舱随档案一起改）。
	updated := validCondition("ship-a", 6.0)
	if err := svc.Save(updated); err != nil {
		t.Fatal(err)
	}
	pA2, tanksA2, err := svc.Resolve("ship-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(tanksA2) != 0 {
		t.Fatalf("覆盖后液舱应清空: %+v", tanksA2)
	}
	rA2, _ := stability.EvaluateLoaded(pA2, tanksA2, 5.0)
	if rA2.Stability != stability.StabilityPositive {
		t.Fatalf("清舱覆盖后应恢复正稳，得到 %q", rA2.Stability)
	}
}
