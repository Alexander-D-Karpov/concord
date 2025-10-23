package rooms

import (
	"context"

	commonv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/common/v1"
	roomsv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/rooms/v1"
	"github.com/Alexander-D-Karpov/concord/internal/common/errors"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type Handler struct {
	roomsv1.UnimplementedRoomsServiceServer
	service *Service
}

func NewHandler(service *Service) *Handler {
	return &Handler{
		service: service,
	}
}

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

	room, err := h.service.CreateRoom(ctx, req.Name, voiceServerID, region)
	if err != nil {
		return nil, errors.ToGRPCError(err)
	}

	return toProtoRoom(room), nil
}

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

func (h *Handler) DeleteRoom(ctx context.Context, req *roomsv1.DeleteRoomRequest) (*roomsv1.EmptyResponse, error) {
	return &roomsv1.EmptyResponse{}, nil
}

func toProtoRoom(room *Room) *commonv1.Room {
	protoRoom := &commonv1.Room{
		Id:        room.ID.String(),
		Name:      room.Name,
		CreatedBy: room.CreatedBy.String(),
		CreatedAt: timestamppb.New(room.CreatedAt),
	}

	if room.VoiceServerID != nil {
		protoRoom.VoiceServerId = room.VoiceServerID.String()
	}

	if room.Region != nil {
		protoRoom.Region = *room.Region
	}

	return protoRoom
}
