package agent

import (
	"context"
	"errors"
	"math"
	"net"
	"sync"
	"time"

	"github.com/nekopass/nekopass/internal/billing"
	pb "github.com/nekopass/nekopass/internal/protocol"
)

type meteringKey struct{}
type billedConn struct {
	net.Conn
	rate int64
}

func (c *billedConn) CloseWrite() error {
	if v, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return v.CloseWrite()
	}
	return nil
}
func ruleRate(r *pb.Rule) int64 {
	if !r.GetMeteringConfigured() {
		return billing.Scale
	}
	return r.TrafficRate
}
func connectionRate(c net.Conn, r *pb.Rule) int64 {
	if v, ok := c.(*billedConn); ok {
		return v.rate
	}
	return ruleRate(r)
}

type trafficReservation struct {
	a         *Account
	n         int
	rate      int64
	unlimited bool
	once      sync.Once
}

// Charge allowance is reserved before writing. Fractional credit belongs to
// the account, not a connection, so opening many tiny streams cannot round
// each stream down or reuse one fraction concurrently.
func (a *Account) reserveTraffic(n int, rate int64, exact ...bool) (*trafficReservation, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.validLocked() || n < 0 || n > maxDatagram || rate < 0 || rate > 2*billing.MaxRate {
		return nil, ErrQuota
	}
	unlimited := a.policy.QuotaUnlimited
	if rate == 0 {
		return &trafficReservation{a: a, n: n, rate: rate, unlimited: unlimited}, nil
	}
	if a.policy.MeteringConfigured && a.policy.QuotaBlocked {
		return nil, ErrQuota
	}
	a.wanted = time.Now()
	cost := int64(n) * rate
	credit := &a.credit
	if unlimited {
		credit = &a.unlimitedCredit
	}
	needed := max(int64(0), (cost-a.weightCredit+billing.Scale-1)/billing.Scale)
	if len(exact) > 0 && exact[0] && !unlimited && *credit+a.disk.Issued-a.disk.Spent-a.disk.Released < needed {
		return nil, nil
	}
	if *credit < needed {
		block := max(int64(ReservationBlock), needed-*credit)
		if unlimited {
			block = min(block, math.MaxInt64-a.disk.Spent-a.disk.UnlimitedSpent)
			if block > 0 {
				a.disk.UnlimitedSpent += block
			}
		} else {
			block = min(block, a.disk.Issued-a.disk.Spent-a.disk.Released)
			if block > 0 {
				a.disk.Spent += block
			}
		}
		if block > 0 {
			if err := a.persist(); err != nil {
				return nil, err
			}
			*credit += block
		}
	}
	if *credit < needed { // Only multiply bounded values after the full request did not fit.
		n = int(min(int64(n), (*credit*billing.Scale+a.weightCredit)/rate))
		if n == 0 {
			return nil, nil
		}
		cost = int64(n) * rate
		needed = max(int64(0), (cost-a.weightCredit+billing.Scale-1)/billing.Scale)
	}
	*credit -= needed
	a.weightCredit += needed*billing.Scale - cost
	return &trafficReservation{a: a, n: n, rate: rate, unlimited: unlimited}, nil
}
func (a *Account) takeTraffic(ctx context.Context, n int, rate int64) (*trafficReservation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	reservation, err := a.reserveTraffic(n, rate)
	if reservation != nil || err != nil {
		return reservation, err
	}
	deadline := time.NewTimer(8 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-deadline.C:
			return nil, ErrQuota
		case <-ticker.C:
			reservation, err = a.reserveTraffic(n, rate)
			if reservation != nil || err != nil {
				return reservation, err
			}
		}
	}
}
func (r *trafficReservation) finish(written int) {
	if r == nil {
		return
	}
	r.once.Do(func() {
		a := r.a
		a.mu.Lock()
		defer a.mu.Unlock()
		written = max(0, min(written, r.n))
		refund := int64(r.n-written) * r.rate
		if r.unlimited == a.unlimitedMode {
			credit := &a.credit
			if r.unlimited {
				credit = &a.unlimitedCredit
			}
			refund += a.weightCredit
			*credit += refund / billing.Scale
			a.weightCredit = refund % billing.Scale
		}
		cost := int64(written) * r.rate
		charge := max(int64(0), (cost-a.disk.ChargeFraction+billing.Scale-1)/billing.Scale)
		if a.disk.Traffic > math.MaxInt64-int64(written) || a.disk.Charged > math.MaxInt64-charge {
			a.fatal = errors.New("traffic counter exhausted")
			return
		}
		a.disk.Traffic += int64(written)
		a.disk.Charged += charge
		a.disk.ChargeFraction += charge*billing.Scale - cost
	})
}
func (a *Account) beginMeter(rate int64) func() {
	if rate == 0 {
		return func() {}
	}
	a.paidActive.Add(1)
	a.mu.Lock()
	a.wanted = time.Now()
	a.mu.Unlock()
	return func() { a.paidActive.Add(-1) }
}

func (a *Account) reserveUDP(n int, rate int64) (*trafficReservation, error) {
	a.mu.Lock()
	if !a.validLocked() {
		a.mu.Unlock()
		return nil, ErrQuota
	}
	if a.limiter.Burst() < n {
		a.limiter.SetBurst(n)
	}
	allowed := a.limiter.AllowN(time.Now(), n)
	a.mu.Unlock()
	if !allowed {
		return nil, errUDPRate
	}
	r, err := a.reserveTraffic(n, rate, true)
	if err != nil {
		return nil, err
	}
	if r == nil {
		return nil, ErrQuota
	}
	if r.n != n {
		r.finish(0)
		return nil, ErrQuota
	}
	return r, nil
}

func (a *Account) billingAllowed(rate int64) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.validLocked() && (rate == 0 || !a.policy.MeteringConfigured || !a.policy.QuotaBlocked)
}
