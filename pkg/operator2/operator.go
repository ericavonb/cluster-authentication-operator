package operator2

import (
	"strings"

	"github.com/golang/glog"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	appsv1client "k8s.io/client-go/kubernetes/typed/apps/v1"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"

	operatorv1 "github.com/openshift/api/operator/v1"
	configclient "github.com/openshift/client-go/config/clientset/versioned"
	configv1client "github.com/openshift/client-go/config/clientset/versioned/typed/config/v1"
	configinformer "github.com/openshift/client-go/config/informers/externalversions"
	authopclient "github.com/openshift/client-go/operator/clientset/versioned/typed/operator/v1"
	authopinformer "github.com/openshift/client-go/operator/informers/externalversions/operator/v1"
	routeclient "github.com/openshift/client-go/route/clientset/versioned/typed/route/v1"
	routeinformer "github.com/openshift/client-go/route/informers/externalversions/route/v1"
	"github.com/openshift/cluster-authentication-operator/pkg/boilerplate/controller"
	"github.com/openshift/cluster-authentication-operator/pkg/boilerplate/operator"
	"github.com/openshift/library-go/pkg/operator/events"
	"github.com/openshift/library-go/pkg/operator/resource/resourceapply"
	"github.com/openshift/library-go/pkg/operator/resourcesynccontroller"
)

const (
	targetName        = "openshift-authentication"
	globalConfigName  = "cluster"
	consoleConfigName = "console"

	machineConfigNamespace = "openshift-config-managed"
	userConfigNamespace    = "openshift-config"

	systemConfigPath           = "/var/config/system"
	systemConfigPathConfigMaps = systemConfigPath + "/configmaps"
	systemConfigPathSecrets    = systemConfigPath + "/secrets"

	// if one day we ever need to come up with something else, we can still find the old secrets and config maps
	versionPrefix = "v4-0-"

	configVersionPrefix = versionPrefix + "config-"

	// secrets and config maps that we manually managed have this prefix
	systemConfigPrefix = configVersionPrefix + "system-"

	// secrets and config maps synced from openshift-config into our namespace have this prefix
	userConfigPrefix = configVersionPrefix + "user-"

	sessionNameAndKey = systemConfigPrefix + "session"
	sessionMount      = systemConfigPathSecrets + "/" + sessionNameAndKey
	sessionPath       = sessionMount + "/" + sessionNameAndKey

	serviceCABase  = "service-ca"
	serviceCAName  = systemConfigPrefix + serviceCABase
	serviceCAKey   = serviceCABase + ".crt"
	serviceCAMount = systemConfigPathConfigMaps + "/" + serviceCAName
	serviceCAPath  = serviceCAMount + "/" + serviceCAKey

	servingCertName     = systemConfigPrefix + "serving-cert"
	servingCertMount    = systemConfigPathSecrets + "/" + servingCertName
	servingCertPathCert = servingCertMount + "/" + corev1.TLSCertKey
	servingCertPathKey  = servingCertMount + "/" + corev1.TLSPrivateKeyKey

	cliConfigNameAndKey = systemConfigPrefix + "cliconfig"
	cliConfigMount      = systemConfigPathConfigMaps + "/" + cliConfigNameAndKey
	cliConfigPath       = cliConfigMount + "/" + cliConfigNameAndKey

	userConfigPath = "/var/config/user"

	servicePort   = 443
	containerPort = 6443
)

type authOperator struct {
	authOperatorConfig authopclient.AuthenticationInterface

	recorder events.Recorder

	route routeclient.RouteInterface

	services    corev1client.ServicesGetter
	secrets     corev1client.SecretsGetter
	configMaps  corev1client.ConfigMapsGetter
	deployments appsv1client.DeploymentsGetter

	authentication configv1client.AuthenticationInterface
	oauth          configv1client.OAuthInterface
	console        configv1client.ConsoleInterface

	resourceSyncer resourcesynccontroller.ResourceSyncer
}

func NewAuthenticationOperator(
	authOpConfigInformer authopinformer.AuthenticationInformer,
	authOpConfigClient authopclient.AuthenticationsGetter,
	kubeInformersNamespaced informers.SharedInformerFactory,
	kubeClient kubernetes.Interface,
	routeInformer routeinformer.RouteInformer,
	routeClient routeclient.RouteV1Interface,
	configInformers configinformer.SharedInformerFactory,
	configClient configclient.Interface,
	recorder events.Recorder,
	resourceSyncer resourcesynccontroller.ResourceSyncer,
) operator.Runner {
	c := &authOperator{
		authOperatorConfig: authOpConfigClient.Authentications(),

		recorder: recorder,

		route: routeClient.Routes(targetName),

		services:    kubeClient.CoreV1(),
		secrets:     kubeClient.CoreV1(),
		configMaps:  kubeClient.CoreV1(),
		deployments: kubeClient.AppsV1(),

		authentication: configClient.ConfigV1().Authentications(),
		oauth:          configClient.ConfigV1().OAuths(),
		console:        configClient.ConfigV1().Consoles(),

		resourceSyncer: resourceSyncer,
	}

	coreInformers := kubeInformersNamespaced.Core().V1()
	configV1Informers := configInformers.Config().V1()

	targetNameFilter := operator.FilterByNames(targetName)
	configNameFilter := operator.FilterByNames(globalConfigName)
	prefixFilter := getPrefixFilter()

	return operator.New("AuthenticationOperator2", c,
		operator.WithInformer(routeInformer, targetNameFilter),
		operator.WithInformer(coreInformers.Services(), targetNameFilter),
		operator.WithInformer(kubeInformersNamespaced.Apps().V1().Deployments(), targetNameFilter),

		operator.WithInformer(coreInformers.Secrets(), prefixFilter),
		operator.WithInformer(coreInformers.ConfigMaps(), prefixFilter),

		operator.WithInformer(authOpConfigInformer, configNameFilter),
		operator.WithInformer(configV1Informers.Authentications(), configNameFilter),
		operator.WithInformer(configV1Informers.OAuths(), configNameFilter),
		operator.WithInformer(configV1Informers.Consoles(), configNameFilter, controller.WithNoSync()),
	)
}

