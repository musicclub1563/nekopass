package control

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"testing"

	pb "github.com/nekopass/nekopass/internal/protocol"
)

func TestEgressGroupAuthorizationDistributionAndBatch(t *testing.T) {
	f := newSecurityFixture(t)
	ctx := context.Background()
	var entry int64
	if err := f.p.QueryRow(ctx, `INSERT INTO nodes(name,token_hash,protocol_version) VALUES($1,$2,24) RETURNING id`, Secret(), Secret()).Scan(&entry); err != nil {
		t.Fatal(err)
	}
	testAuthorize(t, f.p, f.user, entry)
	var exits []int64
	for i := 0; i < 3; i++ {
		var id int64
		if err := f.p.QueryRow(ctx, `INSERT INTO nodes(name,token_hash,tunnel_exit_enabled,tunnel_public_host,tunnel_listen_port,protocol_version) VALUES($1,$2,true,'exit.example.test',24443,24) RETURNING id`, Secret(), Secret()).Scan(&id); err != nil {
			t.Fatal(err)
		}
		exits = append(exits, id)
		if _, err := f.p.Exec(ctx, `INSERT INTO node_tunnel_links(ingress_node_id,egress_node_id,token) VALUES($1,$2,$3)`, entry, id, Secret()); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		f.p.Exec(ctx, "DELETE FROM rule_usage WHERE node_id=$1", entry)
		f.p.Exec(ctx, "DELETE FROM rules WHERE node_id=$1", entry)
		f.p.Exec(ctx, "DELETE FROM grants WHERE node_id=ANY($1)", append(exits, entry))
		f.p.Exec(ctx, "DELETE FROM nodes WHERE id=ANY($1)", append(exits, entry))
	})
	group := nodeGroupInput{Name: Secret(), Enabled: true, NodeIDs: exits, Strategy: "round_robin", HealthIntervalSeconds: 3, HealthFailures: 2, HealthSuccesses: 2, Members: []nodeGroupMember{{exits[0], "active", 2}, {exits[1], "standby", 1}, {exits[2], "disabled", 1}}}
	securityStatus(t, f.request(f.user, "admin/node-groups", "POST", group), 403)
	w := f.request(f.admin, "admin/node-groups", "POST", group)
	securityStatus(t, w, 200)
	var created struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	gid := created.ID
	t.Cleanup(func() {
		f.p.Exec(ctx, "DELETE FROM rules WHERE egress_group_id=$1", gid)
		f.p.Exec(ctx, "DELETE FROM user_node_groups WHERE group_id=$1", gid)
		f.p.Exec(ctx, "DELETE FROM node_groups WHERE id=$1", gid)
	})
	rule := RuleInput{NodeID: entry, EgressGroupID: gid, Targets: []string{"1.1.1.1:443"}, Enabled: true}
	securityStatus(t, f.request(f.user, "rules", "POST", rule), 400)
	if _, err := f.p.Exec(ctx, "INSERT INTO user_node_groups(user_id,group_id) VALUES($1,$2)", f.user, gid); err != nil {
		t.Fatal(err)
	}
	w = f.request(f.user, "rules", "POST", rule)
	if w.Code != 200 {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	json.Unmarshal(w.Body.Bytes(), &created)
	rid := created.ID
	securityStatus(t, f.request(f.other, "rules", "POST", rule), 400)
	w = f.request(f.user, "rule-egress-groups", "GET", nil)
	securityStatus(t, w, 200)
	if !bytes.Contains(w.Body.Bytes(), []byte(group.Name)) || bytes.Contains(w.Body.Bytes(), []byte("token")) {
		t.Fatal("group choices missing or leaked credentials")
	}
	w = f.request(f.other, "rule-egress-groups", "GET", nil)
	securityStatus(t, w, 200)
	if bytes.Contains(w.Body.Bytes(), []byte(group.Name)) {
		t.Fatal("foreign group exposed")
	}
	stream := &StreamServer{Server: f.s}
	instances := map[int64]string{}
	exchange := func(id int64, version int32) *pb.ControlMessage {
		t.Helper()
		if instances[id] == "" {
			instances[id] = Secret()
		}
		c, err := stream.exchange(ctx, f.p, id, &pb.AgentMessage{ProtocolVersion: version, InstanceId: instances[id]})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	for i, id := range exits {
		c := exchange(id, 24)
		want := 1
		if i == 2 {
			want = 0
		}
		if len(c.EgressRules) != want {
			t.Fatalf("exit %d grants %d want %d", i, len(c.EgressRules), want)
		}
	}
	c := exchange(entry, 24)
	if len(c.Rules) != 1 || c.Rules[0].Id != rid || !c.Rules[0].Enabled || len(c.Rules[0].EgressPeers) != 2 || c.Rules[0].EgressStrategy != "round_robin" || c.Rules[0].EgressPeers[0].Weight != 2 || !c.Rules[0].EgressPeers[1].Standby {
		t.Fatal("group candidates/policy not distributed", c.Rules)
	}
	if len(exchange(entry, 23).Rules) != 1 || exchange(entry, 23).Rules[0].Enabled {
		t.Fatal("old entry received an active group rule")
	}
	// Re-selecting a group in a batch must clear the previous single-node exit.
	securityStatus(t, f.request(f.user, "rules/batch", "POST", BatchInput{Action: "edit", IDs: []int64{rid}, Patch: &RuleBatchPatch{EgressNodeID: &exits[0]}}), 200)
	securityStatus(t, f.request(f.user, "rules/batch", "POST", BatchInput{Action: "edit", IDs: []int64{rid}, Patch: &RuleBatchPatch{EgressGroupID: &gid}}), 200)
	var node, groupID int64
	if err := f.p.QueryRow(ctx, "SELECT COALESCE(egress_node_id,0),COALESCE(egress_group_id,0) FROM rules WHERE id=$1", rid).Scan(&node, &groupID); err != nil || node != 0 || groupID != gid {
		t.Fatal("batch lost exit type", err)
	}
	// Updating node details preserves group-local roles and weights.
	v, _, err := loadNodeInput(ctx, f.p, exits[0])
	if err != nil {
		t.Fatal(err)
	}
	securityStatus(t, f.request(f.admin, fmt.Sprintf("admin/nodes/%d", exits[0]), "PUT", v), 200)
	var weight int
	var role string
	if err = f.p.QueryRow(ctx, "SELECT weight,role FROM node_group_members WHERE group_id=$1 AND node_id=$2", gid, exits[0]).Scan(&weight, &role); err != nil || weight != 2 || role != "active" {
		t.Fatal("node save reset group scheduling", err)
	}
	// Removing ingress permission revokes the affected exit only.
	f.p.Exec(ctx, "DELETE FROM node_tunnel_links WHERE ingress_node_id=$1 AND egress_node_id=$2", entry, exits[0])
	c = exchange(entry, 24)
	if len(c.Rules[0].EgressPeers) != 1 || !c.Rules[0].EgressPeers[0].Standby {
		t.Fatal("revoked candidate still delivered")
	}
	if len(exchange(exits[0], 24).EgressRules) != 0 {
		t.Fatal("revoked exit grant still delivered")
	}
	securityStatus(t, f.request(f.admin, fmt.Sprintf("admin/node-groups/%d", gid), "DELETE", nil), 409)
	// Losing the explicit group grant cannot retain access through another group.
	f.p.Exec(ctx, "DELETE FROM user_node_groups WHERE user_id=$1 AND group_id=$2", f.user, gid)
	c = exchange(entry, 24)
	if c.Rules[0].Enabled || len(c.Rules[0].EgressPeers) != 0 {
		t.Fatal("group grant revocation bypassed")
	}
}

func TestGroupFinalSnapshotReleasesDraftReferences(t *testing.T) {
	r := &pb.Rule{Id: 1, EgressGroupId: 3, Enabled: true}
	p := &pb.Rule{Id: 1, EgressNodeId: 4, Enabled: true, Targets: []string{"draft-only-target"}}
	backing := []*pb.Rule{r, p}
	out := &pb.ControlMessage{Rules: backing}
	finishGroupDrafts(out, []groupRuleDraft{{rule: r, peer: p, weight: 1}}, 25, 1)
	if len(out.Rules) != 1 || len(out.Rules[0].EgressPeers) != 1 || out.Rules[0].EgressPeers[0].NodeId != 4 {
		t.Fatal("group route changed")
	}
	if backing[1] != nil || cap(out.Rules) > 1 {
		t.Fatal("snapshot retains temporary candidate rules")
	}
}
