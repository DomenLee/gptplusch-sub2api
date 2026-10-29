package admin

import (
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/gin-gonic/gin"
)

func (h *SettingHandler) GetOpenAIAutoGroupSettings(c *gin.Context) {
	settings, err := h.settingService.GetOpenAIAutoGroupSettings(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, settings)
}

func (h *SettingHandler) UpdateOpenAIAutoGroupSettings(c *gin.Context) {
	settings, err := h.settingService.GetOpenAIAutoGroupSettings(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if err := c.ShouldBindJSON(settings); err != nil {
		response.BadRequest(c, "Invalid GPT auto-group settings: "+err.Error())
		return
	}
	if err := h.settingService.SetOpenAIAutoGroupSettings(c.Request.Context(), settings); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.Success(c, settings)
}
