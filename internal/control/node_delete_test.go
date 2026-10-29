package control

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestDeleteOfflineDisabledNodeDoesNotWaitForAgent(t *testing.T) {
	f := newSecurityFixture(t)
	ctx := context.Background()
	var node int64
	if err := f.p.QueryRow(ctx, `INSERT INTO nodes(name,token_hash,enabled,last_seen,applied_revision,config_revision)
 VALUES($1,$2,false,now()-interval '1 hour',1,100) RETURNING id`, Secret(), Hash(Secret())).Scan(&node); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.p.Exec(ctx, "DELETE FROM nodes WHERE id=$1", node) })
	path := fmt.Sprintf("admin/nodes/%d", node)
	securityStatus(t, f.request(f.user, path, "DELETE", nil), 403)
	securityStatus(t, f.request(f.user, path+"?force=true", "DELETE", nil), 403)
	securityStatus(t, f.request(f.admin, path+"?force=yes", "DELETE", nil), 400)
	securityStatus(t, f.request(f.admin, path, "DELETE", nil), 200)
	securityStatus(t, f.request(f.admin, path, "DELETE", nil), 404)
}

func TestDeleteOnlineNodeRequiresDisableAndAcknowledgement(t *testing.T) {
	f := newSecurityFixture(t)
	ctx := context.Background()
	var node int64
	if err := f.p.QueryRow(ctx, `INSERT INTO nodes(name,token_hash,last_seen,applied_revision,config_revision)
 VALUES($1,$2,now(),1,100) RETURNING id`, Secret(), Hash(Secret())).Scan(&node); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.p.Exec(ctx, "DELETE FROM nodes WHERE id=$1", node) })
	path := fmt.Sprintf("admin/nodes/%d", node)
	w := f.request(f.admin, path, "DELETE", nil)
	securityStatus(t, w, 409)
	if !strings.Contains(w.Body.String(), "请先停用") {
		t.Fatal("missing enabled-node explanation")
	}
	securityStatus(t, f.request(f.admin, path+"?force=true", "DELETE", nil), 409)
	if _, err := f.p.Exec(ctx, "UPDATE nodes SET enabled=false WHERE id=$1", node); err != nil {
		t.Fatal(err)
	}
	w = f.request(f.admin, path, "DELETE", nil)
	securityStatus(t, w, 409)
	if !strings.Contains(w.Body.String(), "仍在线") {
		t.Fatal("missing pending synchronization explanation")
	}
	securityStatus(t, f.request(f.admin, path+"?force=true", "DELETE", nil), 409)
	if _, err := f.p.Exec(ctx, "UPDATE nodes SET applied_revision=config_revision WHERE id=$1", node); err != nil {
		t.Fatal(err)
	}
	securityStatus(t, f.request(f.admin, path, "DELETE", nil), 200)
}

func TestDeleteNodeReferencedAsExitExplainsRuleDependency(t *testing.T) {
	f := newSecurityFixture(t)
	ctx := context.Background()
	var entry, exit int64
	for _, id := range []*int64{&entry, &exit} {
		if err := f.p.QueryRow(ctx, "INSERT INTO nodes(name,token_hash,enabled) VALUES($1,$2,false) RETURNING id", Secret(), Hash(Secret())).Scan(id); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		f.p.Exec(ctx, "DELETE FROM rules WHERE node_id=$1", entry)
		f.p.Exec(ctx, "DELETE FROM nodes WHERE id=ANY($1)", []int64{entry, exit})
	})
	if _, err := f.p.Exec(ctx, "INSERT INTO node_tunnel_links(ingress_node_id,egress_node_id,token) VALUES($1,$2,$3)", entry, exit, Secret()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.p.Exec(ctx, "INSERT INTO rules(user_id,node_id,egress_node_id,listen_port,target_host,target_port) VALUES($1,$2,$3,23000,'127.0.0.1',80)", f.user, entry, exit); err != nil {
		t.Fatal(err)
	}
	w := f.request(f.admin, fmt.Sprintf("admin/nodes/%d", exit), "DELETE", nil)
	securityStatus(t, w, 409)
	if !strings.Contains(w.Body.String(), "入口或出口") {
		t.Fatal("missing exit rule dependency explanation")
	}
	securityStatus(t, f.request(f.admin, fmt.Sprintf("admin/nodes/%d?force=true", exit), "DELETE", nil), 409)
}

