package store

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestMeteringMigrationPreservesLiveAndArchivedUsage(t *testing.T) {
	dsn := os.Getenv("NEKOPASS_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("requires a dedicated PostgreSQL test database")
	}
	config, err := pgx.ParseConfig(dsn)
	if err != nil || !strings.HasSuffix(config.Database, "_test") {
		t.Fatal("refusing migration test outside a dedicated test database")
	}
	ctx := context.Background()
	conn, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	exec := func(sql string) {
		t.Helper()
		if _, err := tx.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	namespace := fmt.Sprintf("metering_migration_%d", time.Now().UnixNano())
	exec("CREATE SCHEMA " + namespace)
	exec("SET LOCAL search_path TO " + namespace)
	for _, sql := range []string{schema, migration002, migration003, migration004, migration005, migration006, migration007, migration008, migration009, migration010, migration011, migration012, migration013, migration014, migration015, migration016, migration017, migration018, migration019, migration020, migration021, migration022, migration023, migration024, migration025, migration026, migration027, migration028, migration029} {
		exec(sql)
	}
	exec(`INSERT INTO users(id,username,password_hash) VALUES(1,'migration@example.test','fixture');
UPDATE users SET traffic_base_bytes=321,quota_bytes=1000000 WHERE id=1;
INSERT INTO nodes(id,name,token_hash) VALUES(1,'migration','fixture');
INSERT INTO grants(node_id,user_id,issued,spent,traffic) VALUES(1,1,1000,500,123);
INSERT INTO deleted_nodes(id,name) VALUES(2,'archived');
INSERT INTO deleted_node_grants(node_id,user_id,issued,spent,traffic) VALUES(2,1,2000,1000,456);
INSERT INTO wallet_accounts(user_id,balance_cents) VALUES(1,4567);`)
	exec(migration030)
	var correct bool
	err = tx.QueryRow(ctx, `SELECT
 (SELECT ingress_rate=10000 AND egress_rate=0 FROM nodes WHERE id=1)
 AND (SELECT issued=1000 AND spent=500 AND traffic=123 AND charged_bytes=123 FROM grants WHERE node_id=1 AND user_id=1)
 AND (SELECT issued=2000 AND spent=1000 AND traffic=456 AND charged_bytes=456 FROM deleted_node_grants WHERE node_id=2 AND user_id=1)
 AND (SELECT traffic_base_bytes=321 AND actual_traffic_base_bytes=321 AND quota_bytes=1000000 AND NOT auto_renew_enabled FROM users WHERE id=1)
 AND (SELECT sum(traffic)=579 AND sum(charged_bytes)=579 FROM current_grants WHERE user_id=1)
 AND (SELECT balance_cents=4567 FROM wallet_accounts WHERE user_id=1)
 AND (SELECT max(version)=30 FROM schema_version)`).Scan(&correct)
	if err != nil || !correct {
		t.Fatal("migration changed old balances, grants or defaults", err)
	}
	// Raw transfers can exceed paid allowance on discounted/free routes;
	// weighted charge remains bounded by durable reservation.
	exec("UPDATE grants SET traffic=5000,charged_bytes=250 WHERE node_id=1")
}
