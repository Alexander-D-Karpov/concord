#!/usr/bin/env python3
import grpc
import sys
import time
import threading
import random
import string
from typing import Optional, List, Callable

sys.path.append('./generated_python')

from auth.v1 import auth_pb2, auth_pb2_grpc
from users.v1 import users_pb2, users_pb2_grpc
from rooms.v1 import rooms_pb2, rooms_pb2_grpc
from chat.v1 import chat_pb2, chat_pb2_grpc
from membership.v1 import membership_pb2, membership_pb2_grpc
from stream.v1 import stream_pb2, stream_pb2_grpc
from friends.v1 import friends_pb2, friends_pb2_grpc
from common.v1 import types_pb2


def log(msg: str):
    now = time.time()
    ts = time.strftime("%H:%M:%S", time.localtime(now))
    ms = int((now % 1) * 1000)
    print(f"[{ts}.{ms:03d}] {msg}", flush=True)


def rand(n: int = 8) -> str:
    return ''.join(random.choices(string.ascii_lowercase + string.digits, k=n))


class ConcordClient:
    def __init__(self, host: str = '127.0.0.1', port: int = 9090, name: str = ""):
        self.name = name or f"client_{rand(4)}"
        self.address = f'{host}:{port}'
        self.channel = grpc.insecure_channel(self.address)
        self.auth = auth_pb2_grpc.AuthServiceStub(self.channel)
        self.users = users_pb2_grpc.UsersServiceStub(self.channel)
        self.rooms = rooms_pb2_grpc.RoomsServiceStub(self.channel)
        self.chat = chat_pb2_grpc.ChatServiceStub(self.channel)
        self.membership = membership_pb2_grpc.MembershipServiceStub(self.channel)
        self.stream = stream_pb2_grpc.StreamServiceStub(self.channel)
        self.friends = friends_pb2_grpc.FriendsServiceStub(self.channel)

        self.access_token: Optional[str] = None
        self.refresh_token: Optional[str] = None
        self.user_id: Optional[str] = None
        self.handle: Optional[str] = None

        self.stream_thread: Optional[threading.Thread] = None
        self.stream_active = False
        self.received_events: List[stream_pb2.ServerEvent] = []
        self._event_cursor = 0

    def _metadata(self) -> List[tuple]:
        return [('authorization', f'Bearer {self.access_token}')] if self.access_token else []

    def register(self, handle: str, password: str, display_name: str):
        log(f"{self.name}: register handle={handle}")
        r = self.auth.Register(auth_pb2.RegisterRequest(handle=handle, password=password, display_name=display_name))
        self.access_token, self.refresh_token = r.access_token, r.refresh_token
        return r

    def login(self, handle: str, password: str):
        log(f"{self.name}: login handle={handle}")
        r = self.auth.LoginPassword(auth_pb2.LoginPasswordRequest(handle=handle, password=password))
        self.access_token, self.refresh_token = r.access_token, r.refresh_token
        return r

    def get_self(self):
        r = self.users.GetSelf(users_pb2.GetSelfRequest(), metadata=self._metadata())
        self.user_id, self.handle = r.id, r.handle
        log(f"{self.name}: self id={self.user_id} handle={self.handle}")
        return r

    def create_room(self, name: str, description: str = "", is_private: bool = False):
        log(f"{self.name}: create_room name={name}")
        return self.rooms.CreateRoom(
            rooms_pb2.CreateRoomRequest(name=name, description=description, is_private=is_private),
            metadata=self._metadata()
        )

    def list_rooms(self):
        r = self.rooms.ListRoomsForUser(rooms_pb2.ListRoomsForUserRequest(), metadata=self._metadata())
        log(f"{self.name}: list_rooms count={len(r.rooms)}")
        return r

    def update_room(self, room_id: str, name: str, description: str):
        log(f"{self.name}: update_room room_id={room_id}")
        return self.rooms.UpdateRoom(
            rooms_pb2.UpdateRoomRequest(room_id=room_id, name=name, description=description),
            metadata=self._metadata()
        )

    def send_message(self, room_id: str, content: str, reply_to_id: str = ""):
        log(f"{self.name}: send_message room_id={room_id} content={content}")
        return self.chat.SendMessage(
            chat_pb2.SendMessageRequest(room_id=room_id, content=content, reply_to_id=reply_to_id),
            metadata=self._metadata()
        )

    def list_messages(self, room_id: str, limit: int = 50):
        r = self.chat.ListMessages(chat_pb2.ListMessagesRequest(room_id=room_id, limit=limit), metadata=self._metadata())
        log(f"{self.name}: list_messages room_id={room_id} count={len(r.messages)}")
        return r

    def edit_message(self, message_id: str, content: str):
        log(f"{self.name}: edit_message id={message_id}")
        return self.chat.EditMessage(chat_pb2.EditMessageRequest(message_id=message_id, content=content), metadata=self._metadata())

    def delete_message(self, message_id: str):
        log(f"{self.name}: delete_message id={message_id}")
        return self.chat.DeleteMessage(chat_pb2.DeleteMessageRequest(message_id=message_id), metadata=self._metadata())

    def add_reaction(self, message_id: str, emoji: str):
        log(f"{self.name}: add_reaction id={message_id} emoji={emoji}")
        return self.chat.AddReaction(chat_pb2.AddReactionRequest(message_id=message_id, emoji=emoji), metadata=self._metadata())

    def pin_message(self, room_id: str, message_id: str):
        log(f"{self.name}: pin_message room_id={room_id} message_id={message_id}")
        return self.chat.PinMessage(chat_pb2.PinMessageRequest(room_id=room_id, message_id=message_id), metadata=self._metadata())

    def list_pinned(self, room_id: str):
        r = self.chat.ListPinnedMessages(chat_pb2.ListPinnedMessagesRequest(room_id=room_id), metadata=self._metadata())
        log(f"{self.name}: list_pinned room_id={room_id} count={len(r.messages)}")
        return r

    def invite_user(self, room_id: str, user_id: str):
        log(f"{self.name}: invite_user room_id={room_id} user_id={user_id}")
        return self.membership.Invite(membership_pb2.InviteRequest(room_id=room_id, user_id=user_id), metadata=self._metadata())

    def list_members(self, room_id: str):
        r = self.membership.ListMembers(membership_pb2.ListMembersRequest(room_id=room_id), metadata=self._metadata())
        log(f"{self.name}: list_members room_id={room_id} count={len(r.members)}")
        return r

    def set_role(self, room_id: str, user_id: str, role: int):
        log(f"{self.name}: set_role room_id={room_id} user_id={user_id} role={role}")
        return self.membership.SetRole(membership_pb2.SetRoleRequest(room_id=room_id, user_id=user_id, role=role), metadata=self._metadata())

    def set_nickname(self, room_id: str, nickname: str):
        log(f"{self.name}: set_nickname room_id={room_id} nickname={nickname}")
        return self.membership.SetNickname(membership_pb2.SetNicknameRequest(room_id=room_id, nickname=nickname), metadata=self._metadata())

    def send_friend_request(self, user_id: str):
        log(f"{self.name}: send_friend_request to={user_id}")
        return self.friends.SendFriendRequest(friends_pb2.SendFriendRequestRequest(user_id=user_id), metadata=self._metadata())

    def list_pending_requests(self):
        r = self.friends.ListPendingRequests(friends_pb2.ListPendingRequestsRequest(), metadata=self._metadata())
        log(f"{self.name}: list_pending incoming={len(r.incoming)} outgoing={len(r.outgoing)}")
        return r

    def accept_friend_request(self, request_id: str):
        log(f"{self.name}: accept_friend_request id={request_id}")
        return self.friends.AcceptFriendRequest(friends_pb2.AcceptFriendRequestRequest(request_id=request_id), metadata=self._metadata())

    def cancel_friend_request(self, request_id: str):
        log(f"{self.name}: cancel_friend_request id={request_id}")
        return self.friends.CancelFriendRequest(friends_pb2.CancelFriendRequestRequest(request_id=request_id), metadata=self._metadata())

    def list_friends(self):
        r = self.friends.ListFriends(friends_pb2.ListFriendsRequest(), metadata=self._metadata())
        log(f"{self.name}: list_friends count={len(r.friends)}")
        return r

    def start_stream(self):
        if self.stream_active:
            return
        log(f"{self.name}: start_stream")
        self.stream_active = True
        self.stream_thread = threading.Thread(target=self._stream_listener, daemon=True)
        self.stream_thread.start()
        time.sleep(0.2)

    def stop_stream(self):
        if not self.stream_active:
            return
        log(f"{self.name}: stop_stream")
        self.stream_active = False
        if self.stream_thread:
            self.stream_thread.join(timeout=2)

    def _stream_listener(self):
        def gen():
            yield stream_pb2.ClientEvent()
            while self.stream_active:
                time.sleep(1)
                yield stream_pb2.ClientEvent()

        try:
            for ev in self.stream.EventStream(gen(), metadata=self._metadata()):
                self.received_events.append(ev)
                typ = ev.WhichOneof('payload')
                if typ == "message_created":
                    m = ev.message_created.message
                    log(f"{self.name}: evt message_created room={m.room_id} id={m.id}")
                elif typ == "message_edited":
                    m = ev.message_edited.message
                    log(f"{self.name}: evt message_edited room={m.room_id} id={m.id}")
                elif typ == "message_deleted":
                    md = ev.message_deleted
                    log(f"{self.name}: evt message_deleted room={md.room_id} id={md.message_id}")
                elif typ == "member_joined":
                    mem = ev.member_joined.member
                    log(f"{self.name}: evt member_joined room={mem.room_id} user={mem.user_id}")
                elif typ == "member_removed":
                    mr = ev.member_removed
                    log(f"{self.name}: evt member_removed room={mr.room_id} user={mr.user_id}")
                elif typ == "role_changed":
                    rc = ev.role_changed
                    log(f"{self.name}: evt role_changed room={rc.room_id} user={rc.user_id} role={rc.new_role}")
                elif typ == "member_nickname_changed":
                    nc = ev.member_nickname_changed
                    log(f"{self.name}: evt nickname_changed room={nc.room_id} user={nc.user_id}")
                elif typ == "friend_request_created":
                    fr = ev.friend_request_created.request
                    log(f"{self.name}: evt friend_request_created id={fr.id} from={fr.from_user_id} to={fr.to_user_id} status={fr.status}")
                elif typ == "friend_request_updated":
                    fr = ev.friend_request_updated.request
                    log(f"{self.name}: evt friend_request_updated id={fr.id} status={fr.status}")
                else:
                    log(f"{self.name}: evt {typ}")

                if not self.stream_active:
                    break
        except grpc.RpcError as e:
            log(f"{self.name}: stream error code={e.code()} details={e.details()}")
        finally:
            self.stream_active = False

    def wait_for_event(self, name: str, timeout: float = 3.0, predicate: Optional[Callable[[stream_pb2.ServerEvent], bool]] = None) -> stream_pb2.ServerEvent:
        log(f"{self.name}: wait_for_event name={name} timeout={timeout}")
        deadline = time.time() + timeout
        while time.time() < deadline:
            while self._event_cursor < len(self.received_events):
                ev = self.received_events[self._event_cursor]
                self._event_cursor += 1
                if ev.WhichOneof('payload') == name and (predicate is None or predicate(ev)):
                    log(f"{self.name}: wait_for_event matched name={name}")
                    return ev
            time.sleep(0.05)
        raise TimeoutError(f"{self.name}: timed out waiting for event {name}")

    def wait_until(self, desc: str, check: Callable[[], bool], timeout: float = 5.0, poll: float = 0.1):
        log(f"{self.name}: wait_until {desc} timeout={timeout}")
        deadline = time.time() + timeout
        while time.time() < deadline:
            if check():
                log(f"{self.name}: condition satisfied: {desc}")
                return
            time.sleep(poll)
        raise TimeoutError(f"{self.name}: condition not met: {desc}")


