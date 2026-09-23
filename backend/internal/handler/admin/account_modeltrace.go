package admin

import (
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

func (h *AccountHandler) DetectModelTrace(c *gin.Context) {
	if h.accountTestService == nil {
		response.Error(c, http.StatusServiceUnavailable, "Account test service unavailable")
		return
	}
	var request service.ModelTraceDetectionRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	if request.Scope != "all" && request.Scope != "selected" && request.Scope != "group" {
		response.BadRequest(c, "scope must be all, selected, or group")
		return
	}
	if request.Scope == "selected" {
		if len(request.AccountIDs) == 0 {
			response.BadRequest(c, "account_ids must contain at least one ID")
			return
		}
		for _, accountID := range request.AccountIDs {
			if accountID <= 0 {
				response.BadRequest(c, "account_ids must contain positive IDs")
				return
			}
		}
	}
	if request.Scope == "group" && request.GroupID <= 0 {
		response.BadRequest(c, "group_id must be a positive ID")
		return
	}
	result, err := h.accountTestService.DetectModelTrace(c.Request.Context(), request)
	if err != nil {
		response.Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	response.Success(c, result)
}
