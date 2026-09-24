// Package api 装配 HTTP 路由，把请求参数交给稳性内核与档案服务处理。
// 本服务只提供 JSON 接口，不含任何前端页面。
package api

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"shipstability/internal/archive"
	"shipstability/internal/stability"
	"shipstability/internal/store"
)

// Server 持有路由处理所需的依赖。
type Server struct {
	Conditions *archive.Service
}

// NewServer 创建 API 服务。
func NewServer(conds *archive.Service) *Server {
	return &Server{Conditions: conds}
}

// Router 装配全部路由，返回可供测试与 main 使用的 gin 引擎。
func (s *Server) Router() *gin.Engine {
	r := gin.New()
	r.Use(gin.Logger(), gin.Recovery())

	r.GET("/health", s.health)

	api := r.Group("/api/v1")
	{
		api.POST("/stability/evaluate", s.evaluate)
		api.POST("/stability/scan", s.scan)

		api.GET("/conditions", s.listConditions)
		api.POST("/conditions", s.createCondition)
		api.GET("/conditions/:name", s.getCondition)
		api.PUT("/conditions/:name", s.putCondition)
		api.DELETE("/conditions/:name", s.deleteCondition)
	}
	return r
}

func (s *Server) health(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok", "service": "ship-initial-stability"})
}

// ---- 请求 / 响应结构 ----

// paramsPayload 是浮态参数的请求体；水密度省略或为 0 时取默认海水密度。
type paramsPayload struct {
	DisplacementVolume float64 `json:"displacementVolume"`
	KB                 float64 `json:"kb"`
	KG                 float64 `json:"kg"`
	TransverseInertia  float64 `json:"transverseInertia"`
	WaterDensity       float64 `json:"waterDensity"`
}

// tankPayload 描述一个随船携带的液舱。
// 液面几何给法二选一：length+width（矩形液面）或直接给 freeSurfaceInertia。
type tankPayload struct {
	Name               string  `json:"name"`
	Length             float64 `json:"length"`
	Width              float64 `json:"width"`
	FreeSurfaceInertia float64 `json:"freeSurfaceInertia,omitempty"`
	LiquidDensity      float64 `json:"liquidDensity"`
	FillingStatus      string  `json:"fillingStatus"`
}

func toTanks(tps []tankPayload) []stability.Tank {
	tanks := make([]stability.Tank, 0, len(tps))
	for _, t := range tps {
		tanks = append(tanks, stability.Tank{
			Name:               t.Name,
			Length:             t.Length,
			Width:              t.Width,
			FreeSurfaceInertia: t.FreeSurfaceInertia,
			LiquidDensity:      t.LiquidDensity,
			FillingStatus:      t.FillingStatus,
		})
	}
	return tanks
}

func fromTanks(tanks []stability.Tank) []tankPayload {
	if len(tanks) == 0 {
		return nil // 无舱时序列化为字段缺省，而不是 []
	}
	out := make([]tankPayload, 0, len(tanks))
	for _, t := range tanks {
		out = append(out, tankPayload{
			Name:               t.Name,
			Length:             t.Length,
			Width:              t.Width,
			FreeSurfaceInertia: t.FreeSurfaceInertia,
			LiquidDensity:      t.LiquidDensity,
			FillingStatus:      t.FillingStatus,
		})
	}
	return out
}

// evaluateRequest 单点核算请求：可按已存档名引用，也可内联给出参数。
type evaluateRequest struct {
	ConditionName string         `json:"conditionName"`
	Params        *paramsPayload `json:"params"`
	// Tanks 内联/附加的液舱清单；省略或为空表示按纯固体核算。
	// 引用档名时，档案自带的液舱会与这里给出的清单合并。
	Tanks []tankPayload `json:"tanks"`
	// AngleDeg 横倾角（度）；省略时按正浮 0° 处理。
	AngleDeg float64 `json:"angleDeg"`
}

// scanRequest 复原力臂曲线扫描请求。
type scanRequest struct {
	ConditionName string         `json:"conditionName"`
	Params        *paramsPayload `json:"params"`
	Tanks         []tankPayload  `json:"tanks"`
	StartDeg      *float64       `json:"startDeg"`
	EndDeg        *float64       `json:"endDeg"`
	StepDeg       *float64       `json:"stepDeg"`
}

// conditionPayload 建档请求体（POST 时名字在体内，PUT 时名字取自路径）。
type conditionPayload struct {
	Name               string        `json:"name"`
	DisplacementVolume float64       `json:"displacementVolume"`
	KB                 float64       `json:"kb"`
	KG                 float64       `json:"kg"`
	TransverseInertia  float64       `json:"transverseInertia"`
	WaterDensity       float64       `json:"waterDensity"`
	Tanks              []tankPayload `json:"tanks"`
}

func toParams(p paramsPayload) stability.Params {
	return stability.Params{
		DisplacementVolume: p.DisplacementVolume,
		KB:                 p.KB,
		KG:                 p.KG,
		TransverseInertia:  p.TransverseInertia,
		WaterDensity:       p.WaterDensity,
	}
}

func fromCondition(c archive.Condition) conditionPayload {
	return conditionPayload{
		Name:               c.Name,
		DisplacementVolume: c.DisplacementVolume,
		KB:                 c.KB,
		KG:                 c.KG,
		TransverseInertia:  c.TransverseInertia,
		WaterDensity:       c.WaterDensity,
		Tanks:              fromTanks(c.Tanks),
	}
}

