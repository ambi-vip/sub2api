package admin

import (
	"encoding/json"
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// SetOpenAIGatewayService attaches the gateway service that owns the codex
// ticket harvester, enabling on-demand account re-probes.
func (h *AccountHandler) SetOpenAIGatewayService(svc *service.OpenAIGatewayService) {
	h.openAIGatewayService = svc
}

// RefreshOpenAICodexTickets handles POST /admin/accounts/:id/codex-tickets/refresh.
// Optional body {"models": [...]} limits the probe to specific gated models;
// omitting it probes every gated model for the account.
func (h *AccountHandler) RefreshOpenAICodexTickets(c *gin.Context) {
	if h == nil || h.openAIGatewayService == nil {
		response.ErrorFrom(c, service.ErrOpenAICodexTicketRefreshUnavailable)
		return
	}
	accountID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || accountID <= 0 {
		response.BadRequest(c, "Invalid account ID")
		return
	}
	var req struct {
		Models []string `json:"models"`
	}
	if c.Request != nil && c.Request.Body != nil {
		raw, readErr := c.GetRawData()
		if readErr != nil {
			response.BadRequest(c, "Invalid request body")
			return
		}
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &req); err != nil {
				response.BadRequest(c, "Invalid request body")
				return
			}
		}
	}
	statuses, err := h.openAIGatewayService.RefreshOpenAICodexTicketsForAccount(c.Request.Context(), accountID, req.Models)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"codex_turn_tickets": statuses})
}
