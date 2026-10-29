package control

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func prepareAutoRenew(t *testing.T) (*securityFixture, time.Time) {
	t.Helper()
	f := newSecurityFixture(t)
	now := time.Now().Add(2 * time.Hour).Truncate(time.Microsecond)
	expiry := now.Add(-time.Minute)
	_, err := f.p.Exec(context.Background(), `UPDATE users SET subscription_managed=true,subscription_expires_at=$2,next_reset_at=$2,reset_anchor_at=$2::timestamptz-interval '1 month',reset_index=1,quota_bytes=1234567,speed_bps=123000,traffic_base_bytes=123 WHERE id=$1`, f.user, expiry)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.p.Exec(context.Background(), "INSERT INTO wallet_accounts(user_id,balance_cents) VALUES($1,5000)", f.user)
	if err != nil {
		t.Fatal(err)
	}
	securityStatus(t, f.request(f.user, "profile/auto-renew", "PUT", map[string]any{"enabled": true, "cycle": "monthly", "expected_price_cents": 1000}), 200)
	return f, now
}
func TestAutoRenewConcurrentChargesOnceAndPreservesPersonalResources(t *testing.T) {
	f, now := prepareAutoRenew(t)
	ctx := context.Background()
	settings := defaultSettings()
	settings.ReferralEnabled = true
	settings.ReferralMode = "first"
	settings.ReferralRate = "15"
	securityStatus(t, f.request(f.admin, "admin/settings", "PUT", settings), 200)
	f.p.Exec(ctx, "UPDATE users SET inviter_id=$2 WHERE id=$1", f.user, f.other)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := f.s.renewDueSubscriptions(ctx, now); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	var balance, orders, epoch, used, quota, speed, refund int64
	var expiry, reset time.Time
	if err := f.p.QueryRow(ctx, `SELECT (SELECT balance_cents FROM wallet_accounts WHERE user_id=$1),(SELECT count(*) FROM shop_orders WHERE user_id=$1 AND kind='renewal'),quota_epoch,traffic_base_bytes,quota_bytes,speed_bps,subscription_expires_at,next_reset_at FROM users WHERE id=$1`, f.user).Scan(&balance, &orders, &epoch, &used, &quota, &speed, &expiry, &reset); err != nil {
		t.Fatal(err)
	}
	if balance != 4000 || orders != 1 || epoch != 1 || used != 0 || quota != 1234567 || speed != 123000 || !expiry.Equal(addBillingMonths(now, 1)) || !reset.Equal(expiry) {
		t.Fatal("renewal duplicated or changed personal resources", balance, orders, epoch, used, quota, speed, expiry, reset)
	}
	if err := f.p.QueryRow(ctx, "SELECT balance_cents FROM wallet_accounts WHERE user_id=$1", f.other).Scan(&refund); err != nil || refund != 150 {
		t.Fatal("automatic referral missing/duplicated", refund, err)
	}
	var automatic bool
	f.p.QueryRow(ctx, "SELECT (snapshot->>'automatic')::boolean FROM shop_orders WHERE user_id=$1 AND kind='renewal'", f.user).Scan(&automatic)
	if !automatic {
		t.Fatal("automatic order is not identifiable")
	}
	if err := f.s.renewDueSubscriptions(ctx, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	f.p.QueryRow(ctx, "SELECT balance_cents FROM wallet_accounts WHERE user_id=$1", f.user).Scan(&balance)
	if balance != 4000 {
		t.Fatal("retry charged again")
	}
}
func TestAutoRenewFailureAndRetryPolicies(t *testing.T) {
	for _, scenario := range []string{"balance", "price", "off_sale", "limit", "disabled"} {
		t.Run(scenario, func(t *testing.T) {
			f, now := prepareAutoRenew(t)
			ctx := context.Background()
			expected := ""
			switch scenario {
			case "balance":
				f.p.Exec(ctx, "UPDATE wallet_accounts SET balance_cents=0 WHERE user_id=$1", f.user)
				expected = "insufficient_balance"
			case "price":
				f.p.Exec(ctx, `UPDATE plans SET prices='{"monthly":2000}' WHERE id=$1`, f.plan)
				expected = "price_changed"
			case "off_sale":
				f.p.Exec(ctx, "UPDATE plans SET enabled=false WHERE id=$1", f.plan)
				expected = "unavailable"
			case "limit":
				f.p.Exec(ctx, "UPDATE plans SET purchase_limit=1 WHERE id=$1", f.plan)
				f.p.Exec(ctx, `INSERT INTO shop_orders(order_no,user_id,kind,plan_id,plan_name,amount_cents,status,request_key) VALUES($1,$2,'purchase',$3,'fixture',0,'paid',$4)`, Secret(), f.user, f.plan, Secret())
				expected = "purchase_limit"
			case "disabled":
				f.p.Exec(ctx, "UPDATE users SET enabled=false WHERE id=$1", f.user)
				expected = "waiting"
			}
			if err := f.s.renewDueSubscriptions(ctx, now); err != nil {
				t.Fatal(err)
			}
			var count int
			var status string
			f.p.QueryRow(ctx, "SELECT auto_renew_status,(SELECT count(*) FROM shop_orders WHERE user_id=$1 AND kind='renewal') FROM users WHERE id=$1", f.user).Scan(&status, &count)
			if status != expected || count != 0 {
				t.Fatal("failed renewal charged or wrong status", status, count)
			}
			if scenario == "balance" {
				f.p.Exec(ctx, "UPDATE wallet_accounts SET balance_cents=5000 WHERE user_id=$1", f.user)
				if err := f.s.renewDueSubscriptions(ctx, now.Add(6*time.Minute)); err != nil {
					t.Fatal(err)
				}
				f.p.QueryRow(ctx, "SELECT count(*) FROM shop_orders WHERE user_id=$1 AND kind='renewal'", f.user).Scan(&count)
				if count != 1 {
					t.Fatal("balance recharge did not resume renewal")
				}
			}
		})
	}
}
func TestAutoRenewSelfScopePriceConfirmationAndPermanentPlan(t *testing.T) {
	f, _ := prepareAutoRenew(t)
	securityStatus(t, f.request(f.user, "profile/auto-renew", "PUT", map[string]any{"enabled": true, "cycle": "monthly", "expected_price_cents": 1}), 409)
	securityStatus(t, f.request(f.user, "profile/auto-renew", "PUT", map[string]any{"enabled": true, "cycle": "onetime", "expected_price_cents": 0}), 400)
	securityStatus(t, f.request(f.user, "profile/auto-renew", "PUT", map[string]any{"enabled": true, "cycle": "monthly", "expected_price_cents": 1000, "user_id": f.other}), 400)
	securityStatus(t, f.request(f.user, fmt.Sprintf("admin/users/%d/auto-renew", f.other), "PUT", map[string]any{}), 403)
	w := f.request(f.user, "profile/auto-renew", "GET", nil)
	securityStatus(t, w, 200)
	var settings []map[string]any
	if json.Unmarshal(w.Body.Bytes(), &settings) != nil || len(settings) != 1 || settings[0]["enabled"] != true {
		t.Fatal("settings missing")
	}
	f.p.Exec(context.Background(), "UPDATE users SET subscription_expires_at=NULL WHERE id=$1", f.user)
	securityStatus(t, f.request(f.user, "profile/auto-renew", "PUT", map[string]any{"enabled": true, "cycle": "monthly", "expected_price_cents": 1000}), 400)
	securityStatus(t, f.request(f.user, "profile/auto-renew", "PUT", map[string]any{"enabled": false}), 200)
}

func TestAutoRenewRacingManualRenewalDoesNotDoubleCharge(t *testing.T) {
	f, now := prepareAutoRenew(t)
	start := make(chan struct{})
	manual := make(chan *httptest.ResponseRecorder, 1)
	automatic := make(chan error, 1)
	go func() {
		<-start
		manual <- f.request(f.user, "shop/purchase", "POST", checkoutInput{PlanID: f.plan, Cycle: "monthly", ExpectedPrice: 1000, ExpectedEpoch: 0, RequestKey: Secret()})
	}()
	go func() { <-start; automatic <- f.s.renewDueSubscriptions(context.Background(), now) }()
	close(start)
	w := <-manual
	if w.Code != 200 && w.Code != 409 {
		t.Fatal(w.Code, w.Body.String())
	}
	if err := <-automatic; err != nil {
		t.Fatal(err)
	}
	var balance, orders int64
	if err := f.p.QueryRow(context.Background(), "SELECT balance_cents,(SELECT count(*) FROM shop_orders WHERE user_id=$1 AND kind='renewal') FROM wallet_accounts WHERE user_id=$1", f.user).Scan(&balance, &orders); err != nil || balance != 4000 || orders != 1 {
		t.Fatal("manual/automatic race charged twice", balance, orders, err)
	}
}

func TestAutoRenewPurchaseFailureRollsBackWalletAndOrder(t *testing.T) {
	f, now := prepareAutoRenew(t)
	ctx := context.Background()
	if _, err := f.p.Exec(ctx, "UPDATE users SET plan_id=$2 WHERE id=$1", f.other, f.plan); err != nil {
		t.Fatal(err)
	}
	if _, err := f.p.Exec(ctx, `UPDATE users other SET subscription_managed=true,subscription_expires_at=source.subscription_expires_at,next_reset_at=source.next_reset_at,reset_anchor_at=source.reset_anchor_at,reset_index=1,auto_renew_enabled=true,auto_renew_cycle='monthly',auto_renew_price_cents=1000 FROM users source WHERE other.id=$1 AND source.id=$2`, f.other, f.user); err != nil {
		t.Fatal(err)
	}
	if _, err := f.p.Exec(ctx, "INSERT INTO wallet_accounts(user_id,balance_cents) VALUES($1,5000)", f.other); err != nil {
		t.Fatal(err)
	}
	name := "auto_renew_fail_" + Secret()[:12]
	if _, err := f.p.Exec(ctx, fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.id=%d THEN RAISE EXCEPTION 'fixture rollback'; END IF; RETURN NEW; END $$; CREATE TRIGGER %s BEFORE UPDATE OF quota_epoch ON users FOR EACH ROW EXECUTE FUNCTION %s()`, name, f.user, name, name)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		f.p.Exec(ctx, "DROP TRIGGER IF EXISTS "+name+" ON users")
		f.p.Exec(ctx, "DROP FUNCTION IF EXISTS "+name+"()")
	})
	if err := f.s.renewDueSubscriptions(ctx, now); err == nil {
		t.Fatal("injected failure was ignored")
	}
	var balance, orders, entries, epoch int64
	if err := f.p.QueryRow(ctx, `SELECT balance_cents,(SELECT count(*) FROM shop_orders WHERE user_id=$1),(SELECT count(*) FROM wallet_entries WHERE user_id=$1),(SELECT quota_epoch FROM users WHERE id=$1) FROM wallet_accounts WHERE user_id=$1`, f.user).Scan(&balance, &orders, &entries, &epoch); err != nil || balance != 5000 || orders != 0 || entries != 0 || epoch != 0 {
		t.Fatal("failed renewal left a partial purchase", balance, orders, entries, epoch, err)
	}
	if err := f.p.QueryRow(ctx, "SELECT balance_cents FROM wallet_accounts WHERE user_id=$1", f.other).Scan(&balance); err != nil || balance != 4000 {
		t.Fatal("one failed account prevented another account's renewal", balance, err)
	}
}
