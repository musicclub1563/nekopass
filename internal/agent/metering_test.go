package agent

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	"github.com/nekopass/nekopass/internal/billing"
	pb "github.com/nekopass/nekopass/internal/protocol"
)

func meteredAccount(t *testing.T, issued int64) *Account {
	t.Helper()
	state, err := OpenState(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { state.Close() })
	a := newAccount(1, DiskUser{}, state)
	if err = a.configure(&pb.UserPolicy{Id: 1, Enabled: true, Issued: issued, MeteringConfigured: true}); err != nil {
		t.Fatal(err)
	}
	return a
}
func TestMeteredTrafficDifferentRatesAndFractionalCredit(t *testing.T) {
	for _, rate := range []int64{0, 1, 1000, 5000, 10000, 15000, 25000, 20000000} {
		a := meteredAccount(t, 1000000000)
		for range 100 {
			r, err := a.reserveTraffic(1, rate)
			if err != nil || r == nil {
				t.Fatal("reserve failed", rate, err)
			}
			r.finish(1)
		}
		want := (100*rate + billing.Scale - 1) / billing.Scale
		if a.disk.Traffic != 100 || a.disk.Charged != want || a.disk.ChargeFraction < 0 || a.disk.ChargeFraction >= billing.Scale {
			t.Fatal("raw and billed traffic mixed or rounding drift", rate, a.disk)
		}
	}
	// A one-byte quota can pay for ten individual bytes on a 0.1x route.
	a := meteredAccount(t, 1)
	for range 10 {
		r, err := a.reserveTraffic(1, 1000)
		if err != nil || r == nil {
			t.Fatal("lost already paid fractional allowance", err)
		}
		r.finish(1)
	}
	if a.disk.Charged != 1 || a.disk.Traffic != 10 {
		t.Fatal(a.disk)
	}
	if r, _ := a.reserveTraffic(1, 1000); r != nil {
		r.finish(0)
		t.Fatal("finite allowance overspent")
	}
}
func TestMeteredConcurrentReservationsDoNotOverspend(t *testing.T) {
	a := meteredAccount(t, 100)
	var wg sync.WaitGroup
	for range 200 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := a.reserveTraffic(1, 15000)
			if err != nil {
				t.Error(err)
				return
			}
			if r != nil {
				r.finish(1)
			}
		}()
	}
	wg.Wait()
	if a.disk.Traffic != 66 || a.disk.Charged != 99 || a.disk.Spent > 100 {
		t.Fatal("concurrent weighted allowance escaped reservation", a.disk)
	}
}
func TestMeteredPartialWriteRefundAndFreeAfterQuotaBlocked(t *testing.T) {
	a := meteredAccount(t, 1000)
	r, err := a.reserveTraffic(100, 15000)
	if err != nil || r == nil {
		t.Fatal(err)
	}
	r.finish(10)
	r.finish(10)
	if a.disk.Traffic != 10 || a.disk.Charged != 15 {
		t.Fatal("reservation completion duplicated or charged unwritten payload", a.disk)
	}
	r, err = a.reserveTraffic(650, 15000)
	if err != nil || r == nil || r.n != 650 {
		t.Fatal("unwritten reservation was not refunded", err)
	}
	r.finish(650)
	if a.disk.Charged != 990 {
		t.Fatal(a.disk)
	}
	policy := &pb.UserPolicy{Id: 1, Enabled: true, Issued: 1000, MeteringConfigured: true, QuotaBlocked: true}
	if err = a.configure(policy); err != nil {
		t.Fatal(err)
	}
	if _, err = a.reserveTraffic(1, 10000); err == nil {
		t.Fatal("paid route worked after quota blocked")
	}
	r, err = a.takeTraffic(context.Background(), 12345, 0)
	if err != nil || r == nil {
		t.Fatal("free route stopped with quota exhausted", err)
	}
	r.finish(12345)
	if a.disk.Charged != 990 || a.disk.Traffic != 13005 {
		t.Fatal("free data was charged", a.disk)
	}
	policy.Enabled = false
	a.configure(policy)
	if _, err = a.reserveTraffic(1, 0); err == nil {
		t.Fatal("disabled account used free forwarding")
	}
}
func TestMeteredStateReloadAllowsRawTrafficAboveQuotaSpend(t *testing.T) {
	a := meteredAccount(t, 0)
	r, err := a.reserveTraffic(5000, 0)
	if err != nil {
		t.Fatal(err)
	}
	r.finish(5000)
	if err = a.persist(); err != nil {
		t.Fatal(err)
	}
	loaded, err := a.state.LoadUsers()
	if err != nil || loaded[1].Traffic != 5000 || loaded[1].Charged != 0 || !loaded[1].Metered {
		t.Fatal("free traffic state rejected or converted into charged traffic", loaded, err)
	}
}
