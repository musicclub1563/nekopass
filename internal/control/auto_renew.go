package control

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
)

func (s *Server) autoRenew(w http.ResponseWriter, r *http.Request) {
	uid := current(r).ID
	rows, err := s.Pool.Query(r.Context(), `SELECT u.auto_renew_enabled AS enabled,u.auto_renew_cycle AS cycle,u.auto_renew_price_cents AS price_limit_cents,u.auto_renew_status AS status,u.auto_renew_error AS error,u.auto_renew_last_attempt AS last_attempt_at,u.auto_renew_last_success AS last_success_at,u.plan_id,COALESCE(p.name,'') AS plan_name,COALESCE(p.prices,'{}') AS prices,e.expires_at FROM users u JOIN user_entitlements e ON e.id=u.id LEFT JOIN plans p ON p.id=u.plan_id AND p.enabled WHERE u.id=$1`, uid)
	s.sendRows(w, rows, err)
}
func (s *Server) saveAutoRenew(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Enabled       bool   `json:"enabled"`
		Cycle         string `json:"cycle"`
		ExpectedPrice int64  `json:"expected_price_cents"`
	}
	if !decode(w, r, &in) {
		return
	}
	ctx := r.Context()
	tx, err := s.ruleTx(ctx)
	if err != nil {
		s.dbError(w, err)
		return
	}
	defer tx.Rollback(ctx)
	uid := current(r).ID
	sub, err := subscriptionForUpdate(ctx, tx, uid)
	if err != nil {
		fail(w, 403, "账号不可用")
		return
	}
	if !in.Enabled {
		_, err = tx.Exec(ctx, "UPDATE users SET auto_renew_enabled=false,auto_renew_status='off',auto_renew_error='' WHERE id=$1", uid)
	} else {
		months, ok := billingMonths[in.Cycle]
		if !ok || months == 0 {
			fail(w, 400, "请选择自动续费周期，一次性套餐无需自动续费")
			return
		}
		var expiry *time.Time
		if err = tx.QueryRow(ctx, "SELECT expires_at FROM user_entitlements WHERE id=$1", uid).Scan(&expiry); err != nil {
			s.dbError(w, err)
			return
		}
		if expiry == nil {
			fail(w, 400, "永不到期套餐无需自动续费")
			return
		}
		plan, e := salePlanForUpdate(ctx, tx, sub.PlanID)
		if e != nil {
			fail(w, 400, "当前套餐未在售，无法开启自动续费")
			return
		}
		price, ok := plan.Prices[in.Cycle]
		if !ok || price != in.ExpectedPrice {
			fail(w, 409, "价格已变化，请刷新后重新确认")
			return
		}
		_, err = tx.Exec(ctx, "UPDATE users SET auto_renew_enabled=true,auto_renew_cycle=$2,auto_renew_price_cents=$3,auto_renew_status='waiting',auto_renew_error='',auto_renew_next_attempt=now() WHERE id=$1", uid, in.Cycle, price)
	}
	if err != nil {
		s.dbError(w, err)
		return
	}
	if err = tx.Commit(ctx); err != nil {
		s.dbError(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) renewDueSubscriptions(ctx context.Context, now time.Time) error {
	rows, err := s.Pool.Query(ctx, `SELECT u.id FROM users u JOIN user_entitlements e ON e.id=u.id WHERE u.enabled AND u.auto_renew_enabled AND e.expires_at<=$1 AND u.auto_renew_next_attempt<=$1 ORDER BY u.auto_renew_next_attempt,u.id LIMIT 100`, now)
	if err != nil {
		return err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return err
	}
	var failures error
	for _, id := range ids {
		if err = s.renewOneSubscription(ctx, id, now); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			failures = errors.Join(failures, fmt.Errorf("user %d: %w", id, err))
		}
	}
	return failures
}
func (s *Server) renewOneSubscription(ctx context.Context, uid int64, now time.Time) error {
	tx, err := s.ruleTx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	sub, err := subscriptionForUpdate(ctx, tx, uid)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var enabled bool
	var cycle string
	var cap int64
	var expiry *time.Time
	var attempt time.Time
	if err = tx.QueryRow(ctx, `SELECT u.auto_renew_enabled,u.auto_renew_cycle,u.auto_renew_price_cents,e.expires_at,u.auto_renew_next_attempt FROM users u JOIN user_entitlements e ON e.id=u.id WHERE u.id=$1`, uid).Scan(&enabled, &cycle, &cap, &expiry, &attempt); err != nil {
		return err
	}
	if !enabled || expiry == nil || expiry.After(now) || attempt.After(now) {
		return nil
	}
	failRenew := func(status, message string) error {
		if _, err := tx.Exec(ctx, "UPDATE users SET auto_renew_status=$2,auto_renew_error=$3,auto_renew_last_attempt=$4,auto_renew_next_attempt=$4::timestamptz+interval '5 minutes' WHERE id=$1", uid, status, message, now); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	months, ok := billingMonths[cycle]
	if !ok || months == 0 {
		return failRenew("unavailable", "自动续费周期无效")
	}
	p, err := salePlanForUpdate(ctx, tx, sub.PlanID)
	if errors.Is(err, pgx.ErrNoRows) {
		return failRenew("unavailable", "当前套餐已停售")
	}
	if err != nil {
		return err
	}
	price, ok := p.Prices[cycle]
	if !ok {
		return failRenew("unavailable", "该周期已停售")
	}
	if price < 0 || price > maxMoneyCents {
		return failRenew("unavailable", "invalid plan price")
	}
	if price > cap {
		return failRenew("price_changed", "价格高于已确认金额，请重新确认自动续费")
	}
	if p.PurchaseLimit > 0 {
		var n int64
		if err = tx.QueryRow(ctx, "SELECT count(*) FROM shop_orders WHERE user_id=$1 AND plan_id=$2 AND status='paid' AND kind IN ('purchase','renewal')", uid, p.ID).Scan(&n); err != nil {
			return err
		}
		if n >= int64(p.PurchaseLimit) {
			return failRenew("purchase_limit", "已达到套餐购买上限")
		}
	}
	var limit, n int
	if err = tx.QueryRow(ctx, "SELECT max_rules,(SELECT count(*) FROM rules WHERE user_id=$1) FROM users WHERE id=$1", uid).Scan(&limit, &n); err != nil {
		return err
	}
	if limit > 0 && n > limit {
		return failRenew("rule_limit", "当前规则数量超过个人规则上限")
	}
	balance, err := walletBalanceLocked(ctx, tx, uid)
	if err != nil {
		return err
	}
	if balance < price {
		return failRenew("insufficient_balance", "余额不足，请充值后等待重试")
	}
	key := "auto-" + Hash(fmt.Sprintf("%d/%d/%s", uid, p.ID, expiry.UTC().Format(time.RFC3339Nano)))[:40]
	var exists bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM shop_orders WHERE user_id=$1 AND request_key=$2)", uid, key).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return failRenew("already_processed", "此到期周期已完成自动续费，请检查套餐到期时间")
	}
	if _, _, err = completePurchase(ctx, tx, uid, p, sub, cycle, key, price, balance, now, true); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "UPDATE users SET auto_renew_status='renewed',auto_renew_error='',auto_renew_last_attempt=$2,auto_renew_last_success=$2,auto_renew_next_attempt=subscription_expires_at WHERE id=$1", uid, now); err != nil {
		return err
	}
	return finishRules(ctx, tx)
}
