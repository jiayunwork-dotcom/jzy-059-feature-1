// Package archive 在持久化存储之上管理「按名字存取的装载状态档」，
// 负责名字合法性、浮态参数校验，以及服务启动时预置矩形驳船算例。
package archive

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"shipstability/internal/stability"
	"shipstability/internal/store"
)

// 预置矩形驳船算例的档名。
const RectangularBargeName = "rectangular-barge"

// 名字只允许字母、数字、下划线与连字符，长度 1~64，避免路径穿越等问题。
var namePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// Condition 是业务层使用的装载状态档视图。
type Condition struct {
	Name               string  `json:"name"`
	DisplacementVolume float64 `json:"displacementVolume"`
	KB                 float64 `json:"kb"`
	KG                 float64 `json:"kg"`
	TransverseInertia  float64 `json:"transverseInertia"`
	WaterDensity       float64 `json:"waterDensity"`
	// Tanks 随这条船一起存档的液舱清单；空列表与 nil 等价（不携带液舱）。
	Tanks []stability.Tank `json:"tanks,omitempty"`
}

// ValidateName 校验档名。
func ValidateName(name string) error {
	if !namePattern.MatchString(name) {
		return fmt.Errorf("装载状态档名只能包含字母、数字、下划线与连字符，长度 1~64，收到 %q", name)
	}
	return nil
}

// ToParams 转为稳性内核参数（应用默认水密度后返回）。
func (c Condition) ToParams() (stability.Params, error) {
	p := stability.Params{
		DisplacementVolume: c.DisplacementVolume,
		KB:                 c.KB,
		KG:                 c.KG,
		TransverseInertia:  c.TransverseInertia,
		WaterDensity:       c.WaterDensity,
	}
	np, err := p.Normalize()
	if err != nil {
		return stability.Params{}, err
	}
	if err := np.Validate(); err != nil {
		return stability.Params{}, err
	}
	return np, nil
}

func toTankRecords(tanks []stability.Tank) []store.TankRecord {
	if len(tanks) == 0 {
		return nil
	}
	out := make([]store.TankRecord, len(tanks))
	for i, t := range tanks {
		out[i] = store.TankRecord{
			Name:               t.Name,
			Length:             t.Length,
			Width:              t.Width,
			FreeSurfaceInertia: t.FreeSurfaceInertia,
			LiquidDensity:      t.LiquidDensity,
			Status:             t.Status,
		}
	}
	return out
}

func fromTankRecords(recs []store.TankRecord) []stability.Tank {
	if len(recs) == 0 {
		return nil
	}
	out := make([]stability.Tank, len(recs))
	for i, r := range recs {
		out[i] = stability.Tank{
			Name:               r.Name,
			Length:             r.Length,
			Width:              r.Width,
			FreeSurfaceInertia: r.FreeSurfaceInertia,
			LiquidDensity:      r.LiquidDensity,
			Status:             r.Status,
		}
	}
	return out
}

func toRecord(c Condition) store.Record {
	return store.Record{
		Name:               c.Name,
		DisplacementVolume: c.DisplacementVolume,
		KB:                 c.KB,
		KG:                 c.KG,
		TransverseInertia:  c.TransverseInertia,
		WaterDensity:       c.WaterDensity,
		Tanks:              toTankRecords(c.Tanks),
	}
}

func fromRecord(r store.Record) Condition {
	return Condition{
		Name:               r.Name,
		DisplacementVolume: r.DisplacementVolume,
		KB:                 r.KB,
		KG:                 r.KG,
		TransverseInertia:  r.TransverseInertia,
		WaterDensity:       r.WaterDensity,
		Tanks:              fromTankRecords(r.Tanks),
	}
}

// Service 是装载状态档管理服务。
type Service struct {
	store store.Store
}

// NewService 基于给定存储创建档案服务。
func NewService(s store.Store) *Service {
	return &Service{store: s}
}

// Save 校验名字、浮态参数与全部液舱后建档/覆盖。
func (svc *Service) Save(c Condition) error {
	if err := ValidateName(strings.TrimSpace(c.Name)); err != nil {
		return err
	}
	if _, err := c.ToParams(); err != nil {
		return err
	}
	// 液舱参数在落库之前逐舱过校验：几何/惯性矩须为正、密度须为正、
	// 注液状态须在允许集合里。
	if err := stability.ValidateTanks(c.Tanks); err != nil {
		return err
	}
	return svc.store.Put(toRecord(c))
}

// Get 按名取档；不存在返回 store.ErrNotFound（包 errors.Is 可判）。
func (svc *Service) Get(name string) (Condition, error) {
	if err := ValidateName(strings.TrimSpace(name)); err != nil {
		return Condition{}, err
	}
	rec, err := svc.store.Get(name)
	if err != nil {
		return Condition{}, err
	}
	return fromRecord(rec), nil
}

// Delete 按名删档。
func (svc *Service) Delete(name string) error {
	if err := ValidateName(strings.TrimSpace(name)); err != nil {
		return err
	}
	return svc.store.Delete(name)
}

// List 列出全部档案。
func (svc *Service) List() ([]Condition, error) {
	recs, err := svc.store.List()
	if err != nil {
		return nil, err
	}
	out := make([]Condition, 0, len(recs))
	for _, rec := range recs {
		out = append(out, fromRecord(rec))
	}
	return out, nil
}

// ResolveParams 按名取档并转成内核参数；参数在落库时已校验过，
// 这里仍再次规整以应用默认水密度。
func (svc *Service) ResolveParams(name string) (stability.Params, error) {
	p, _, err := svc.Resolve(name)
	return p, err
}

// Resolve 按名取档，连同液舱清单一起还原，供带自由液面修正的核算使用。
func (svc *Service) Resolve(name string) (stability.Params, []stability.Tank, error) {
	c, err := svc.Get(name)
	if err != nil {
		return stability.Params{}, nil, err
	}
	p, err := c.ToParams()
	if err != nil {
		return stability.Params{}, nil, err
	}
	return p, c.Tanks, nil
}

// SeedDefaults 幂等预置内置算例：档案已存在则保留用户数据不动。
func (svc *Service) SeedDefaults() error {
	if _, err := svc.store.Get(RectangularBargeName); err == nil {
		return nil // 已预置过，保持现状
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}

	// 矩形驳船：B=12m, L=40m, d=2.5m, KG=2.0m。
	//   ∇ = 12·40·2.5 = 1200 m^3
	//   KB = d/2 = 1.25 m
	//   IT = B³L/12 = 1728·40/12 = 5760 m^4
	//   BM = IT/∇ = 4.8 m，GM = 1.25 + 4.8 − 2.0 = 4.05 m（正稳性）
	barge := stability.BargeGeometry{Beam: 12, Length: 40, Draft: 2.5, KG: 2.0}
	p, err := barge.ToParams()
	if err != nil {
		return err
	}
	c := Condition{
		Name:               RectangularBargeName,
		DisplacementVolume: p.DisplacementVolume,
		KB:                 p.KB,
		KG:                 p.KG,
		TransverseInertia:  p.TransverseInertia,
		WaterDensity:       p.WaterDensity,
	}
	return svc.Save(c)
}
