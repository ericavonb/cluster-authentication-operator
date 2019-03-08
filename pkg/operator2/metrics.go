package operator2

import (
	"errors"
	monitoringv1 "github.com/coreos/prometheus-operator/pkg/apis/monitoring/v1"
	"k8s.io/api/core/v1"
	apiextensionsv1beta1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func (c *authOperator) handleServiceMonitor(svc *v1.Service) (*monitoringv1.ServiceMonitor, err) {
	// Do nothing if no service provided
	if svc == nil {
		glog.V(5).Infof("not creating ServiceMonitor: <nil> service")
		return nil, nil
	}
	// Fetch any existing ServiceMonitor instance
	glog.V(5).Infof("fetching ServiceMonitor %s/%s", svc.GetNamespace(), svc.GetName())
	existing, err := c.servicemonitors(svc.GetNamespace()).Get(svc.Name())

	if apierrors.IsNotFound(err) {
		// No existing instance found; attempt to create one
		glog.V(5).Infof("creating ServiceMonitor '%s/%s'", svc.GetNamespace(), svc.GetName())
		created, err := c.servicemonitors(svc.GetNamespace()).Create(serviceMonitorForService(svc))

		if apierrors.IsAlreadyExists(err) {
			// A ServiceMonitor must have been created between when we first checked and
			// when we attempted to create one.
			// Find the existing instance, returning any errors trying to fetch it
			glog.V(5).Infof("re-fetching servicemonitor '%s/%s'", svc.GetNamespace(), svc.GetName())
			return c.servicemonitors(svc.GetNamespace()).Get(svc.Name())
		}
		// ServiceMonitor successfully created, or unknown error
		return created, err
	}

	// Existing instance found, or unknown error
	return existing, err
}

// serviceMonitorForService creates a prometheus-operator ServiceMonitor object
// for scraping metrics from the passed in service
func serviceMonitorForService(s *v1.Service) *monitoringv1.ServiceMonitor {
	if s == nil {
		return nil
	}

	labels := defaultLabels()
	for k, v := range s.GetLabels() {
		labels[k] = v
	}

	return &monitoringv1.ServiceMonitor{
		TypeMeta: metav1.TypeMeta{
			Kind:       monitoringv1.ServiceMonitorsKind,
			APIVersion: monitoringv1.SchemeGroupVersion.String(),
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      s.GetName(),
			Namespace: monitoringNamespace,
			Labels:    labels,
		},
		Spec: monitoringv1.ServiceMonitorSpec{
			NamespaceSelector: monitoringv1.NamespaceSelector{
				MatchNames: []string{s.GetNamespace()},
			},
			Selector: metav1.LabelSelector{
				MatchLabels: labels,
			},
			Endpoints: []monitoringv1.Endpoint{
				{
					BearerTokenFile: "/var/run/secrets/kubernetes.io/serviceaccount/token",
					Interval:        "30s",
					Port:            s.Spec.Ports[0].Name,
					Scheme:          "https",
					Path:            "/metrics",
					TLSConfig: &monitoringv1.TLSConfig{
						CAFile:     "/etc/prometheus/configmaps/serving-certs-ca-bundle/service-ca.crt",
						ServerName: fmt.Sprintf("%s.%s.svc", s.GetName(), s.GetNamespace()),
					},
				},
			},
		},
	}
}
