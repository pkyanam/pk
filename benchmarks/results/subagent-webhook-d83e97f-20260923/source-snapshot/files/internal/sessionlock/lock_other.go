//go:build !unix && !windows

package sessionlock

import "errors"

func lockPath(string) (func() error, bool, error) {
	return nil, false, errors.New("session leases are unsupported on this platform")
}