func (c *authOperator) Key() (metav1.Object, error) {
	return c.authOperatorConfig.Get(globalConfigName, metav1.GetOptions{})
}

func (c *authOperator) Sync(obj metav1.Object) error {
	operatorConfig := obj.(*operatorv1.Authentication)

	if operatorConfig.Spec.ManagementState != operatorv1.Managed {
		return nil // TODO do something better for all states
	}

	if err := c.handleSync(operatorConfig); err != nil {
		return err
	}

	// TODO update states and handle ClusterOperator spec/status

	return nil
}

func (c *authOperator) handleSync(operatorConfig *operatorv1.Authentication) error {
	// we get resource versions so that if either changes, we redeploy our payload
	resourceVersions := []string{operatorConfig.GetResourceVersion()}

	route, err := c.handleRoute()
	if err != nil {
		return err
	}
	resourceVersions = append(resourceVersions, route.GetResourceVersion())

	serviceCA, err := c.handleServiceCA()
	if err != nil {
		return err
	}
	resourceVersions = append(resourceVersions, serviceCA.GetResourceVersion())

	metadata, _, err := resourceapply.ApplyConfigMap(c.configMaps, c.recorder, getMetadataConfigMap(route))
	if err != nil {
		return err
	}
	resourceVersions = append(resourceVersions, metadata.GetResourceVersion())

	authConfig, err := c.handleAuthConfig()
	if err != nil {
		return err
	}
	resourceVersions = append(resourceVersions, authConfig.GetResourceVersion())

	service, _, err := resourceapply.ApplyService(c.services, c.recorder, defaultService())
	if err != nil {
		return err
	}
	resourceVersions = append(resourceVersions, service.GetResourceVersion())

	expectedSessionSecret, err := c.expectedSessionSecret()
	if err != nil {
		return err
	}
	sessionSecret, _, err := resourceapply.ApplySecret(c.secrets, c.recorder, expectedSessionSecret)
	if err != nil {
		return err
	}
	resourceVersions = append(resourceVersions, sessionSecret.GetResourceVersion())

	consoleConfig, err := c.handleConsoleConfig()
	if err != nil {
		return err
	}
	resourceVersions = append(resourceVersions, consoleConfig.GetResourceVersion())

	oauthConfig, expectedCLIconfig, syncData, err := c.handleOAuthConfig(operatorConfig, route, service, consoleConfig)
	if err != nil {
		return err
	}
	resourceVersions = append(resourceVersions, oauthConfig.GetResourceVersion())

	configResourceVersions, err := c.handleConfigSync(syncData)
	if err != nil {
		return err
	}
	resourceVersions = append(resourceVersions, configResourceVersions...)

	cliConfig, _, err := resourceapply.ApplyConfigMap(c.configMaps, c.recorder, expectedCLIconfig)
	if err != nil {
		return err
	}
	resourceVersions = append(resourceVersions, cliConfig.GetResourceVersion())

	// deployment, have RV of all resources
	// TODO use ExpectedDeploymentGeneration func
	// TODO we also need the RV for the serving-cert secret (servingCertName)
	expectedDeployment := defaultDeployment(
		operatorConfig,
		syncData,
		resourceVersions...,
	)
	// TODO add support for spec.operandSpecs.unsupportedResourcePatches, like:
	// operatorConfig.Spec.OperandSpecs[...].UnsupportedResourcePatches[...].Patch
	deployment, _, err := resourceapply.ApplyDeployment(c.deployments, c.recorder, expectedDeployment, c.getGeneration(), false)
	if err != nil {
		return err
	}

	glog.V(4).Infof("current deployment: %#v", deployment)

	return nil
}

func defaultLabels() map[string]string {
	return map[string]string{
		"app": targetName,
	}
}

func defaultMeta() metav1.ObjectMeta {
	return metav1.ObjectMeta{
		Name:            targetName,
		Namespace:       targetName,
		Labels:          defaultLabels(),
		Annotations:     map[string]string{},
		OwnerReferences: nil, // TODO
	}
}

func getPrefixFilter() controller.Filter {
	names := operator.FilterByNames(targetName)
	prefix := func(obj metav1.Object) bool { // TODO add helper to combine filters
		return names.Add(obj) || strings.HasPrefix(obj.GetName(), configVersionPrefix)
	}
	return controller.FilterFuncs{
		AddFunc: prefix,
		UpdateFunc: func(oldObj, newObj metav1.Object) bool {
			return prefix(newObj)
		},
		DeleteFunc: prefix,
	}
}
