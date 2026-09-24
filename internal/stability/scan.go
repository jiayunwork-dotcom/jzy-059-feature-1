package stability

import (
	"encoding/json"
	"fmt"
	"math"
)

// MaxScanPoints 单次扫描允许返回的最大点数，防止步长过小拖垮服务。
const MaxScanPoints = 10000

// ScanParams 描述横倾角扫描区间，角度均以「度」计。
type ScanParams struct {
	Params
	// Tanks 随船携带的液舱清单；为空时按纯固体核算，与从前完全一致。
	// 非空时整条 GZ 曲线都建立在自由液面修正后的有效 GM 之上。
	Tanks []Tank `json:"tanks,omitempty"`
	// StartDeg 起始横倾角（度）。
	StartDeg float64 `json:"startDeg"`
	// EndDeg 终止横倾角（度），须不小于 StartDeg。
	EndDeg float64 `json:"endDeg"`
	// StepDeg 采样步长（度），必须为正。
	StepDeg float64 `json:"stepDeg"`
}

// CurvePoint 是复原力臂曲线上的一个采样点，全部由公式实算得到。
type CurvePoint struct {
	AngleDeg   float64 `json:"angleDeg"`
	AngleRad   float64 `json:"angleRad"`
	GZ         float64 `json:"gz"`         // 复原力臂 (m)
	Moment     float64 `json:"moment"`     // 复原力矩 (N·m)
	SmallAngle bool    `json:"smallAngle"` // 该点是否仍在 10° 小倾角范围内
}

// ScanResult 是一次扫描的完整结果。
type ScanResult struct {
	BM        float64      `json:"bm"`
	GM        float64      `json:"gm"`
	Stability string       `json:"stability"`
	Points    []CurvePoint `json:"points"`

	// 自由液面修正层字段：仅在请求附带液舱时由 MarshalJSON 展开。
	// GM 始终是曲线实际采用的值——带液舱时即有效 GM（与 EffectiveGM 相同）。
	SolidGM               float64          `json:"-"`
	FreeSurfaceCorrection float64          `json:"-"`
	EffectiveGM           float64          `json:"-"`
	TankCorrections       []TankCorrection `json:"-"` // nil 表示本次扫描不含液舱

	// Warning 非空时表示扫描区间越过了小倾角范围；每个超界点
	// 的 smallAngle 字段同时置 false，可逐点判断。
	Warning string `json:"warning,omitempty"`
}

// MarshalJSON 与单点结果保持同样的约定：无液舱时响应与旧版逐字段一致；
// 携带液舱时展开三笔账与逐舱明细，临界（有效 GM≈0）也不丢字段。
func (s ScanResult) MarshalJSON() ([]byte, error) {
	type plain ScanResult
	out := struct {
		plain
		SolidGM               *float64          `json:"solidGm,omitempty"`
		FreeSurfaceCorrection *float64          `json:"freeSurfaceCorrection,omitempty"`
		EffectiveGM           *float64          `json:"effectiveGm,omitempty"`
		TankCorrections       *[]TankCorrection `json:"tankCorrections,omitempty"`
	}{plain: plain(s)}
	if s.TankCorrections != nil {
		solid, corr, eff := s.SolidGM, s.FreeSurfaceCorrection, s.EffectiveGM
		tanks := s.TankCorrections
		out.SolidGM, out.FreeSurfaceCorrection, out.EffectiveGM = &solid, &corr, &eff
		out.TankCorrections = &tanks
	}
	return json.Marshal(out)
}

// Validate 校验扫描参数：浮态参数须合法，区间与步长也须自洽。
func (s ScanParams) Validate() error {
	np, err := s.Params.Normalize()
	if err != nil {
		return err
	}
	if err := np.Validate(); err != nil {
		return err
	}
	if err := ValidateTanks(s.Tanks); err != nil {
		return err
	}
	if !finite(s.StartDeg) || !finite(s.EndDeg) || !finite(s.StepDeg) {
		return validationError("扫描区间的起点、终点与步长必须是有限数值（度）")
	}
	if s.StepDeg <= 0 {
		return validationError("扫描步长必须为正（度），收到 %v", s.StepDeg)
	}
	if s.EndDeg < s.StartDeg {
		return validationError("扫描终止角 %v° 小于起始角 %v°", s.EndDeg, s.StartDeg)
	}
	count := 1 + int(math.Floor((s.EndDeg-s.StartDeg)/s.StepDeg+1e-9))
	if count > MaxScanPoints {
		return validationError("扫描点数 %d 超过上限 %d，请放大步长或缩小区间", count, MaxScanPoints)
	}
	return nil
}

// Scan 在 [StartDeg, EndDeg] 上按固定步长采样，逐点调用 EvaluateAt
// 实算 GZ 与复原力矩。每个点都是独立公式计算，不存在预置曲线。
func Scan(s ScanParams) (ScanResult, error) {
	if err := s.Validate(); err != nil {
		return ScanResult{}, err
	}
	np, _ := s.Params.Normalize()

	// 修正与角度无关：先在起始角算一次，头部的 GM/判定以及逐舱
	// 扣减明细全部取自这一结果，曲线各点再按各自角度重算 GZ。
	first, err := EvaluateLoading(s.Params, s.Tanks, s.StartDeg)
	if err != nil {
		return ScanResult{}, err
	}
	out := ScanResult{
		BM:        first.BM,
		GM:        first.GM,
		Stability: first.Stability,
	}
	if len(s.Tanks) > 0 {
		// 带液舱时头部把三笔账分开写明：固体 GM、总扣减、有效 GM。
		out.SolidGM = first.SolidGM
		out.FreeSurfaceCorrection = first.FreeSurfaceCorrection
		out.EffectiveGM = first.EffectiveGM
		out.TankCorrections = first.TankCorrections
	}

	count := 1 + int(math.Floor((s.EndDeg-s.StartDeg)/s.StepDeg+1e-9))
	out.Points = make([]CurvePoint, 0, count)

	rangeExceedsSmall := false
	for i := 0; i < count; i++ {
		angle := s.StartDeg + float64(i)*s.StepDeg
		// 修正浮点漂移：理论末点直接取 EndDeg，保证终止角一定被采到。
		if i == count-1 && math.Abs(angle-s.EndDeg) < 1e-9*math.Max(1, math.Abs(s.EndDeg)) {
			angle = s.EndDeg
		}
		r, err := EvaluateLoading(np, s.Tanks, angle)
		if err != nil {
			return ScanResult{}, err
		}
		if !r.SmallAngle {
			rangeExceedsSmall = true
		}
		out.Points = append(out.Points, CurvePoint{
			AngleDeg:   angle,
			AngleRad:   r.AngleRad,
			GZ:         r.GZ,
			Moment:     r.RightingMoment,
			SmallAngle: r.SmallAngle,
		})
	}

	if rangeExceedsSmall {
		out.Warning = fmt.Sprintf(
			"扫描区间 [%.3f°, %.3f°] 越过小倾角近似适用范围（%g° 以内）；"+
				"超出部分的 GZ = GM·sinφ 结果仅供参考",
			s.StartDeg, s.EndDeg, SmallAngleLimitDeg)
	}
	return out, nil
}
