package domotz_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// Confirms the address fields the query editor searches on are actually
// populated upstream, not merely present in the contract.
func TestLive_DevicesReportAddresses(t *testing.T) {
	client := liveClient(t)
	ctx := context.Background()

	agents, err := client.Agents(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, agents)

	devices, err := client.Devices(ctx, agents[0].ID)
	require.NoError(t, err)
	require.NotEmpty(t, devices)

	var withIP, withMAC int
	for _, d := range devices {
		if d.PrimaryIP() != "" {
			withIP++
		}
		if d.HWAddress != "" {
			withMAC++
		}
	}
	t.Logf("collector %d: %d/%d devices report an IP, %d/%d report a MAC",
		agents[0].ID, withIP, len(devices), withMAC, len(devices))

	require.Positive(t, withIP, "no device reported an IP - the field name is wrong")
	require.Positive(t, withMAC, "no device reported a MAC - the field name is wrong")
}
