package operator2

import (
	"errors"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	configv1 "github.com/openshift/api/config/v1"
	configv1client "github.com/openshift/client-go/config/clientset/versioned/typed/config/v1"
)

// TODO make static or replace globals with input parameters
func defaultAuth() *configv1.Authentication {
	return &configv1.Authentication{
		ObjectMeta: metav1.ObjectMeta{
			Name:   configName,
			Labels: defaultLabels(),
			Annotations: map[string]string{
				// TODO - better annotations & messaging to user about defaulting behavior
				"message": "Default Authentication created by cluster-authentication-operator",
			},
		},
		Spec: configv1.AuthenticationSpec{
			Type: configv1.AuthenticationTypeIntegratedOAuth,
		},
		Status: configv1.AuthenticationStatus{
			IntegratedOAuthMetadata: configv1.ConfigMapNameReference{
				Name: targetName,
			},
		},
	}
}

func findOrCreateAuth(authClient configv1client.AuthenticationInterface, auth *configv1.Authentication) (*configv1.Authentication, error) {
	// Parameter validation
	if authClient == nil {
		return nil, errors.New("invalid AuthenticationInterface client: <nil>")
	}
	if auth == nil {
		return nil, errors.New("invalid auth paramenter: <nil>")
	}

	// Fetch any existing Authentication instance
	existing, err := authClient.Get(auth.GetName(), metav1.GetOptions{})
	if err != nil {
		if !apierrors.IsNotFound(err) {
			// Unknown error from api
			return nil, err
		}
		// No existing instance found; attempt to create default
		created, err := authClient.Create(auth)
		if err != nil {
			if !apierrors.IsAlreadyExists(err) {
				// Unknown error from api
				return nil, err
			}
			// An Authentication instance must have been created between when we
			// first checked and when we attempted to create the default.
			// Find the existing instance, returning any errors trying to fetch it
			return authClient.Get(auth.GetName(), metav1.GetOptions{})
		}
		// Default successfully created - return the new Authentication instance
		return created, err
	}
	// Existing Authentication instance found. Return it
	return existing, nil
}
func (c *osinOperator) fetchAuthConfig() (*configv1.Authentication, error) {
	return findOrCreateAuth(c.authentication, defaultAuth())
}
func (c *osinOperator) updateAuthStatus(auth *configv1.Authentication) (*configv1.Authentication, error) {
	if auth == nil {
		return nil, nil
	}
	var expectedRef configv1.ConfigMapNameReference
	switch auth.Spec.Type {
	case configv1.AuthenticationTypeNone:
		expectedRef = auth.Spec.OAuthMetadata
	case configv1.AuthenticationTypeIntegratedOAuth:
		expectedRef = configv1.ConfigMapNameReference{
			Name: targetName,
		}
	default:
		return nil, fmt.Errorf("unknown AuthenticationType '%s'", auth.Spec.Type)
	}

	if auth.Status.IntegratedOAuthMetadata != expectedRef {
		auth.Status.IntegratedOAuthMetadata = expectedRef
		return c.authentication.UpdateStatus(auth)
	}
	return auth, nil
}
