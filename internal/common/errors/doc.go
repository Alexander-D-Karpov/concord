// Package errors defines AppError, the application error type that carries a gRPC
// status code.
//
// Because AppError implements GRPCStatus, returning one directly from a gRPC
// handler yields the correct status code without extra mapping. Use the
// constructors (NotFound, Unauthorized, Forbidden, BadRequest, Conflict, Internal)
// at boundaries and the IsNotFound/IsConflict predicates to branch on kind.
package errors
