package unfurl

import (
	"context"

	unfurlv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/unfurl/v1"
	"github.com/Alexander-D-Karpov/concord/internal/common/errors"
)

type Handler struct {
	unfurlv1.UnimplementedUnfurlServiceServer
	service *Service
}

func NewHandler(service *Service) *Handler {
	return &Handler{service: service}
}

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
