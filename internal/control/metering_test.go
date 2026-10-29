package control

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	pb "github.com/nekopass/nekopass/internal/protocol"
)

func TestWeightedUsageSeparatesQuotaAndRawAndKeepsFreeRules(t *testing.T) {
	f := newSecurityFixture(t)
	ctx := context.Background()
	f.p.Exec(ctx, "UPDATE users SET quota_bytes=100 WHERE id=$1", f.user)
	var entry, paid, free int64
	for i, id := range []*int64{&entry, &paid, &free} {
		if err := f.p.QueryRow(ctx, `INSERT INTO nodes(name,token_hash,ingress_rate,egress_rate,tunnel_exit_enabled,tunnel_public_host,tunnel_listen_port,protocol_version) VALUES($1,$2,0,$3,$4,'exit.example.test',24443,25) RETURNING id`, Secret(), Secret(), map[bool]int{true: 10000, false: 0}[i == 1], i > 0).Scan(id); err != nil {
			t.Fatal(err)
		}
		testAuthorize(t, f.p, f.user, *id)
	}
	t.Cleanup(func() {
		f.p.Exec(ctx, "DELETE FROM rule_usage WHERE node_id=$1", entry)
		f.p.Exec(ctx, "DELETE FROM rules WHERE node_id=$1", entry)
		f.p.Exec(ctx, "DELETE FROM grants WHERE node_id=ANY($1)", []int64{entry, paid, free})
		f.p.Exec(ctx, "DELETE FROM nodes WHERE id=ANY($1)", []int64{entry, paid, free})
	})
	for _, id := range []int64{paid, free} {
		if _, err := f.p.Exec(ctx, "INSERT INTO node_tunnel_links(ingress_node_id,egress_node_id,token) VALUES($1,$2,$3)", entry, id, Secret()); err != nil {
			t.Fatal(err)
		}
	}
	group := testAuthorize(t, f.p, f.user, paid)
	f.p.Exec(ctx, "INSERT INTO node_group_members(group_id,node_id) VALUES($1,$2)", group, free)
	makeRule := func(exit, group int64) int64 {
		t.Helper()
		w := f.request(f.user, "rules", "POST", RuleInput{NodeID: entry, EgressNodeID: exit, EgressGroupID: group, Targets: []string{"1.1.1.1:443"}, Enabled: true})
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		var id struct {
			ID int64 `json:"id"`
		}
		json.Unmarshal(w.Body.Bytes(), &id)
		return id.ID
	}
	directRule, paidRule, groupRule := makeRule(0, 0), makeRule(paid, 0), makeRule(0, group)
	stream := &StreamServer{Server: f.s}
	report := &pb.AgentMessage{ProtocolVersion: 25, InstanceId: Secret(), RequestUsers: []int64{f.user}}
	config, err := stream.exchange(ctx, f.p, entry, report)
	if err != nil {
		t.Fatal(err)
	}
	if len(config.Users) != 1 || config.Users[0].Issued != 100 {
		t.Fatal("initial weighted credit missing", config.Users)
	}
	report.RequestUsers = nil
	report.Usage = []*pb.Usage{{UserId: f.user, Spent: 100, Traffic: 200, ChargedBytes: 100}}
	config, err = stream.exchange(ctx, f.p, entry, report)
	if err != nil {
		t.Fatal("raw bytes must be allowed above charged bytes", err)
	}
	if !config.Users[0].Enabled || !config.Users[0].QuotaBlocked {
		t.Fatal("exhaustion disabled the whole account or did not block paid traffic", config.Users[0])
	}
	for _, r := range config.Rules {
		switch r.Id {
		case directRule:
			if !r.Enabled || r.TrafficRate != 0 {
				t.Fatal("free direct forwarding stopped")
			}
		case paidRule:
			if r.Enabled {
				t.Fatal("paid route not stopped")
			}
		case groupRule:
			if !r.Enabled || len(r.EgressPeers) != 1 || r.EgressPeers[0].NodeId != free || r.EgressPeers[0].TrafficRate != 0 {
				t.Fatal("group did not retain only its free candidate", r)
			}
		}
	}
	w := f.request(f.user, "profile", "GET", nil)
	securityStatus(t, w, 200)
	var users []map[string]any
	json.Unmarshal(w.Body.Bytes(), &users)
	if users[0]["traffic_bytes"] != float64(100) || users[0]["actual_traffic_bytes"] != float64(200) {
		t.Fatal("quota display uses unweighted traffic", users[0]["traffic_bytes"], users[0]["actual_traffic_bytes"])
	}
	report.Usage[0].ChargedBytes = 101
	if _, err = stream.exchange(ctx, f.p, entry, report); err == nil {
		t.Fatal("charge exceeded allowance")
	}
	report.Usage[0].ChargedBytes = 99
	if _, err = stream.exchange(ctx, f.p, entry, report); err == nil {
		t.Fatal("charge regressed")
	}
	report.Usage = nil
	report.ProtocolVersion = 24
	if _, err = stream.exchange(ctx, f.p, entry, report); err == nil {
		t.Fatal("old agent accepted non-legacy billing state")
	}
	// Rates are combined on the controller before being sent to the entry;
	// each group member retains its individual egress component.
	f.p.Exec(ctx, "UPDATE users SET quota_bytes=-1 WHERE id=$1", f.user)
	f.p.Exec(ctx, "UPDATE nodes SET ingress_rate=12500 WHERE id=$1", entry)
	f.p.Exec(ctx, "UPDATE nodes SET egress_rate=7500 WHERE id=$1", paid)
	report.ProtocolVersion = 25
	config, err = stream.exchange(ctx, f.p, entry, report)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range config.Rules {
		if r.Id == directRule && r.TrafficRate != 12500 {
			t.Fatal("direct rate included an exit", r)
		}
		if r.Id == paidRule && r.TrafficRate != 20000 {
			t.Fatal("entry and exit rates were not added", r)
		}
		if r.Id == groupRule {
			if len(r.EgressPeers) != 2 {
				t.Fatal("unlimited quota did not restore paid peers", r)
			}
			for _, p := range r.EgressPeers {
				want := int64(12500)
				if p.NodeId == paid {
					want = 20000
				}
				if p.TrafficRate != want {
					t.Fatal("group multiplier not combined", p)
				}
			}
		}
	}
}

