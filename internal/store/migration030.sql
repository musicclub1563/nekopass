ALTER TABLE nodes ADD COLUMN ingress_rate integer NOT NULL DEFAULT 10000 CHECK(ingress_rate BETWEEN 0 AND 10000000);
ALTER TABLE nodes ADD COLUMN egress_rate integer NOT NULL DEFAULT 0 CHECK(egress_rate BETWEEN 0 AND 10000000);
ALTER TABLE users ADD COLUMN actual_traffic_base_bytes bigint NOT NULL DEFAULT 0 CHECK(actual_traffic_base_bytes>=0);
UPDATE users SET actual_traffic_base_bytes=traffic_base_bytes;
ALTER TABLE users ADD COLUMN auto_renew_enabled boolean NOT NULL DEFAULT false;
ALTER TABLE users ADD COLUMN auto_renew_cycle text NOT NULL DEFAULT '' CHECK(auto_renew_cycle IN ('','monthly','quarterly','semiannual','annual','biennial','triennial'));
ALTER TABLE users ADD COLUMN auto_renew_price_cents bigint NOT NULL DEFAULT 0 CHECK(auto_renew_price_cents BETWEEN 0 AND 100000000000);
ALTER TABLE users ADD COLUMN auto_renew_status text NOT NULL DEFAULT 'off';
ALTER TABLE users ADD COLUMN auto_renew_error text NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN auto_renew_next_attempt timestamptz NOT NULL DEFAULT 'epoch';
ALTER TABLE users ADD COLUMN auto_renew_last_attempt timestamptz;
ALTER TABLE users ADD COLUMN auto_renew_last_success timestamptz;
CREATE INDEX users_auto_renew_due ON users(auto_renew_next_attempt) WHERE auto_renew_enabled;
ALTER TABLE grants ADD COLUMN charged_bytes bigint NOT NULL DEFAULT 0;
ALTER TABLE deleted_node_grants ADD COLUMN charged_bytes bigint NOT NULL DEFAULT 0;
UPDATE grants SET charged_bytes=traffic;
UPDATE deleted_node_grants SET charged_bytes=traffic;
DO $$ DECLARE c record; BEGIN
 FOR c IN SELECT conrelid::regclass AS tbl,conname FROM pg_constraint WHERE conrelid IN ('grants'::regclass,'deleted_node_grants'::regclass) AND contype='c' AND pg_get_constraintdef(oid) LIKE '%traffic%' LOOP
  EXECUTE format('ALTER TABLE %s DROP CONSTRAINT %I',c.tbl,c.conname);
 END LOOP;
END $$;
ALTER TABLE grants ADD CHECK(spent>=0 AND released>=0 AND traffic>=0 AND unlimited_spent>=0 AND spent::numeric+released<=issued AND charged_bytes>=0 AND charged_bytes::numeric<=spent::numeric+unlimited_spent);
ALTER TABLE deleted_node_grants ADD CHECK(spent>=0 AND released>=0 AND traffic>=0 AND unlimited_spent>=0 AND spent::numeric+released<=issued AND charged_bytes>=0 AND charged_bytes::numeric<=spent::numeric+unlimited_spent);
CREATE OR REPLACE VIEW current_grants AS
 SELECT g.* FROM grants g JOIN users u ON u.id=g.user_id AND u.quota_epoch=g.quota_epoch
 UNION ALL SELECT g.* FROM deleted_node_grants g JOIN users u ON u.id=g.user_id AND u.quota_epoch=g.quota_epoch;
CREATE OR REPLACE VIEW user_entitlements AS
 SELECT u.id,u.username,u.is_admin,u.enabled AS account_enabled,u.plan_id,u.plan_started_at,u.created_at,
 u.enabled AND COALESCE(p.enabled,false) AS enabled,COALESCE(p.enabled,false) AS plan_enabled,COALESCE(p.name,'未分配套餐') AS plan_name,
 CASE WHEN u.subscription_managed THEN u.subscription_expires_at ELSE u.resource_expires_at END AS expires_at,
 u.speed_bps,u.quota_bytes,u.max_rules,u.max_connections,u.ip_limit,u.rule_speed_bps,u.rule_ip_limit,u.rule_connection_limit,
 u.quota_epoch,u.next_reset_at,u.subscription_managed,u.traffic_base_bytes,u.resources_revision,u.actual_traffic_base_bytes
 FROM users u LEFT JOIN plans p ON p.id=u.plan_id;
CREATE FUNCTION clear_auto_renew_on_plan_change() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF NEW.plan_id IS DISTINCT FROM OLD.plan_id THEN NEW.auto_renew_enabled=false;NEW.auto_renew_status='off';NEW.auto_renew_error=''; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER clear_auto_renew_on_plan_change BEFORE UPDATE OF plan_id ON users FOR EACH ROW EXECUTE FUNCTION clear_auto_renew_on_plan_change();
UPDATE revision SET value=value+1;
INSERT INTO schema_version VALUES(30);
