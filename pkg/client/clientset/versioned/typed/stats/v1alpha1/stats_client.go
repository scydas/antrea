// Copyright 2024 Antrea Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Code generated by client-gen. DO NOT EDIT.

package v1alpha1

import (
	"net/http"

	v1alpha1 "antrea.io/antrea/pkg/apis/stats/v1alpha1"
	"antrea.io/antrea/pkg/client/clientset/versioned/scheme"
	rest "k8s.io/client-go/rest"
)

type StatsV1alpha1Interface interface {
	RESTClient() rest.Interface
	AntreaClusterNetworkPolicyStatsGetter
	AntreaNetworkPolicyStatsGetter
	MulticastGroupsGetter
	NetworkPolicyStatsGetter
	NodeLatencyStatsGetter
}

// StatsV1alpha1Client is used to interact with features provided by the stats.antrea.io group.
type StatsV1alpha1Client struct {
	restClient rest.Interface
}

func (c *StatsV1alpha1Client) AntreaClusterNetworkPolicyStats() AntreaClusterNetworkPolicyStatsInterface {
	return newAntreaClusterNetworkPolicyStats(c)
}

func (c *StatsV1alpha1Client) AntreaNetworkPolicyStats(namespace string) AntreaNetworkPolicyStatsInterface {
	return newAntreaNetworkPolicyStats(c, namespace)
}

func (c *StatsV1alpha1Client) MulticastGroups() MulticastGroupInterface {
	return newMulticastGroups(c)
}

func (c *StatsV1alpha1Client) NetworkPolicyStats(namespace string) NetworkPolicyStatsInterface {
	return newNetworkPolicyStats(c, namespace)
}

func (c *StatsV1alpha1Client) NodeLatencyStats() NodeLatencyStatsInterface {
	return newNodeLatencyStats(c)
}

// NewForConfig creates a new StatsV1alpha1Client for the given config.
// NewForConfig is equivalent to NewForConfigAndClient(c, httpClient),
// where httpClient was generated with rest.HTTPClientFor(c).
func NewForConfig(c *rest.Config) (*StatsV1alpha1Client, error) {
	config := *c
	if err := setConfigDefaults(&config); err != nil {
		return nil, err
	}
	httpClient, err := rest.HTTPClientFor(&config)
	if err != nil {
		return nil, err
	}
	return NewForConfigAndClient(&config, httpClient)
}

// NewForConfigAndClient creates a new StatsV1alpha1Client for the given config and http client.
// Note the http client provided takes precedence over the configured transport values.
func NewForConfigAndClient(c *rest.Config, h *http.Client) (*StatsV1alpha1Client, error) {
	config := *c
	if err := setConfigDefaults(&config); err != nil {
		return nil, err
	}
	client, err := rest.RESTClientForConfigAndClient(&config, h)
	if err != nil {
		return nil, err
	}
	return &StatsV1alpha1Client{client}, nil
}

// NewForConfigOrDie creates a new StatsV1alpha1Client for the given config and
// panics if there is an error in the config.
func NewForConfigOrDie(c *rest.Config) *StatsV1alpha1Client {
	client, err := NewForConfig(c)
	if err != nil {
		panic(err)
	}
	return client
}

// New creates a new StatsV1alpha1Client for the given RESTClient.
func New(c rest.Interface) *StatsV1alpha1Client {
	return &StatsV1alpha1Client{c}
}

func setConfigDefaults(config *rest.Config) error {
	gv := v1alpha1.SchemeGroupVersion
	config.GroupVersion = &gv
	config.APIPath = "/apis"
	config.NegotiatedSerializer = scheme.Codecs.WithoutConversion()

	if config.UserAgent == "" {
		config.UserAgent = rest.DefaultKubernetesUserAgent()
	}

	return nil
}

// RESTClient returns a RESTClient that is used to communicate
// with API server by this client implementation.
func (c *StatsV1alpha1Client) RESTClient() rest.Interface {
	if c == nil {
		return nil
	}
	return c.restClient
}