// conditionFromPayload 把建档请求体转为业务层档案；
// tankPayload 与 stability.Tank 字段同名但类型独立，不能直接整体转换。
func conditionFromPayload(p conditionPayload) archive.Condition {
	return archive.Condition{
		Name:               p.Name,
		DisplacementVolume: p.DisplacementVolume,
		KB:                 p.KB,
		KG:                 p.KG,
		TransverseInertia:  p.TransverseInertia,
		WaterDensity:       p.WaterDensity,
		Tanks:              toTanks(p.Tanks),
	}
}

// loading 是一次核算/扫描解析出的浮态参数与液舱清单。
type loading struct {
	params stability.Params
	tanks  []stability.Tank
}

// resolveLoading 在「引用档名」与「内联参数」两种方式中解析出浮态与液舱。
// 两者必须二选一：
//   - 引用档名：浮态与液舱都取自该份档案，档案里没有舱就不修正；
//   - 内联参数：浮态取 params，液舱取请求体里的 tanks。
//
// 同名引用各自独立取档，计算过程互不渗透。
func (s *Server) resolveLoading(c *gin.Context, name string, inline *paramsPayload, tanks []tankPayload) (loading, bool) {
	hasName := name != ""
	hasInline := inline != nil
	switch {
	case hasName && hasInline:
		badRequest(c, "conditionName 与 params 只能二选一，不能同时给出")
		return loading{}, false
	case hasName:
		if len(tanks) > 0 {
			badRequest(c, "引用档名时液舱以档案内登记的为准；要临时挂舱做试算请改用内联 params+tanks")
			return loading{}, false
		}
		p, t, err := s.Conditions.ResolveLoading(name)
		if err != nil {
			abortByError(c, err)
			return loading{}, false
		}
		return loading{params: p, tanks: t}, true
	case hasInline:
		return loading{params: toParams(*inline), tanks: toTanks(tanks)}, true
	default:
		badRequest(c, "必须给出 conditionName（引用已存档档名）或 params（内联浮态参数）")
		return loading{}, false
	}
}

// ---- 业务 handler ----

func (s *Server) evaluate(c *gin.Context) {
	var req evaluateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		badRequest(c, "请求体不是合法 JSON: "+err.Error())
		return
	}
	l, ok := s.resolveLoading(c, req.ConditionName, req.Params, req.Tanks)
	if !ok {
		return
	}

	result, err := stability.EvaluateLoading(l.params, l.tanks, req.AngleDeg)
	if err != nil {
		abortByError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"stability": result.Stability,
		"positive":  result.Stability == stability.StabilityPositive,
		"result":    result,
	})
}

func (s *Server) scan(c *gin.Context) {
	var req scanRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		badRequest(c, "请求体不是合法 JSON: "+err.Error())
		return
	}
	if req.StartDeg == nil || req.EndDeg == nil || req.StepDeg == nil {
		badRequest(c, "扫描必须显式给出 startDeg、endDeg 与 stepDeg（度）")
		return
	}
	l, ok := s.resolveLoading(c, req.ConditionName, req.Params, req.Tanks)
	if !ok {
		return
	}

	result, err := stability.Scan(stability.ScanParams{
		Params:   l.params,
		Tanks:    l.tanks,
		StartDeg: *req.StartDeg,
		EndDeg:   *req.EndDeg,
		StepDeg:  *req.StepDeg,
	})
	if err != nil {
		abortByError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (s *Server) listConditions(c *gin.Context) {
	conds, err := s.Conditions.List()
	if err != nil {
		abortByError(c, err)
		return
	}
	out := make([]conditionPayload, 0, len(conds))
	for _, cond := range conds {
		out = append(out, fromCondition(cond))
	}
	c.JSON(http.StatusOK, gin.H{"conditions": out})
}

func (s *Server) createCondition(c *gin.Context) {
	var payload conditionPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		badRequest(c, "请求体不是合法 JSON: "+err.Error())
		return
	}
	cond := conditionFromPayload(payload)
	if err := s.Conditions.Save(cond); err != nil {
		abortByError(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"condition": fromCondition(cond)})
}

func (s *Server) getCondition(c *gin.Context) {
	cond, err := s.Conditions.Get(c.Param("name"))
	if err != nil {
		abortByError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"condition": fromCondition(cond)})
}

// putCondition 按路径名字建档/覆盖（upsert），忽略体内的 name 字段。
func (s *Server) putCondition(c *gin.Context) {
	var payload conditionPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		badRequest(c, "请求体不是合法 JSON: "+err.Error())
		return
	}
	payload.Name = c.Param("name")
	cond := conditionFromPayload(payload)
	if err := s.Conditions.Save(cond); err != nil {
		abortByError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"condition": fromCondition(cond)})
}

func (s *Server) deleteCondition(c *gin.Context) {
	if err := s.Conditions.Delete(c.Param("name")); err != nil {
		abortByError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"deleted": c.Param("name")})
}

// ---- 错误处理 ----

func badRequest(c *gin.Context, reason string) {
	c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "invalid_parameters", "reason": reason})
}

// abortByError 把领域错误映射成 HTTP 状态：
// 参数/名字非法 → 400，档不存在 → 404，其余 → 500。
func abortByError(c *gin.Context, err error) {
	var ve *stability.ValidationError
	switch {
	case errors.As(err, &ve):
		badRequest(c, ve.Reason)
	case errors.Is(err, store.ErrNotFound):
		c.AbortWithStatusJSON(http.StatusNotFound, gin.H{"error": "not_found", "reason": err.Error()})
	default:
		// archive 的名字校验等普通参数错误也归 400。
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "invalid_parameters", "reason": err.Error()})
	}
}
