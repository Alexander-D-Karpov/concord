package errors

import (
	"errors"
	"fmt"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Sentinel errors wrapped by the AppError constructors below. Callers compare
// against them with errors.Is to classify a failure independently of its
// message.
var (
	// ErrNotFound marks a missing resource (maps to gRPC NotFound).
	ErrNotFound = errors.New("resource not found")
	// ErrUnauthorized marks a missing or unauthenticated caller (gRPC Unauthenticated).
	ErrUnauthorized = errors.New("unauthorized")
	// ErrForbidden marks an authenticated caller lacking permission (gRPC PermissionDenied).
	ErrForbidden = errors.New("forbidden")
	// ErrBadRequest marks invalid input (gRPC InvalidArgument).
	ErrBadRequest = errors.New("bad request")
	// ErrConflict marks a state conflict such as a duplicate (gRPC AlreadyExists).
	ErrConflict = errors.New("conflict")
	// ErrInternalError marks an unexpected server-side failure (gRPC Internal).
	ErrInternalError = errors.New("internal error")
	// ErrInvalidCredentials marks a failed login (wrong username/password).
	ErrInvalidCredentials = errors.New("invalid credentials")
	// ErrTokenExpired marks an authentication token past its expiry.
	ErrTokenExpired = errors.New("token expired")
	// ErrInvalidToken marks a malformed or unverifiable authentication token.
	ErrInvalidToken = errors.New("invalid token")
	// ErrTooManyRequests marks a rate-limited or locked-out caller (gRPC ResourceExhausted).
	ErrTooManyRequests = errors.New("too many requests")
)

// AppError is an application error carrying a gRPC status Code, a human-readable
// Message, and an optional wrapped Err (usually one of the sentinels above). It
// implements error, Unwrap, and GRPCStatus so it bridges cleanly to gRPC.
type AppError struct {
	Code    codes.Code
	Message string
	Err     error
}

// Error returns "Message: wrapped" when a cause is present, otherwise just
// Message.
func (e *AppError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %v", e.Message, e.Err)
	}
	return e.Message
}

// Unwrap returns the wrapped cause so errors.Is/As can traverse to the sentinel.
func (e *AppError) Unwrap() error {
	return e.Err
}

// GRPCStatus returns a gRPC *status.Status carrying the error's Code and
// Message. This lets gRPC transmit the correct status code to clients; the
// wrapped Err is intentionally not exposed over the wire.
func (e *AppError) GRPCStatus() *status.Status {
	return status.New(e.Code, e.Message)
}

// NewAppError constructs an AppError with an explicit code, message, and
// wrapped cause.
func NewAppError(code codes.Code, message string, err error) *AppError {
	return &AppError{
		Code:    code,
		Message: message,
		Err:     err,
	}
}

// NotFound returns a NotFound AppError wrapping ErrNotFound.
func NotFound(message string) *AppError {
	return &AppError{
		Code:    codes.NotFound,
		Message: message,
		Err:     ErrNotFound,
	}
}

// Unauthorized returns an Unauthenticated AppError wrapping ErrUnauthorized.
func Unauthorized(message string) *AppError {
	return &AppError{
		Code:    codes.Unauthenticated,
		Message: message,
		Err:     ErrUnauthorized,
	}
}

// Forbidden returns a PermissionDenied AppError wrapping ErrForbidden.
func Forbidden(message string) *AppError {
	return &AppError{
		Code:    codes.PermissionDenied,
		Message: message,
		Err:     ErrForbidden,
	}
}

// BadRequest returns an InvalidArgument AppError wrapping ErrBadRequest.
func BadRequest(message string) *AppError {
	return &AppError{
		Code:    codes.InvalidArgument,
		Message: message,
		Err:     ErrBadRequest,
	}
}

// Conflict returns an AlreadyExists AppError wrapping ErrConflict.
func Conflict(message string) *AppError {
	return &AppError{
		Code:    codes.AlreadyExists,
		Message: message,
		Err:     ErrConflict,
	}
}

// TooManyRequests returns a ResourceExhausted AppError wrapping ErrTooManyRequests,
// used for rate-limited or locked-out callers.
func TooManyRequests(message string) *AppError {
	return &AppError{
		Code:    codes.ResourceExhausted,
		Message: message,
		Err:     ErrTooManyRequests,
	}
}

// Internal returns an Internal AppError wrapping err. The underlying err is kept
// for logging but not surfaced to clients via GRPCStatus.
func Internal(message string, err error) *AppError {
	return &AppError{
		Code:    codes.Internal,
		Message: message,
		Err:     err,
	}
}

// ToGRPCError converts any error into a gRPC status error. An AppError (anywhere
// in the chain) yields its GRPCStatus; an error already carrying a gRPC status
// is passed through; anything else becomes a generic Internal status. Returns
// nil for a nil error.
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

// IsNotFound reports whether err represents a not-found condition, checking (in
// order) an AppError's Code/wrapped sentinel, a gRPC status code, and finally
// errors.Is against ErrNotFound. Returns false for nil.
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

// IsConflict reports whether err represents a conflict/already-exists condition,
// checking an AppError's Code/wrapped sentinel, a gRPC status code, and
// errors.Is against ErrConflict. Returns false for nil.
func IsConflict(err error) bool {
	if err == nil {
		return false
	}
	var appErr *AppError
	if errors.As(err, &appErr) {
		return appErr.Code == codes.AlreadyExists || errors.Is(appErr.Err, ErrConflict)
	}
	if st, ok := status.FromError(err); ok {
		return st.Code() == codes.AlreadyExists
	}
	return errors.Is(err, ErrConflict)
}

// IsTooManyRequests reports whether err represents a rate-limited/locked-out
// condition, checking an AppError's Code/wrapped sentinel, a gRPC status code, and
// errors.Is against ErrTooManyRequests. Returns false for nil.
func IsTooManyRequests(err error) bool {
	if err == nil {
		return false
	}
	var appErr *AppError
	if errors.As(err, &appErr) {
		return appErr.Code == codes.ResourceExhausted || errors.Is(appErr.Err, ErrTooManyRequests)
	}
	if st, ok := status.FromError(err); ok {
		return st.Code() == codes.ResourceExhausted
	}
	return errors.Is(err, ErrTooManyRequests)
}
