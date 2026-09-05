package control

import (
	"errors"
	"net/url"
	"time"
)

// 时间范围的默认值与上限。
//
// 上限取 92 天：它覆盖「看上个季度」这个最常见的长跨度需求，同时把单次
// 查询触及的分区数压在 4 个以内（usage_records 按月分区）。没有这道闸，
// 一个手滑的 from=2020 就会让 ClickHouse 扫全表。
const (
	defaultRangeDays = 7
	maxRangeDays     = 92
)

var (
	errRangeInvalid  = errors.New("时间格式无法解析")
	errRangeInverted = errors.New("起始时间晚于结束时间")
	errRangeTooLong  = errors.New("时间跨度超过上限")
)

// parseRange 解析 from/to 参数。两者都缺省时取最近 defaultRangeDays 天。
//
// 接受两种写法：RFC3339（带时区的精确时刻）与 YYYY-MM-DD（当天零点 UTC）。
// 后者是人在 URL 里手写时的自然形式，不支持它会逼着用户去查时区偏移。
func parseRange(q url.Values) (from, to time.Time, err error) {
	now := time.Now().UTC()

	to, err = parseOneTime(q.Get("to"), now)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	from, err = parseOneTime(q.Get("from"), to.AddDate(0, 0, -defaultRangeDays))
	if err != nil {
		return time.Time{}, time.Time{}, err
	}

	if !from.Before(to) {
		return time.Time{}, time.Time{}, errRangeInverted
	}
	if to.Sub(from) > time.Duration(maxRangeDays)*24*time.Hour {
		return time.Time{}, time.Time{}, errRangeTooLong
	}
	return from, to, nil
}

func parseOneTime(raw string, fallback time.Time) (time.Time, error) {
	if raw == "" {
		return fallback, nil
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t.UTC(), nil
	}
	if t, err := time.Parse("2006-01-02", raw); err == nil {
		return t.UTC(), nil
	}
	return time.Time{}, errRangeInvalid
}
