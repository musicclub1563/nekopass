package control

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/nekopass/nekopass/internal/billing"
	pb "github.com/nekopass/nekopass/internal/protocol"
)

func appendMeteringControl(ctx context.Context, tx pgx.Tx, node int64, version int32, out *pb.ControlMessage) error {
	type rates struct{ ingress, egress int64 }
	nodes := []int64{node}
	for _, r := range out.Rules {
		if r.EgressNodeId != 0 {
			nodes = append(nodes, r.EgressNodeId)
		}
		for _, p := range r.EgressPeers {
			nodes = append(nodes, p.NodeId)
		}
	}
	values := map[int64]rates{}
	rows, err := tx.Query(ctx, "SELECT id,ingress_rate,egress_rate FROM nodes WHERE id=ANY($1::bigint[])", nodes)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id int64
		var r rates
		if err = rows.Scan(&id, &r.ingress, &r.egress); err != nil {
			rows.Close()
			return err
		}
		values[id] = r
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if _, ok := values[node]; !ok {
		return errors.New("metering ingress no longer exists")
	}
	users := map[int64]*pb.UserPolicy{}
	var ids []int64
	for _, u := range out.Users {
		users[u.Id] = u
		ids = append(ids, u.Id)
	}
	if version >= 25 {
		rows, err = tx.Query(ctx, `SELECT u.id,u.enabled,(u.quota_bytes>=0 AND (u.traffic_base_bytes>=u.quota_bytes OR u.traffic_base_bytes+COALESCE((SELECT sum(charged_bytes) FROM current_grants WHERE user_id=u.id),0)>=u.quota_bytes OR u.traffic_base_bytes+COALESCE((SELECT sum(issued-released+unlimited_spent) FROM current_grants WHERE user_id=u.id),0)>u.quota_bytes)) FROM user_entitlements u WHERE u.id=ANY($1::bigint[])`, ids)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id int64
			var enabled, blocked bool
			if err = rows.Scan(&id, &enabled, &blocked); err != nil {
				rows.Close()
				return err
			}
			if u := users[id]; u != nil {
				u.Enabled = enabled
				u.QuotaBlocked = blocked
				u.MeteringConfigured = true
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
	}
	for _, r := range out.Rules {
		r.MeteringConfigured = true
		r.TrafficRate = values[node].ingress
		if r.EgressNodeId != 0 {
			if _, ok := values[r.EgressNodeId]; !ok {
				r.Enabled = false
				continue
			}
			r.TrafficRate += values[r.EgressNodeId].egress
		}
		blocked := users[r.UserId] != nil && users[r.UserId].QuotaBlocked
		if r.EgressGroupId != 0 {
			peers := r.EgressPeers[:0]
			for _, p := range r.EgressPeers {
				if _, ok := values[p.NodeId]; !ok {
					continue
				}
				p.TrafficRate = values[node].ingress + values[p.NodeId].egress
				if version < 25 && p.TrafficRate != billing.Scale || blocked && p.TrafficRate > 0 {
					continue
				}
				peers = append(peers, p)
			}
			r.EgressPeers = peers
			if len(peers) == 0 {
				r.Enabled = false
			}
		} else if version < 25 && r.TrafficRate != billing.Scale || blocked && r.TrafficRate > 0 {
			r.Enabled = false
		}
	}
	return nil
}
