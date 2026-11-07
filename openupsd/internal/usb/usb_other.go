//go:build !linux

package usb

import (
	"context"
	"errors"
)

// OpenUSB is only implemented on Linux (usbfs).
func OpenUSB(ctx context.Context, id DeviceID, index int) (Dev, error) {
	return nil, errors.New("usb: usbfs transport is only available on Linux")
}
