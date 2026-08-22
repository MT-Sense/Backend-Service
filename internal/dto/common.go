// Package dto holds the wire format: every request body the API accepts and every
// response body it returns.
//
// It exists so the contract with the Vue frontend is stated once, in typed structs the
// compiler checks, instead of being scattered across handlers as fiber.Map literals. It
// also keeps persistence out of the wire: GORM models are never serialized directly, so
// adding a column cannot silently start publishing it.
//
// Field names here mirror Web-Frontend/src/types/*.ts one for one. Changing a json tag in
// this package is a breaking API change.
package dto

import "github.com/mt-sense/backend-service/internal/models"

// Localized and Suppressible are reused from models rather than redeclared: they are value
// types whose JSON shape is already exactly what the frontend's LocalizedText and
// Suppressible<T> expect, and a parallel copy would only be something to keep in sync.
type (
	Localized = models.Localized
	Sentiment = models.SentimentSplit
)

// Suppressible re-exported for readability at DTO field sites.
type Suppressible[T any] = models.Suppressible[T]

// ErrorResponse is the single error shape the API returns, produced by the Fiber error
// handler in cmd/server.
type ErrorResponse struct {
	Error string `json:"error"`
}

// ValidationErrors reports everything wrong with a submitted form at once, rather than
// failing on the first problem — HR editing a survey should see the full list.
type ValidationErrors struct {
	Errors []string `json:"errors"`
}
