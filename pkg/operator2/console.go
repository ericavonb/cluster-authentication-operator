package operator2

import (
	"net/url"
	"regexp"

	"github.com/golang/glog"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	configv1 "github.com/openshift/api/config/v1"
)

func (c *authOperator) handleConsoleConfig() (*configv1.Console, error) {
	// technically this should be an observed config loop
	return c.console.Get(consoleConfigName, metav1.GetOptions{})
}

func consoleToDeploymentData(console *configv1.Console) (string, []string) {
	host := console.Status.PublicHostname

	if len(host) == 0 {
		return "", nil
	}

	assetPublicURL := "https://" + host  // needs to be a valid URL
	corsAllowedOrigins := []string{host} // needs to be valid regexps

	if _, err := url.Parse(assetPublicURL); err != nil { // should never happen
		glog.Errorf("failed to parse assetPublicURL %s: %v", assetPublicURL, err)
		return "", nil
	}
	for _, corsAllowedOrigin := range corsAllowedOrigins {
		if _, err := regexp.Compile(corsAllowedOrigin); err != nil { // also should never happen
			glog.Errorf("failed to parse corsAllowedOrigin %s: %v", corsAllowedOrigin, err)
			return "", nil
		}
	}

	// the console in 4.0 does not need CORS to interact with the OAuth server
	// we will leave all of the wiring in place in case we need to revisit this in the future
	return assetPublicURL, nil // corsAllowedOrigins
}
