package unfurl

import (
	"context"

	unfurlv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/unfurl/v1"
	"github.com/Alexander-D-Karpov/concord/internal/common/errors"
)

// Handler adapts the gRPC UnfurlService surface onto the internal Service.
type Handler struct {
	unfurlv1.UnimplementedUnfurlServiceServer
	service *Service
}

// NewHandler wires a Handler to the given Service.
func NewHandler(service *Service) *Handler {
	return &Handler{service: service}
}

// Unfurl fetches the requested URL and returns its link preview. It rejects an
// empty URL as BadRequest and maps any fetch/parse failure to a BadRequest whose
// message wraps the underlying error, so upstream failures surface as client
// errors rather than Internal.
func (h *Handler) Unfurl(ctx context.Context, req *unfurlv1.UnfurlRequest) (*unfurlv1.UnfurlResponse, error) {
	if req.Url == "" {
		return nil, errors.ToGRPCError(errors.BadRequest("url is required"))
	}

	preview, err := h.service.Unfurl(ctx, req.Url)
	if err != nil {
		return nil, errors.ToGRPCError(errors.BadRequest("failed to unfurl: " + err.Error()))
	}

	return &unfurlv1.UnfurlResponse{
		Url:         preview.URL,
		Title:       preview.Title,
		Description: preview.Description,
		Image:       preview.Image,
		SiteName:    preview.SiteName,
		Favicon:     preview.Favicon,
	}, nil
}
