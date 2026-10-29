package billing

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

const Scale int64 = 10000
const MaxRate int64 = 1000 * Scale

type Rate int64

func Parse(raw string) (Rate, error) {
	raw = strings.TrimSpace(raw)
	parts := strings.Split(raw, ".")
	if len(parts) > 2 || len(parts[0]) == 0 || len(parts[0]) > 4 {
		return 0, errors.New("倍率须为 0–1000 的数值，最多四位小数")
	}
	for _, p := range parts {
		for _, c := range p {
			if c < '0' || c > '9' {
				return 0, errors.New("倍率格式无效")
			}
		}
	}
	whole, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return 0, err
	}
	var fraction int64
	if len(parts) == 2 {
		if len(parts[1]) == 0 || len(parts[1]) > 4 {
			return 0, errors.New("倍率最多四位小数")
		}
		fraction, _ = strconv.ParseInt(parts[1]+strings.Repeat("0", 4-len(parts[1])), 10, 64)
	}
	n := whole*Scale + fraction
	if n > MaxRate {
		return 0, errors.New("倍率不能超过 1000")
	}
	return Rate(n), nil
}
func (r Rate) String() string {
	v := int64(r)
	if v%Scale == 0 {
		return strconv.FormatInt(v/Scale, 10)
	}
	return fmt.Sprintf("%d.%s", v/Scale, strings.TrimRight(fmt.Sprintf("%04d", v%Scale), "0"))
}
func (r Rate) MarshalJSON() ([]byte, error) { return json.Marshal(r.String()) }
func (r *Rate) UnmarshalJSON(b []byte) error {
	raw := string(b)
	if len(b) > 0 && b[0] == '"' {
		if err := json.Unmarshal(b, &raw); err != nil {
			return err
		}
	}
	v, err := Parse(raw)
	if err == nil {
		*r = v
	}
	return err
}
