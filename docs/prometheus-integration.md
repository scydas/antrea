# Prometheus Integration

## Purpose

Prometheus server can monitor various metrics and provide an observation of the
Antrea Controller and Agent components. The doc provides general guidelines to
the configuration of Prometheus server to operate with the Antrea components.

## About Prometheus

[Prometheus](https://prometheus.io/) is an open source monitoring and alerting
server. Prometheus is capable of collecting metrics from various Kubernetes
components, storing and providing alerts.
Prometheus can provide visibility by integrating with other products such as
[Grafana](https://grafana.com/).

One of Prometheus capabilities is self-discovery of Kubernetes services which
expose their metrics. So Prometheus can scrape the metrics of any additional
components which are added to the cluster without further configuration changes.

## Antrea Configuration

Enable Prometheus metrics listener by setting `enablePrometheusMetrics`
parameter to true in the Controller and the Agent configurations.

## Prometheus Configuration

### Prometheus version

Prometheus integration with Antrea is validated as part of CI using Prometheus v2.46.0.

### Prometheus RBAC

Prometheus requires access to Kubernetes API resources for the service discovery
capability. Reading metrics also requires access to the "/metrics" API
endpoints.

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: prometheus
rules:
- apiGroups: [""]
  resources:
  - nodes
  - nodes/proxy
  - services
  - endpoints
  - pods
  verbs: ["get", "list", "watch"]
- apiGroups:
  - networking.k8s.io
  resources:
  - ingresses
  verbs: ["get", "list", "watch"]
- nonResourceURLs: ["/metrics"]
  verbs: ["get"]
```

### Antrea Metrics Listener Access

To scrape the metrics from Antrea Controller and Agent, Prometheus needs the
following permissions

```yaml
kind: ClusterRole
apiVersion: rbac.authorization.k8s.io/v1
metadata:
  name: prometheus-antrea
rules:
- nonResourceURLs:
  - /metrics
  verbs:
  - get
```

### Antrea Components Scraping configuration

Add the following jobs to Prometheus scraping configuration to enable metrics
collection from Antrea components. Antrea Agent metrics endpoint is exposed through
Antrea apiserver on `apiport` config parameter given in `antrea-agent.conf` (default
value is 10350). Antrea Controller metrics endpoint is exposed through Antrea apiserver
on `apiport` config parameter given in `antrea-controller.conf` (default value is 10349).

#### Controller Scraping

```yaml
- job_name: 'antrea-controllers'
kubernetes_sd_configs:
- role: endpoints
scheme: https
tls_config:
  ca_file: /var/run/secrets/kubernetes.io/serviceaccount/ca.crt
  insecure_skip_verify: true
bearer_token_file: /var/run/secrets/kubernetes.io/serviceaccount/token
relabel_configs:
- source_labels: [__meta_kubernetes_namespace, __meta_kubernetes_pod_container_name]
  action: keep
  regex: kube-system;antrea-controller
- source_labels: [__meta_kubernetes_pod_node_name, __meta_kubernetes_pod_name]
  target_label: instance
```

#### Agent Scraping

```yaml
- job_name: 'antrea-agents'
kubernetes_sd_configs:
- role: pod
scheme: https
tls_config:
  ca_file: /var/run/secrets/kubernetes.io/serviceaccount/ca.crt
  insecure_skip_verify: true
bearer_token_file: /var/run/secrets/kubernetes.io/serviceaccount/token
relabel_configs:
- source_labels: [__meta_kubernetes_namespace, __meta_kubernetes_pod_container_name]
  action: keep
  regex: kube-system;antrea-agent
- source_labels: [__meta_kubernetes_pod_node_name, __meta_kubernetes_pod_name]
  target_label: instance
```

For further reference see the enclosed
[configuration file](../build/yamls/antrea-prometheus.yml).

The configuration file above can be used to deploy Prometheus Server with
scraping configuration for Antrea services.
To deploy this configuration use
`kubectl apply -f build/yamls/antrea-prometheus.yml`

## Antrea Prometheus Metrics

Antrea Controller and Agents expose various metrics, some of which are provided
by the Antrea components and others which are provided by 3rd party components
used by the Antrea components.

Below is a list of metrics, provided by the components and by 3rd parties.

### Antrea Metrics

#### Antrea Agent Metrics

- **antrea_agent_conntrack_antrea_connection_count:** Number of connections
in the Antrea ZoneID of the conntrack table. This metric gets updated at
an interval specified by flowPollInterval, a configuration parameter for
the Agent.
- **antrea_agent_conntrack_max_connection_count:** Size of the conntrack
table. This metric gets updated at an interval specified by flowPollInterval,
a configuration parameter for the Agent.
- **antrea_agent_conntrack_total_connection_count:** Number of connections
in the conntrack table. This metric gets updated at an interval specified
by flowPollInterval, a configuration parameter for the Agent.
- **antrea_agent_denied_connection_count:** Number of denied connections
detected by Flow Exporter deny connections tracking. This metric gets updated
when a flow is rejected/dropped by network policy.
- **antrea_agent_egress_networkpolicy_rule_count:** Number of egress
NetworkPolicy rules on local Node which are managed by the Antrea Agent.
- **antrea_agent_flow_collector_reconnection_count:** Number of re-connections
between Flow Exporter and flow collector. This metric gets updated whenever
the connection is re-established between the Flow Exporter and the flow
collector (e.g. the Flow Aggregator).
- **antrea_agent_ingress_networkpolicy_rule_count:** Number of ingress
NetworkPolicy rules on local Node which are managed by the Antrea Agent.
- **antrea_agent_local_pod_count:** Number of Pods on local Node which are
managed by the Antrea Agent.
- **antrea_agent_networkpolicy_count:** Number of NetworkPolicies on local
Node which are managed by the Antrea Agent.
- **antrea_agent_ovs_flow_count:** Flow count for each OVS flow table. The
TableID and TableName are used as labels.
- **antrea_agent_ovs_flow_ops_count:** Number of OVS flow operations,
partitioned by operation type (add, modify and delete).
- **antrea_agent_ovs_flow_ops_error_count:** Number of OVS flow operation
errors, partitioned by operation type (add, modify and delete).
- **antrea_agent_ovs_flow_ops_latency_milliseconds:** The latency of OVS
flow operations, partitioned by operation type (add, modify and delete).
- **antrea_agent_ovs_meter_packet_dropped_count:** Number of packets dropped by
OVS meter. The value is greater than 0 when the packets exceed the rate-limit.
- **antrea_agent_ovs_total_flow_count:** Total flow count of all OVS flow
tables.

#### Antrea Controller Metrics

- **antrea_controller_acnp_status_updates:** The total number of actual
status updates performed for Antrea ClusterNetworkPolicy Custom Resources
- **antrea_controller_address_group_processed:** The total number of
address-group processed
- **antrea_controller_address_group_sync_duration_milliseconds:** The duration
of syncing address-group
- **antrea_controller_annp_status_updates:** The total number of actual
status updates performed for Antrea NetworkPolicy Custom Resources
- **antrea_controller_applied_to_group_processed:** The total number of
applied-to-group processed
- **antrea_controller_applied_to_group_sync_duration_milliseconds:** The
duration of syncing applied-to-group
- **antrea_controller_length_address_group_queue:** The length of
AddressGroupQueue
- **antrea_controller_length_applied_to_group_queue:** The length of
AppliedToGroupQueue
- **antrea_controller_length_network_policy_queue:** The length of
InternalNetworkPolicyQueue
- **antrea_controller_network_policy_processed:** The total number of
internal-networkpolicy processed
- **antrea_controller_network_policy_sync_duration_milliseconds:** The
duration of syncing internal-networkpolicy

#### Antrea Proxy Metrics

- **antrea_proxy_sync_proxy_rules_duration_seconds:** SyncProxyRules duration
of Antrea Proxy in seconds
- **antrea_proxy_total_endpoints_installed:** The number of Endpoints
installed by Antrea Proxy
- **antrea_proxy_total_endpoints_updates:** The cumulative number of Endpoint
updates received by Antrea Proxy
- **antrea_proxy_total_services_installed:** The number of Services installed
by Antrea Proxy
- **antrea_proxy_total_services_updates:** The cumulative number of Service
updates received by Antrea Proxy

### Common Metrics Provided by Infrastructure

#### Aggregator Metrics

- **aggregator_discovery_aggregation_count_total:** Counter of number of
times discovery was aggregated

#### Apiserver Metrics

- **apiserver_audit_event_total:** Counter of audit events generated and
sent to the audit backend.
- **apiserver_audit_requests_rejected_total:** Counter of apiserver requests
rejected due to an error in audit logging backend.
- **apiserver_client_certificate_expiration_seconds:** Distribution of the
remaining lifetime on the certificate used to authenticate a request.
- **apiserver_current_inflight_requests:** Maximal number of currently used
inflight request limit of this apiserver per request kind in last second.
- **apiserver_delegated_authn_request_duration_seconds:** Request latency
in seconds. Broken down by status code.
- **apiserver_delegated_authn_request_total:** Number of HTTP requests
partitioned by status code.
- **apiserver_delegated_authz_request_duration_seconds:** Request latency
in seconds. Broken down by status code.
- **apiserver_delegated_authz_request_total:** Number of HTTP requests
partitioned by status code.
- **apiserver_envelope_encryption_dek_cache_fill_percent:** Percent of the
cache slots currently occupied by cached DEKs.
- **apiserver_flowcontrol_read_vs_write_current_requests:** EXPERIMENTAL:
Observations, at the end of every nanosecond, of the number of requests
(as a fraction of the relevant limit) waiting or in regular stage of execution
- **apiserver_flowcontrol_seat_fair_frac:** Fair fraction of server's
concurrency to allocate to each priority level that can use it
- **apiserver_longrunning_requests:** Gauge of all active long-running
apiserver requests broken out by verb, group, version, resource, scope and
component. Not all requests are tracked this way.
- **apiserver_request_duration_seconds:** Response latency distribution in
seconds for each verb, dry run value, group, version, resource, subresource,
scope and component.
- **apiserver_request_filter_duration_seconds:** Request filter latency
distribution in seconds, for each filter type
- **apiserver_request_sli_duration_seconds:** Response latency distribution
(not counting webhook duration and priority & fairness queue wait times)
in seconds for each verb, group, version, resource, subresource, scope
and component.
- **apiserver_request_slo_duration_seconds:** Response latency distribution
(not counting webhook duration and priority & fairness queue wait times)
in seconds for each verb, group, version, resource, subresource, scope
and component.
- **apiserver_request_total:** Counter of apiserver requests broken out
for each verb, dry run value, group, version, resource, scope, component,
and HTTP response code.
- **apiserver_response_sizes:** Response size distribution in bytes for each
group, version, verb, resource, subresource, scope and component.
- **apiserver_storage_data_key_generation_duration_seconds:** Latencies in
seconds of data encryption key(DEK) generation operations.
- **apiserver_storage_data_key_generation_failures_total:** Total number of
failed data encryption key(DEK) generation operations.
- **apiserver_storage_envelope_transformation_cache_misses_total:** Total
number of cache misses while accessing key decryption key(KEK).
- **apiserver_tls_handshake_errors_total:** Number of requests dropped with
'TLS handshake error from' error
- **apiserver_watch_events_sizes:** Watch event size distribution in bytes
- **apiserver_watch_events_total:** Number of events sent in watch clients
- **apiserver_webhooks_x509_insecure_sha1_total:** Counts the number of
requests to servers with insecure SHA1 signatures in their serving certificate
OR the number of connection failures due to the insecure SHA1 signatures
(either/or, based on the runtime environment)
- **apiserver_webhooks_x509_missing_san_total:** Counts the number of requests
to servers missing SAN extension in their serving certificate OR the number
of connection failures due to the lack of x509 certificate SAN extension
missing (either/or, based on the runtime environment)

#### Authenticated Metrics

- **authenticated_user_requests:** Counter of authenticated requests broken
out by username.

#### Authentication Metrics

- **authentication_attempts:** Counter of authenticated attempts.
- **authentication_duration_seconds:** Authentication duration in seconds
broken out by result.
- **authentication_token_cache_active_fetch_count:**
- **authentication_token_cache_fetch_total:**
- **authentication_token_cache_request_duration_seconds:**
- **authentication_token_cache_request_total:**

#### Authorization Metrics

- **authorization_attempts_total:** Counter of authorization attempts broken
down by result. It can be either 'allowed', 'denied', 'no-opinion' or 'error'.
- **authorization_duration_seconds:** Authorization duration in seconds
broken out by result.

#### Cardinality Metrics

- **cardinality_enforcement_unexpected_categorizations_total:** The count
of unexpected categorizations during cardinality enforcement.

#### Disabled Metrics

- **disabled_metrics_total:** The count of disabled metrics.

#### Field Metrics

- **field_validation_request_duration_seconds:** Response latency distribution
in seconds for each field validation value

#### Hidden Metrics

- **hidden_metrics_total:** The count of hidden metrics.

#### Process Metrics

- **process_cpu_seconds_total:** Total user and system CPU time spent
in seconds.
- **process_max_fds:** Maximum number of open file descriptors.
- **process_network_receive_bytes_total:** Number of bytes received by the
process over the network.
- **process_network_transmit_bytes_total:** Number of bytes sent by the
process over the network.
- **process_open_fds:** Number of open file descriptors.
- **process_resident_memory_bytes:** Resident memory size in bytes.
- **process_start_time_seconds:** Start time of the process since unix epoch
in seconds.
- **process_virtual_memory_bytes:** Virtual memory size in bytes.
- **process_virtual_memory_max_bytes:** Maximum amount of virtual memory
available in bytes.

#### Registered Metrics

- **registered_metrics_total:** The count of registered metrics broken by
stability level and deprecation version.

#### Workqueue Metrics

- **workqueue_adds_total:** Total number of adds handled by workqueue
- **workqueue_depth:** Current depth of workqueue
- **workqueue_longest_running_processor_seconds:** How many seconds has the
longest running processor for workqueue been running.
- **workqueue_queue_duration_seconds:** How long in seconds an item stays
in workqueue before being requested.
- **workqueue_retries_total:** Total number of retries handled by workqueue
- **workqueue_unfinished_work_seconds:** How many seconds of work has
done that is in progress and hasn't been observed by work_duration. Large
values indicate stuck threads. One can deduce the number of stuck threads
by observing the rate at which this increases.
- **workqueue_work_duration_seconds:** How long in seconds processing an
item from workqueue takes.

#### Go Metrics

Please check Go metrics from [this site](https://pkg.go.dev/runtime/metrics)
