package control

import (
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestParseRangeDefaultsToLastSevenDays(t *testing.T) {
	from, to, err := parseRange(url.Values{})
	require.NoError(t, err)
	require.WithinDuration(t, time.Now(), to, time.Minute)
	require.WithinDuration(t, time.Now().AddDate(0, 0, -7), from, time.Minute)
}

func TestParseRangeAcceptsExplicitDates(t *testing.T) {
	from, to, err := parseRange(url.Values{
		"from": {"2026-09-01"}, "to": {"2026-09-08"},
	})
	require.NoError(t, err)
	require.Equal(t, 2026, from.Year())
	require.Equal(t, time.September, from.Month())
	require.Equal(t, 1, from.Day())
	require.Equal(t, 8, to.Day())
}

func TestParseRangeRejectsTooLongSpan(t *testing.T) {
	// ClickHouse 上的无界扫描要付真金白银的代价，闸放在入口。
	_, _, err := parseRange(url.Values{
		"from": {"2026-01-01"}, "to": {"2026-06-01"},
	})
	require.ErrorIs(t, err, errRangeTooLong)
}

func TestParseRangeRejectsInvertedRange(t *testing.T) {
	_, _, err := parseRange(url.Values{
		"from": {"2026-09-08"}, "to": {"2026-09-01"},
	})
	require.ErrorIs(t, err, errRangeInverted)
}

func TestParseRangeRejectsUnparsableDate(t *testing.T) {
	_, _, err := parseRange(url.Values{"from": {"上周"}})
	require.ErrorIs(t, err, errRangeInvalid)
}
