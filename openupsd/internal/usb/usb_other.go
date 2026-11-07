//go:build !linux

package usb

import (
	"context"
	"errors"
)

// OpenHID is only implemented on Linux (hidraw).
func OpenHID(ctx context.Context, id DeviceID, index int) (Dev, error) {
	return nil, errors.New("usb: hidraw transport is only available on Linux")
}

func OpenUSB(ctx context.Context, id DeviceID, index int) (Dev, error) {
	return nil, errors.New("usb: usbfs transport is only available on Linux")
}
