ALTER TABLE user_inventory
    ADD COLUMN role_status TEXT NOT NULL DEFAULT 'pending'
    CHECK (role_status IN ('pending', 'fulfilled'));
ALTER TABLE user_inventory
    ADD COLUMN role_id TEXT NOT NULL DEFAULT '';
