-- The users.status column stores a user's status *preference*; the service layer
-- treats an "offline" preference as an absolute override, so a new account whose
-- preference defaulted to 'offline' (migrations 002/006) appeared offline forever,
-- even after connecting. Default new users to 'online' so live presence surfaces.
ALTER TABLE users
    ALTER COLUMN status SET DEFAULT 'online';
