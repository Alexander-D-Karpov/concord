package rooms

import (
	"context"

	commonv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/common/v1"
	roomsv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/rooms/v1"
	"github.com/Alexander-D-Karpov/concord/internal/common/errors"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Handler is the RoomsService gRPC server, a thin translation layer over the
// rooms Service.
type Handler struct {
	roomsv1.UnimplementedRoomsServiceServer
	service *Service
}

// NewHandler constructs the RoomsService handler.
func NewHandler(service *Service) *Handler {
	return &Handler{
		service: service,
	}
}

// CreateRoom handles the CreateRoom RPC, requiring a name; empty voice-server and
// region fields are passed through as nil (unset) to the service.
func (h *Handler) CreateRoom(ctx context.Context, req *roomsv1.CreateRoomRequest) (*commonv1.Room, error) {
	if req.Name == "" {
		return nil, errors.ToGRPCError(errors.BadRequest("name is required"))
	}

	var voiceServerID *string
	if req.VoiceServerId != "" {
		voiceServerID = &req.VoiceServerId
	}

	var region *string
	if req.Region != "" {
		region = &req.Region
	}

	room, err := h.service.CreateRoom(ctx, req.Name, voiceServerID, region, req.Description, req.IsPrivate)
	if err != nil {
		return nil, errors.ToGRPCError(err)
	}

	return toProtoRoom(room), nil
}

// GetRoom handles the GetRoom RPC, returning a room by id.
func (h *Handler) GetRoom(ctx context.Context, req *roomsv1.GetRoomRequest) (*commonv1.Room, error) {
	if req.RoomId == "" {
		return nil, errors.ToGRPCError(errors.BadRequest("room_id is required"))
	}

	room, err := h.service.GetRoom(ctx, req.RoomId)
	if err != nil {
		return nil, errors.ToGRPCError(err)
	}

	return toProtoRoom(room), nil
}

// UpdateRoom handles the UpdateRoom RPC. Each field arrives as an optional
// wrapper; only present wrappers become non-nil pointers, so absent fields are
// left unchanged by the service (admin-only enforcement lives there).
func (h *Handler) UpdateRoom(ctx context.Context, req *roomsv1.UpdateRoomRequest) (*commonv1.Room, error) {
	if req.RoomId == "" {
		return nil, errors.ToGRPCError(errors.BadRequest("room_id is required"))
	}

	var namePtr *string
	var descPtr *string
	var privatePtr *bool

	if req.Name != nil {
		v := req.Name.Value
		namePtr = &v
	}

	if req.Description != nil {
		v := req.Description.Value
		descPtr = &v
	}

	if req.IsPrivate != nil {
		v := req.IsPrivate.Value
		privatePtr = &v
	}

	room, err := h.service.UpdateRoom(ctx, req.RoomId, namePtr, descPtr, privatePtr)
	if err != nil {
		return nil, errors.ToGRPCError(err)
	}

	return toProtoRoom(room), nil
}

// DeleteRoom handles the DeleteRoom RPC (soft delete); admin-only enforcement
// lives in the service.
func (h *Handler) DeleteRoom(ctx context.Context, req *roomsv1.DeleteRoomRequest) (*roomsv1.EmptyResponse, error) {
	if req.RoomId == "" {
		return nil, errors.ToGRPCError(errors.BadRequest("room_id is required"))
	}

	if err := h.service.DeleteRoom(ctx, req.RoomId); err != nil {
		return nil, errors.ToGRPCError(err)
	}

	return &roomsv1.EmptyResponse{}, nil
}

// ListRoomsForUser handles the ListRoomsForUser RPC, returning the caller's
// rooms.
func (h *Handler) ListRoomsForUser(ctx context.Context, req *roomsv1.ListRoomsForUserRequest) (*roomsv1.ListRoomsForUserResponse, error) {
	rooms, err := h.service.ListRoomsForUser(ctx)
	if err != nil {
		return nil, errors.ToGRPCError(err)
	}

	protoRooms := make([]*commonv1.Room, len(rooms))
	for i, room := range rooms {
		protoRooms[i] = toProtoRoom(room)
	}

	return &roomsv1.ListRoomsForUserResponse{
		Rooms: protoRooms,
	}, nil
}

// AttachVoiceServer handles the AttachVoiceServer RPC; admin-only enforcement
// lives in the service.
func (h *Handler) AttachVoiceServer(ctx context.Context, req *roomsv1.AttachVoiceServerRequest) (*commonv1.Room, error) {
	if req.RoomId == "" {
		return nil, errors.ToGRPCError(errors.BadRequest("room_id is required"))
	}
	if req.VoiceServerId == "" {
		return nil, errors.ToGRPCError(errors.BadRequest("voice_server_id is required"))
	}

	room, err := h.service.AttachVoiceServer(ctx, req.RoomId, req.VoiceServerId)
	if err != nil {
		return nil, errors.ToGRPCError(err)
	}

	return toProtoRoom(room), nil
}

// toProtoRoom converts a domain Room to the wire commonv1.Room, emitting the
// optional voice-server, region, and description fields only when set.
func toProtoRoom(room *Room) *commonv1.Room {
	protoRoom := &commonv1.Room{
		Id:        room.ID.String(),
		Name:      room.Name,
		CreatedBy: room.CreatedBy.String(),
		CreatedAt: timestamppb.New(room.CreatedAt),
		IsPrivate: room.IsPrivate,
	}

	if room.VoiceServerID != nil {
		protoRoom.VoiceServerId = room.VoiceServerID.String()
	}

	if room.Region != "" {
		protoRoom.Region = room.Region
	}

	if room.Description != "" {
		protoRoom.Description = room.Description
	}

	return protoRoom
}
