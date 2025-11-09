package errors

import (
	"errors"
	"fmt"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var (
	ErrNotFound           = errors.New("resource not found")
	ErrUnauthorized       = errors.New("unauthorized")
	ErrForbidden          = errors.New("forbidden")
	ErrBadRequest         = errors.New("bad request")
	ErrConflict           = errors.New("conflict")
	ErrInternalError      = errors.New("internal error")
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrTokenExpired       = errors.New("token expired")
	ErrInvalidToken       = errors.New("invalid token")
)

type AppError struct {
	Code    codes.Code
	Message string
	Err     error
}

func (e *AppError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %v", e.Message, e.Err)
	}
	return e.Message
}

func (e *AppError) Unwrap() error {
	return e.Err
}

func (e *AppError) GRPCStatus() *status.Status {
	return status.New(e.Code, e.Message)
}

func NewAppError(code codes.Code, message string, err error) *AppError {
	return &AppError{
		Code:    code,
		Message: message,
		Err:     err,
	}
}

func NotFound(message string) *AppError {
	return &AppError{
		Code:    codes.NotFound,
		Message: message,
		Err:     ErrNotFound,
	}
}

func Unauthorized(message string) *AppError {
	return &AppError{
		Code:    codes.Unauthenticated,
		Message: message,
		Err:     ErrUnauthorized,
	}
}

func Forbidden(message string) *AppError {
	return &AppError{
		Code:    codes.PermissionDenied,
		Message: message,
		Err:     ErrForbidden,
	}
}

func BadRequest(message string) *AppError {
	return &AppError{
		Code:    codes.InvalidArgument,
		Message: message,
		Err:     ErrBadRequest,
	}
}

func Conflict(message string) *AppError {
	return &AppError{
		Code:    codes.AlreadyExists,
		Message: message,
		Err:     ErrConflict,
	}
}

func Internal(message string, err error) *AppError {
	return &AppError{
		Code:    codes.Internal,
		Message: message,
		Err:     err,
	}
}

func ToGRPCError(err error) error {
	if err == nil {
		return nil
	}

	var appErr *AppError
	if errors.As(err, &appErr) {
		return appErr.GRPCStatus().Err()
	}

	if st, ok := status.FromError(err); ok {
		return st.Err()
	}

	return status.Error(codes.Internal, err.Error())
}

func IsNotFound(err error) bool {
	if err == nil {
		return false
	}

	var appErr *AppError
	if errors.As(err, &appErr) {
		if appErr.Code == codes.NotFound {
			return true
		}
		return errors.Is(appErr.Err, ErrNotFound)
	}

	if st, ok := status.FromError(err); ok {
		return st.Code() == codes.NotFound
	}

	return errors.Is(err, ErrNotFound)
}
