package license

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTrialIsFiveSeatsWithoutExpiry(t *testing.T) {
	lic := Trial()

	require.Equal(t, StatusTrial, lic.Status)
	require.Equal(t, 5, lic.Seats)
	require.True(t, lic.ExpiresAt.IsZero(), "试用额度不设到期，ExpiresAt 应为零值")
	require.Empty(t, lic.Customer)
}
