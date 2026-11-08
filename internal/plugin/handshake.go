package plugin

import (
	"context"
	"errors"
	"fmt"

	sdkplugin "github.com/arafathusayn/herdsman/sdk/plugin"
)

// ErrNotApproved reports a plugin whose checksum no person has accepted.
var ErrNotApproved = errors.New("not approved")

// Start runs one plugin, after checking that its current checksum was approved.
// The SDK completes the handshake and checks the protocol major.
func (h *Host) Start(ctx context.Context, found Found) (*sdkplugin.Process, error) {
	approved, err := h.Approvals().Approved(found.Name, found.Checksum)
	if err != nil {
		return nil, err
	}
	if !approved {
		return nil, fmt.Errorf("%w: run herdsman plugin install %s on a terminal", ErrNotApproved, found.Name)
	}
	return sdkplugin.Host{Name: found.Name, Timeout: h.Timeout, Env: h.Env}.Start(ctx, found.Path)
}
