package orchestration

import (
	"github.com/samclement/swhurl-platform/v2/adapters/helm"
	"github.com/samclement/swhurl-platform/v2/kernel"
)

// AcquireChart resolves the profile's named repository before any cache or network operation.
// The resulting local directory is a dependency for checks, never permission to install anything.
func AcquireChart(profile kernel.PlatformProfile, repositories []byte, cache string, acquirer helm.Acquirer) (string, error) {
	address, err := helm.Repository(repositories, profile.Chart.Repository)
	if err != nil {
		return "", err
	}
	return acquirer.Acquire(helm.Pin{Name: profile.Chart.Name, Version: profile.Chart.Version, Repository: address}, cache)
}
