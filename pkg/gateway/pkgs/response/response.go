package response

import (
	"github.com/gin-gonic/gin"
)

type ErrorCode uint

const (
	ServerError ErrorCode = iota
	FormError

	NoError
)

var HttpCode = map[ErrorCode]int{
	FormError:   400,
	ServerError: 500,
}

var Message = map[ErrorCode]string{
	ServerError: "Server Error",
	FormError:   "Form Error",
}

func SuccessResponse(c *gin.Context, data any) {
	c.JSON(200, gin.H{
		"msg":  "success",
		"code": 200,
		"data": data,
	})
}

func ErrorResponse(c *gin.Context, code ErrorCode) {
	httpStatus, ok := HttpCode[code]
	if !ok {
		httpStatus = 403
	}
	msg, ok := Message[code]
	if !ok {
		msg = "Unknown Error"
	}

	c.JSON(httpStatus, gin.H{
		"code": code,
		"msg":  msg,
	})
}