func TestNodeMultiplierIsAdminOnlyAndPreciselyStored(t *testing.T) {
	f := newSecurityFixture(t)
	ctx := context.Background()
	var id int64
	f.p.QueryRow(ctx, "INSERT INTO nodes(name,token_hash) VALUES($1,$2) RETURNING id", Secret(), Secret()).Scan(&id)
	t.Cleanup(func() { f.p.Exec(ctx, "DELETE FROM nodes WHERE id=$1", id) })
	v, _, err := loadNodeInput(ctx, f.p, id)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(v)
	var body map[string]any
	json.Unmarshal(data, &body)
	body["ingress_multiplier"] = "1.2345"
	body["egress_multiplier"] = "0"
	path := fmt.Sprintf("admin/nodes/%d", id)
	securityStatus(t, f.request(f.user, path, "PUT", body), 403)
	securityStatus(t, f.request(f.admin, path, "PUT", body), 200)
	var ingress, egress int64
	if err = f.p.QueryRow(ctx, "SELECT ingress_rate,egress_rate FROM nodes WHERE id=$1", id).Scan(&ingress, &egress); err != nil || ingress != 12345 || egress != 0 {
		t.Fatal("multiplier rounded or zero rejected", ingress, egress, err)
	}
	for _, bad := range []any{"-1", "0.00001", "1001", "NaN"} {
		body["ingress_multiplier"] = bad
		securityStatus(t, f.request(f.admin, path, "PUT", body), 400)
	}
}
