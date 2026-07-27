//go:build !windows

package systemstats

import "errors"

func readHostSample() (hostSample, error) {
	return hostSample{}, errors.New("host resource sampling is unavailable on this platform")
}