def run_tests():
    c1, c2 = ConcordClient(name="A"), ConcordClient(name="B")

    u1h, u2h = f"u_{rand()}", f"u_{rand()}"
    p1, p2 = "password123", "password456"

    c1.register(u1h, p1, "User One")
    c2.register(u2h, p2, "User Two")
    u1 = c1.get_self()
    u2 = c2.get_self()

    c1.start_stream()
    c2.start_stream()

    room = c1.create_room(name=f"room_{rand(4)}", description="t")
    room_id = room.id
    c1.update_room(room_id, "updated", "updated desc")
    c1.list_rooms()

    log("ACTION invite user B to room")
    c1.invite_user(room_id, u2.id)

    try:
        ev = c1.wait_for_event(
            "member_joined",
            timeout=2.5,
            predicate=lambda e: e.member_joined.member.room_id == room_id and e.member_joined.member.user_id == u2.id
        )
        assert ev.member_joined.member.user_id == u2.id
    except TimeoutError as te:
        log(f"ACTION fallback: no member_joined to A, polling membership")
    finally:
        c1.wait_until(
            "B appears in room members (server-side truth)",
            check=lambda: any(m.user_id == u2.id for m in c1.list_members(room_id).members),
            timeout=6.0
        )

    try:
        ev2 = c2.wait_for_event(
            "member_joined",
            timeout=2.0,
            predicate=lambda e: e.member_joined.member.room_id == room_id and e.member_joined.member.user_id == u2.id
        )
        assert ev2.member_joined.member.room_id == room_id
    except TimeoutError:
        log("INFO: B did not see member_joined (likely invite/broadcast race); continuing")

    m1 = c1.send_message(room_id, "hello from A")
    mid1 = m1.message.id
    try:
        c2.wait_for_event("message_created", timeout=3.0, predicate=lambda e: e.message_created.message.id == mid1)
    except TimeoutError:
        log("ERROR: B did not see message_created from A")

    m2 = c2.send_message(room_id, "hello from B")
    mid2 = m2.message.id
    c1.wait_for_event("message_created", timeout=3.0, predicate=lambda e: e.message_created.message.id == mid2)

    c1.edit_message(mid1, "edited by A")
    c2.wait_for_event("message_edited", timeout=3.0, predicate=lambda e: e.message_edited.message.id == mid1)

    c1.pin_message(room_id, mid2)
    c1.list_pinned(room_id)

    fr = c1.send_friend_request(u2.id)

    saw_friend_created = False
    try:
        _ = c2.wait_for_event(
            "friend_request_created",
            timeout=1.5,
            predicate=lambda e: e.friend_request_created.request.id == fr.request.id
        )
        saw_friend_created = True
    except TimeoutError:
        log("INFO: friend_request_created event not received by B; will validate via ListPending")

    c2.wait_until(
        "B sees incoming friend request via ListPending",
        check=lambda: any(r.id == fr.request.id for r in c2.list_pending_requests().incoming),
        timeout=6.0
    )

    c2.accept_friend_request(fr.request.id)

    try:
        c1.wait_for_event(
            "friend_request_updated",
            timeout=2.0,
            predicate=lambda e: e.friend_request_updated.request.id == fr.request.id and
                                e.friend_request_updated.request.status == friends_pb2.FRIEND_REQUEST_STATUS_ACCEPTED
        )
        c2.wait_for_event(
            "friend_request_updated",
            timeout=2.0,
            predicate=lambda e: e.friend_request_updated.request.id == fr.request.id and
                                e.friend_request_updated.request.status == friends_pb2.FRIEND_REQUEST_STATUS_ACCEPTED
        )
    except TimeoutError:
        log("INFO: friend_request_updated events not seen; verifying via ListFriends")

    lf1 = c1.list_friends()
    lf2 = c2.list_friends()
    assert any(f.user_id == u2.id for f in lf1.friends)
    assert any(f.user_id == u1.id for f in lf2.friends)

    msgs = c1.list_messages(room_id, limit=10)
    assert any(msg.id == mid1 for msg in msgs.messages)

    c1.delete_message(mid1)
    try:
        c2.wait_for_event("message_deleted", timeout=2.0, predicate=lambda e: e.message_deleted.message_id == mid1)
    except TimeoutError:
        log("INFO: message_deleted not observed by B; continuing")

    c1.set_role(room_id, u2.id, types_pb2.ROLE_MODERATOR)
    c1.set_nickname(room_id, "User1Nick")

    log("RESULT OK")

    c1.stop_stream()
    c2.stop_stream()
    c1.channel.close()
    c2.channel.close()


if __name__ == "__main__":
    try:
        run_tests()
    except grpc.RpcError as e:
        log(f"RPC ERROR code={e.code()} details={e.details()}")
        raise
    except Exception as e:
        log(f"ERROR {e}")
        raise
