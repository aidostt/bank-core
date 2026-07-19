package http

import (
	"errors"
	"strings"

	"github.com/aidostt/bank-core/pkg/apperr"
	"github.com/aidostt/bank-core/pkg/logging"
	"github.com/gin-gonic/gin"
)

// Problem is the RFC 9457 body with a stable machine `code` (ADR-0018).
type Problem struct {
	Type      string `json:"type"`
	Title     string `json:"title"`
	Status    int    `json:"status"`
	Code      string `json:"code"`
	Detail    string `json:"detail,omitempty"`
	Instance  string `json:"instance,omitempty"`
	RequestID string `json:"request_id,omitempty"`
}

// writeProblem is the single gRPC/app error → HTTP mapping (tested).
func writeProblem(c *gin.Context, err error) {
	var ae *apperr.Error
	if !errors.As(err, &ae) {
		ae = apperr.FromGRPC(err)
	}
	status := apperr.HTTPStatus(ae.Code)
	detail := ae.Message
	if ae.Code == apperr.CodeInternal {
		// Never leak internals; the request id lets support correlate logs.
		detail = "internal error"
	}
	if status == 429 {
		c.Header("Retry-After", "1")
	}
	c.Header("Content-Type", "application/problem+json")
	c.AbortWithStatusJSON(status, Problem{
		Type:      "https://bank-core.dev/errors/" + strings.ToLower(string(ae.Code)),
		Title:     apperr.HTTPTitle(ae.Code),
		Status:    status,
		Code:      string(ae.Code),
		Detail:    detail,
		Instance:  c.Request.URL.Path,
		RequestID: logging.RequestID(c.Request.Context()),
	})
}
