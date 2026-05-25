package middleware

import (
	"errors"
	"net/http"

	"df-build-server/pkg/logger"
	"df-build-server/pkg/response"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// AppError is a typed error with a stable error code and user-facing message.
type AppError struct {
	Code    int
	Message string
}

func (e *AppError) Error() string { return e.Message }

// NewAppError constructs an AppError.
func NewAppError(code int, message string) *AppError {
	return &AppError{Code: code, Message: message}
}

// Predefined errors used across services.
var (
	ErrNotFound         = NewAppError(40001, "资源不存在")
	ErrInvalidParam     = NewAppError(40002, "参数错误")
	ErrDuplicate        = NewAppError(40003, "资源已存在")
	ErrUnauthorized     = NewAppError(40101, "未授权")
	ErrForbidden        = NewAppError(40301, "无权限")
	ErrInternal         = NewAppError(50001, "系统内部错误")
	ErrDatabaseFailure  = NewAppError(50002, "数据库错误")
)

// ErrorHandler catches errors propagated via c.Error(err) and formats the response.
func ErrorHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Next()

		if len(c.Errors) == 0 {
			return
		}

		// Only the last error is returned to the client, all logged
		last := c.Errors.Last()

		// Map known error types to response
		var appErr *AppError
		if errors.As(last.Err, &appErr) {
			logger.Log.Warnf("[AppError %d] %s %s: %s", appErr.Code, c.Request.Method, c.Request.URL.Path, appErr.Message)
			response.Fail(c, appErr.Code, appErr.Message)
			return
		}

		if errors.Is(last.Err, gorm.ErrRecordNotFound) {
			response.Fail(c, ErrNotFound.Code, ErrNotFound.Message)
			return
		}

		// Unknown error: log stack and return generic 500
		logger.Log.Errorf("[InternalError] %s %s: %v", c.Request.Method, c.Request.URL.Path, last.Err)
		c.JSON(http.StatusInternalServerError, response.Response{
			Code:    ErrInternal.Code,
			Message: ErrInternal.Message,
		})
	}
}