func TestForceDeleteNodeArchivesUsageWithoutRefundingQuota(t *testing.T) {
	f := newSecurityFixture(t)
	ctx := context.Background()
	var oldNode, liveNode int64
	key, nodeName := Secret(), Secret()
	if err := f.p.QueryRow(ctx, "INSERT INTO nodes(name,token_hash,token,enabled,last_seen,applied_revision,config_revision) VALUES($1,$2,$3,false,now()-interval '1 hour',1,100) RETURNING id", nodeName, Hash(key), key).Scan(&oldNode); err != nil {
		t.Fatal(err)
	}
	if err := f.p.QueryRow(ctx, "INSERT INTO nodes(name,token_hash) VALUES($1,$2) RETURNING id", Secret(), Hash(Secret())).Scan(&liveNode); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, table := range []string{"grants", "rule_usage", "deleted_node_grants", "deleted_node_rule_usage"} {
			f.p.Exec(ctx, "DELETE FROM "+table+" WHERE node_id=ANY($1)", []int64{oldNode, liveNode})
		}
		f.p.Exec(ctx, "DELETE FROM rules WHERE node_id=$1", liveNode)
		f.p.Exec(ctx, "DELETE FROM deleted_nodes WHERE id=$1", oldNode)
		f.p.Exec(ctx, "DELETE FROM nodes WHERE id=ANY($1) OR name=$2", []int64{oldNode, liveNode}, nodeName)
	})
	testAuthorize(t, f.p, f.user, liveNode)
	if _, err := f.p.Exec(ctx, "UPDATE users SET quota_epoch=3,traffic_base_bytes=17 WHERE id=$1", f.user); err != nil {
		t.Fatal(err)
	}
	if _, err := f.p.Exec(ctx, `INSERT INTO grants(node_id,user_id,quota_epoch,issued,spent,released,traffic,unlimited_spent,unlimited_open) VALUES
 ($1,$3,3,1000,400,100,300,50,true),($1,$3,2,100,100,0,100,0,false),($2,$3,3,500,100,0,100,0,false)`, oldNode, liveNode, f.user); err != nil {
		t.Fatal(err)
	}
	if _, err := f.p.Exec(ctx, "UPDATE grants SET charged_bytes=traffic/2 WHERE node_id=ANY($1)", []int64{oldNode, liveNode}); err != nil {
		t.Fatal(err)
	}
	w := f.request(f.user, "rules", "POST", RuleInput{NodeID: liveNode, Name: "migrated-ledger", Protocol: "tcp", Targets: []string{"1.1.1.1:8080"}, Enabled: true})
	if w.Code != 200 {
		t.Fatalf("cannot create accounting fixture rule: %d %s", w.Code, w.Body.String())
	}
	var created struct{ ID int64 }
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if _, err := f.p.Exec(ctx, "INSERT INTO rule_usage(node_id,rule_id,user_id,traffic) VALUES($1,$3,$4,777),($2,$3,$4,222) ON CONFLICT(node_id,rule_id) DO UPDATE SET traffic=excluded.traffic", oldNode, liveNode, created.ID, f.user); err != nil {
		t.Fatal(err)
	}
	totals := func() [3]int64 {
		t.Helper()
		var result [3]int64
		err := f.p.QueryRow(ctx, "SELECT COALESCE(sum(traffic),0),COALESCE(sum(spent+unlimited_spent),0),COALESCE(sum(issued-released+unlimited_spent),0) FROM current_grants WHERE user_id=$1", f.user).Scan(&result[0], &result[1], &result[2])
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	before := totals()
	path := fmt.Sprintf("admin/nodes/%d", oldNode)
	w = f.request(f.admin, path, "DELETE", nil)
	securityStatus(t, w, 409)
	var conflict struct{ Code string }
	if err := json.Unmarshal(w.Body.Bytes(), &conflict); err != nil || conflict.Code != "node_delete_confirmation_required" {
		t.Fatal("missing machine-readable second-confirmation response", err)
	}
	var count int
	if err := f.p.QueryRow(ctx, "SELECT count(*) FROM deleted_nodes WHERE id=$1", oldNode).Scan(&count); err != nil || count != 0 {
		t.Fatal("ordinary delete already archived data", err)
	}
	securityStatus(t, f.request(f.user, path+"?force=true", "DELETE", nil), 403)
	// A late foreign-key failure must roll back both archival copies and the
	// removal of the original grants, rather than double counting or losing them.
	blocker := pgx.Identifier{"node_delete_block_" + Secret()[:12]}.Sanitize()
	if _, err := f.p.Exec(ctx, "CREATE TABLE "+blocker+"(node_id bigint REFERENCES nodes(id))"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.p.Exec(ctx, "DROP TABLE IF EXISTS "+blocker) })
	if _, err := f.p.Exec(ctx, "INSERT INTO "+blocker+" VALUES($1)", oldNode); err != nil {
		t.Fatal(err)
	}
	securityStatus(t, f.request(f.admin, path+"?force=true", "DELETE", nil), 409)
	if after := totals(); after != before {
		t.Fatal("failed deletion changed accounting", before, after)
	}
	if err := f.p.QueryRow(ctx, "SELECT count(*) FROM deleted_nodes WHERE id=$1", oldNode).Scan(&count); err != nil || count != 0 {
		t.Fatal("failed deletion left a partial archive", err)
	}
	if _, err := f.p.Exec(ctx, "DROP TABLE "+blocker); err != nil {
		t.Fatal(err)
	}
	start, result := make(chan struct{}), make(chan int, 2)
	for i := 0; i < 2; i++ {
		go func() { <-start; result <- f.request(f.admin, path+"?force=true", "DELETE", nil).Code }()
	}
	close(start)
	statuses := map[int]int{}
	statuses[<-result]++
	statuses[<-result]++
	if statuses[200] != 1 || statuses[404] != 1 {
		t.Fatal("concurrent deletion was not atomic and idempotent", statuses)
	}
	if after := totals(); before != after {
		t.Fatal("deleted node reset usage or refunded outstanding quota", before, after)
	}
	var charged int64
	if err := f.p.QueryRow(ctx, "SELECT sum(charged_bytes) FROM current_grants WHERE user_id=$1", f.user).Scan(&charged); err != nil || charged != 200 {
		t.Fatal("deleted node lost weighted charges", charged, err)
	}
	if _, err := f.s.nodeForToken(ctx, key); err != pgx.ErrNoRows {
		t.Fatal("deleted node credential still authenticates", err)
	}
	var archived, active int
	if err := f.p.QueryRow(ctx, "SELECT (SELECT count(*) FROM deleted_node_grants WHERE node_id=$1),(SELECT count(*) FROM nodes WHERE id=$1)", oldNode).Scan(&archived, &active); err != nil || archived != 2 || active != 0 {
		t.Fatal("node was not physically deleted with its accounting retained", err)
	}
	var unlimitedOpen bool
	if err := f.p.QueryRow(ctx, "SELECT bool_or(unlimited_open) FROM deleted_node_grants WHERE node_id=$1", oldNode).Scan(&unlimitedOpen); err != nil || unlimitedOpen {
		t.Fatal("archived unlimited authorization remained open", err)
	}
	w = f.request(f.user, "rules", "GET", nil)
	securityStatus(t, w, 200)
	var rules []struct {
		ID           int64 `json:"id"`
		TrafficBytes int64 `json:"traffic_bytes"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &rules); err != nil || len(rules) != 1 || rules[0].TrafficBytes != 999 {
		t.Fatal("migrated rule lost its old node traffic", err, rules)
	}
	securityStatus(t, f.request(f.user, "rules/batch", "POST", BatchInput{IDs: []int64{created.ID}, Action: "clear"}), 200)
	if _, err := f.p.Exec(ctx, "UPDATE rule_usage SET traffic=333 WHERE node_id=$1 AND rule_id=$2", liveNode, created.ID); err != nil {
		t.Fatal(err)
	}
	w = f.request(f.user, "rules", "GET", nil)
	securityStatus(t, w, 200)
	if err := json.Unmarshal(w.Body.Bytes(), &rules); err != nil || len(rules) != 1 || rules[0].TrafficBytes != 111 {
		t.Fatal("traffic reset ignored archived history", err, rules)
	}
	securityStatus(t, f.request(f.admin, path+"?force=true", "DELETE", nil), 404)
	if _, err := f.p.Exec(ctx, "INSERT INTO nodes(name,token_hash) VALUES($1,$2)", nodeName, Hash(Secret())); err != nil {
		t.Fatal("deleted node name cannot be reused", err)
	}
	if _, err := f.p.Exec(ctx, "UPDATE users SET quota_epoch=4 WHERE id=$1", f.user); err != nil {
		t.Fatal(err)
	}
	if after := totals(); after != [3]int64{} {
		t.Fatal("archived traffic spilled into a new quota epoch", after)
	}
}
