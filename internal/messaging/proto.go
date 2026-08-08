package messaging

import (
	"strconv"

	commonv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/common/v1"
	dmv1 "github.com/Alexander-D-Karpov/concord/api/gen/go/dm/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ToCommonProto converts a Message to the shared commonv1.Message wire type used
// by the room surface. It returns nil for a nil input. The surface's scope ID
// (RoomID or ChannelID) is flattened into RoomId, forward metadata is emitted
// only when any forward field is set, and ReplyMentionAuthor is always sent as an
// explicit bool pointer.
func ToCommonProto(m *Message) *commonv1.Message {
	if m == nil {
		return nil
	}
	p := &commonv1.Message{
		Id:         strconv.FormatInt(m.ID, 10),
		AuthorId:   m.AuthorID.String(),
		Content:    m.Content,
		CreatedAt:  timestamppb.New(m.CreatedAt),
		ReplyCount: m.ReplyCount,
		Pinned:     m.Pinned,
		EditCount:  m.EditCount,
	}

	switch m.Surface {
	case SurfaceRoom:
		if m.RoomID != nil {
			p.RoomId = m.RoomID.String()
		}
	case SurfaceDM:
		if m.ChannelID != nil {
			p.RoomId = m.ChannelID.String()
		}
	}

	if m.EditedAt != nil {
		p.EditedAt = timestamppb.New(*m.EditedAt)
	}
	if m.DeletedAt != nil {
		p.Deleted = true
	}
	if m.ReplyToID != nil {
		p.ReplyToId = strconv.FormatInt(*m.ReplyToID, 10)
	}
	if m.MediaGroupID != nil {
		p.MediaGroupId = *m.MediaGroupID
	}
	if m.ReplyQuotedContent != nil {
		p.ReplyQuotedContent = *m.ReplyQuotedContent
	}
	rma := m.ReplyMentionAuthor
	p.ReplyMentionAuthor = &rma

	for _, a := range m.Attachments {
		p.Attachments = append(p.Attachments, &commonv1.MessageAttachment{
			Id:          a.ID.String(),
			Url:         a.URL,
			Filename:    a.Filename,
			ContentType: a.ContentType,
			Size:        a.Size,
			Width:       int32(a.Width),
			Height:      int32(a.Height),
			CreatedAt:   timestamppb.New(a.CreatedAt),
		})
	}
	for _, mention := range m.Mentions {
		p.Mentions = append(p.Mentions, mention.String())
	}
	for _, r := range m.Reactions {
		p.Reactions = append(p.Reactions, &commonv1.MessageReaction{
			Id:        r.ID.String(),
			MessageId: strconv.FormatInt(r.MessageID, 10),
			UserId:    r.UserID.String(),
			Emoji:     r.Emoji,
			CreatedAt: timestamppb.New(r.CreatedAt),
		})
	}

	if m.ForwardFromUserID != nil || m.ForwardFromUserName != nil ||
		m.ForwardFromRoomID != nil || m.ForwardFromChannelID != nil ||
		m.ForwardFromMsgID != nil || m.ForwardOriginalTS != nil {
		p.ForwardInfo = &commonv1.ForwardInfo{}
		if m.ForwardFromUserID != nil {
			p.ForwardInfo.OriginalAuthorId = m.ForwardFromUserID.String()
		}
		if m.ForwardFromUserName != nil {
			p.ForwardInfo.OriginalAuthorName = *m.ForwardFromUserName
		}
		if m.ForwardFromRoomID != nil {
			p.ForwardInfo.OriginalRoomId = m.ForwardFromRoomID.String()
		}
		if m.ForwardFromChannelID != nil {
			p.ForwardInfo.OriginalChannelId = m.ForwardFromChannelID.String()
		}
		if m.ForwardFromMsgID != nil {
			p.ForwardInfo.OriginalMessageId = strconv.FormatInt(*m.ForwardFromMsgID, 10)
		}
		if m.ForwardOriginalTS != nil {
			p.ForwardInfo.OriginalTimestamp = timestamppb.New(*m.ForwardOriginalTS)
		}
	}

	return p
}

// ToDMProto converts a Message to the DM-specific dmv1.DMMessage wire type,
// returning nil for a nil input. Unlike ToCommonProto it carries read receipts
// (ReadBy) and reports deletion via the Deleted flag rather than a timestamp.
func ToDMProto(m *Message) *dmv1.DMMessage {
	if m == nil {
		return nil
	}
	p := &dmv1.DMMessage{
		Id:        strconv.FormatInt(m.ID, 10),
		AuthorId:  m.AuthorID.String(),
		Content:   m.Content,
		CreatedAt: timestamppb.New(m.CreatedAt),
		Deleted:   m.DeletedAt != nil,
	}
	if m.ChannelID != nil {
		p.ChannelId = m.ChannelID.String()
	}
	if m.EditedAt != nil {
		p.EditedAt = timestamppb.New(*m.EditedAt)
	}
	if m.ReplyToID != nil {
		p.ReplyToId = strconv.FormatInt(*m.ReplyToID, 10)
	}
	for _, a := range m.Attachments {
		p.Attachments = append(p.Attachments, &dmv1.DMAttachment{
			Id:          a.ID.String(),
			Url:         a.URL,
			Filename:    a.Filename,
			ContentType: a.ContentType,
			Size:        a.Size,
			Width:       int32(a.Width),
			Height:      int32(a.Height),
		})
	}
	for _, r := range m.ReadBy {
		p.ReadBy = append(p.ReadBy, &dmv1.ReadReceipt{
			UserId: r.UserID.String(),
			ReadAt: timestamppb.New(r.ReadAt),
		})
	}
	return p
}
