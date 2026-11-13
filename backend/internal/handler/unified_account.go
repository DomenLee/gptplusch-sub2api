package handler

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/identity"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	middleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

func unifiedResponse(c *gin.Context, value any, err error) {
	if err == nil {
		response.Success(c, value)
		return
	}
	var e *identity.Error
	if errors.As(err, &e) {
		c.JSON(e.Status, gin.H{"code": e.Status, "reason": e.Code, "message": e.Message})
		return
	}
	response.ErrorFrom(c, service.UnifiedError(err))
}
func (h *AuthHandler) RegisterUnifiedRoutes(v1 *gin.RouterGroup, jwt gin.HandlerFunc) {
	v1.GET("/auth/unified/config", func(c *gin.Context) { response.Success(c, gin.H{"enabled": h.authService.Unified != nil}) })
	v1.POST("/internal/unified/:action", h.internalUnified)
	v1.POST("/auth/unified/code", h.publicUnifiedCode)
	v1.POST("/auth/unified/recover", h.recoverUnified)
	v1.GET("/auth/unified/account", jwt, h.unifiedStatus)
	v1.POST("/auth/unified/account/:action", jwt, h.updateUnified)
}
func (h *AuthHandler) unifiedAvailable(c *gin.Context) bool {
	if h.authService.Unified == nil {
		c.JSON(404, gin.H{"code": 404, "message": "通用账号暂未启用"})
		return false
	}
	if err := h.authService.Unified.Rate(c.Request.Context(), "ip:"+c.ClientIP(), 60, time.Minute); err != nil {
		unifiedResponse(c, nil, err)
		return false
	}
	return true
}
func (h *AuthHandler) internalUnified(c *gin.Context) {
	if h.authService.Unified == nil {
		c.AbortWithStatus(404)
		return
	}
	s := h.authService.Unified
	if subtle.ConstantTimeCompare([]byte(c.GetHeader("X-Unified-Key")), []byte(s.Key)) != 1 {
		c.AbortWithStatus(401)
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 8192)
	var in identity.Input
	if c.ShouldBindJSON(&in) != nil {
		response.BadRequest(c, "请求无效")
		return
	}
	in.Site = "store"
	ctx := c.Request.Context()
	var v any
	var err error
	if err = s.Rate(ctx, "store:"+in.Email+":"+in.LocalID, 60, time.Minute); err != nil {
		unifiedResponse(c, nil, err)
		return
	}
	switch c.Param("action") {
	case "code":
		v, err = s.SendCode(ctx, in)
	case "register":
		v, err = s.Register(ctx, in)
	case "lookup":
		v, err = s.Lookup(ctx, in.Email)
	case "verify":
		v, err = s.Verify(ctx, in.Email, in.Password)
	case "attach":
		v, err = s.Attach(ctx, in)
	case "link":
		v, err = s.Link(ctx, in)
	case "linked":
		v, err = s.Linked(ctx, "store", in.LocalID)
	case "get":
		v, err = s.Get(ctx, in.AccountID)
	case "password":
		v, err = s.ChangePassword(ctx, in, false)
	case "recover":
		v, err = s.ChangePassword(ctx, in, true)
	case "email":
		v, err = s.ChangeEmail(ctx, in)
	case "status":
		v, err = s.Operation(ctx, "store", in.RequestID)
	default:
		c.AbortWithStatus(404)
		return
	}
	unifiedResponse(c, v, err)
}
func (h *AuthHandler) sendUnifiedRegisterCode(c *gin.Context, email string) {
	v, err := h.authService.Unified.SendCode(c.Request.Context(), identity.Input{Email: email, Purpose: "register", Site: "ai"})
	unifiedResponse(c, v, err)
}
func (h *AuthHandler) registerUnified(c *gin.Context, req RegisterRequest) {
	u, err := h.authService.RegisterUnified(c.Request.Context(), identity.Input{Email: req.Email, Password: req.Password, ChallengeID: req.ChallengeID, Code: req.VerifyCode, RequestID: req.RequestID, Site: "ai"}, req.PromoCode, req.InvitationCode, req.AffCode)
	if err != nil {
		unifiedResponse(c, nil, err)
		return
	}
	if u.TotpEnabled || u.Role == service.RoleAdmin {
		response.BadRequest(c, "该账号需二次验证，请返回登录页登录")
		return
	}
	h.respondWithTokenPair(c, u)
}
func (h *AuthHandler) publicUnifiedCode(c *gin.Context) {
	if !h.unifiedAvailable(c) {
		return
	}
	var in identity.Input
	if c.ShouldBindJSON(&in) != nil || in.Purpose != "recover" {
		response.BadRequest(c, "请求无效")
		return
	}
	v, err := h.authService.Unified.SendCode(c.Request.Context(), in)
	unifiedResponse(c, v, err)
}
func (h *AuthHandler) recoverUnified(c *gin.Context) {
	if !h.unifiedAvailable(c) {
		return
	}
	var in identity.Input
	if c.ShouldBindJSON(&in) != nil {
		response.BadRequest(c, "请求无效")
		return
	}
	in.Site = "ai"
	a, err := h.authService.Unified.ChangePassword(c.Request.Context(), in, true)
	// AI session revocation is committed atomically with the credential change.
	unifiedResponse(c, a, err)
}
func (h *AuthHandler) unifiedStatus(c *gin.Context) {
	if !h.unifiedAvailable(c) {
		return
	}
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok {
		c.AbortWithStatus(401)
		return
	}
	a, err := h.authService.Unified.Linked(c.Request.Context(), "ai", fmt.Sprint(subject.UserID))
	if err != nil {
		unifiedResponse(c, nil, err)
		return
	}
	u, err := h.userService.GetByID(c.Request.Context(), subject.UserID)
	if err != nil {
		unifiedResponse(c, nil, err)
		return
	}
	unifiedResponse(c, gin.H{"account": a, "requiresMfa": u.TotpEnabled}, nil)
}
func (h *AuthHandler) updateUnified(c *gin.Context) {
	if !h.unifiedAvailable(c) {
		return
	}
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok {
		c.AbortWithStatus(401)
		return
	}
	var req struct {
		identity.Input
		OldPassword string `json:"oldPassword"`
		TotpCode    string `json:"totpCode"`
	}
	if c.ShouldBindJSON(&req) != nil {
		response.BadRequest(c, "请求无效")
		return
	}
	ctx := c.Request.Context()
	s := h.authService.Unified
	req.Site = "ai"
	req.LocalID = fmt.Sprint(subject.UserID)
	u, err := h.userService.GetByID(ctx, subject.UserID)
	if err != nil {
		unifiedResponse(c, nil, err)
		return
	}
	action := c.Param("action")
	a, err := s.Linked(ctx, "ai", req.LocalID)
	if err != nil {
		unifiedResponse(c, nil, err)
		return
	}
	if action == "code" {
		if req.Purpose != "link" && req.Purpose != "email-old" && req.Purpose != "email-new" {
			response.BadRequest(c, "请求无效")
			return
		}
		if req.Purpose == "email-old" {
			if a == nil {
				response.BadRequest(c, "请先关联账号")
				return
			}
			req.Email = a.Email
		}
		v, e := s.SendCode(ctx, req.Input)
		unifiedResponse(c, v, e)
		return
	}
	if u.TotpEnabled && action != "logout-all" {
		if h.totpService == nil {
			response.BadRequest(c, "二次验证不可用")
			return
		}
		if err = h.totpService.VerifyCode(ctx, u.ID, req.TotpCode); err != nil {
			unifiedResponse(c, nil, err)
			return
		}
	}
	var v any
	if action == "link" {
		v, err = h.authService.LinkUnified(ctx, u.ID, req.OldPassword, req.Input)
	} else {
		if a == nil {
			response.BadRequest(c, "请先关联通用账号")
			return
		}
		req.AccountID = a.ID
		switch action {
		case "password":
			req.Email = a.Email
			v, err = s.ChangePassword(ctx, req.Input, false)
		case "email":
			v, err = s.ChangeEmail(ctx, req.Input)
		case "logout-all":
			err = h.authService.RevokeAllUserTokens(ctx, u.ID)
		default:
			c.AbortWithStatus(404)
			return
		}
	}
	// Credential changes revoke only this site's sessions; the other site remains untouched.
	if err == nil && (action == "password" || action == "email") {
		_ = h.authService.RevokeAllUserSessions(ctx, u.ID)
	}
	unifiedResponse(c, v, err)
}
