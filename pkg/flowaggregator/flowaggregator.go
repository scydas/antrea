// Copyright 2020 Antrea Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package flowaggregator

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/google/uuid"
	"github.com/vmware/go-ipfix/pkg/collector"
	ipfixentities "github.com/vmware/go-ipfix/pkg/entities"
	ipfixregistry "github.com/vmware/go-ipfix/pkg/registry"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"

	flowaggregatorconfig "antrea.io/antrea/pkg/config/flowaggregator"
	"antrea.io/antrea/pkg/flowaggregator/exporter"
	"antrea.io/antrea/pkg/flowaggregator/infoelements"
	"antrea.io/antrea/pkg/flowaggregator/intermediate"
	"antrea.io/antrea/pkg/flowaggregator/options"
	"antrea.io/antrea/pkg/flowaggregator/querier"
	"antrea.io/antrea/pkg/ipfix"
	"antrea.io/antrea/pkg/util/podstore"
)

var (
	aggregationElements = &intermediate.AggregationElements{
		NonStatsElements:                   infoelements.NonStatsElementList,
		StatsElements:                      infoelements.StatsElementList,
		AggregatedSourceStatsElements:      infoelements.AntreaSourceStatsElementList,
		AggregatedDestinationStatsElements: infoelements.AntreaDestinationStatsElementList,
		AntreaFlowEndSecondsElements:       infoelements.AntreaFlowEndSecondsElementList,
		ThroughputElements:                 infoelements.AntreaThroughputElementList,
		SourceThroughputElements:           infoelements.AntreaSourceThroughputElementList,
		DestinationThroughputElements:      infoelements.AntreaDestinationThroughputElementList,
	}

	correlateFields = []string{
		"sourcePodName",
		"sourcePodNamespace",
		"sourceNodeName",
		"destinationPodName",
		"destinationPodNamespace",
		"destinationNodeName",
		"destinationClusterIPv4",
		"destinationClusterIPv6",
		"destinationServicePort",
		"destinationServicePortName",
		"ingressNetworkPolicyName",
		"ingressNetworkPolicyNamespace",
		"ingressNetworkPolicyRuleAction",
		"ingressNetworkPolicyType",
		"ingressNetworkPolicyRuleName",
		"egressNetworkPolicyName",
		"egressNetworkPolicyNamespace",
		"egressNetworkPolicyRuleAction",
		"egressNetworkPolicyType",
		"egressNetworkPolicyRuleName",
	}
)

const (
	aggregationWorkerNum = 2
	udpTransport         = "udp"
	tcpTransport         = "tcp"
	collectorAddress     = "0.0.0.0:4739"
)

// these are used for unit testing
var (
	newIPFIXExporter = func(clusterUUID uuid.UUID, opt *options.Options, registry ipfix.IPFIXRegistry) exporter.Interface {
		return exporter.NewIPFIXExporter(clusterUUID, opt, registry)
	}
	newClickHouseExporter = func(clusterUUID uuid.UUID, opt *options.Options) (exporter.Interface, error) {
		return exporter.NewClickHouseExporter(clusterUUID, opt)
	}
	newS3Exporter = func(clusterUUID uuid.UUID, opt *options.Options) (exporter.Interface, error) {
		return exporter.NewS3Exporter(clusterUUID, opt)
	}
	newLogExporter = func(opt *options.Options) (exporter.Interface, error) {
		return exporter.NewLogExporter(opt)
	}
)

type flowAggregator struct {
	aggregatorMode              flowaggregatorconfig.AggregatorMode
	clusterUUID                 uuid.UUID
	clusterID                   string
	aggregatorTransportProtocol flowaggregatorconfig.AggregatorTransportProtocol
	collectingProcess           ipfix.IPFIXCollectingProcess
	preprocessor                *preprocessor
	aggregationProcess          intermediate.AggregationProcess
	activeFlowRecordTimeout     time.Duration
	inactiveFlowRecordTimeout   time.Duration
	registry                    ipfix.IPFIXRegistry
	flowAggregatorAddress       string
	includePodLabels            bool
	k8sClient                   kubernetes.Interface
	podStore                    podstore.Interface
	numRecordsExported          atomic.Int64
	numRecordsDropped           atomic.Int64
	updateCh                    chan *options.Options
	configFile                  string
	configWatcher               *fsnotify.Watcher
	configData                  []byte
	APIServer                   flowaggregatorconfig.APIServerConfig
	ipfixExporter               exporter.Interface
	clickHouseExporter          exporter.Interface
	s3Exporter                  exporter.Interface
	logExporter                 exporter.Interface
	logTickerDuration           time.Duration
	preprocessorOutCh           chan *ipfixentities.Message
	exportersMutex              sync.Mutex
}

func NewFlowAggregator(
	k8sClient kubernetes.Interface,
	clusterUUID uuid.UUID,
	podStore podstore.Interface,
	configFile string,
) (*flowAggregator, error) {
	if len(configFile) == 0 {
		return nil, fmt.Errorf("configFile is empty string")
	}
	registry := ipfix.NewIPFIXRegistry()
	registry.LoadRegistry()

	var err error
	configWatcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("error when creating file watcher for configuration file: %v", err)
	}
	// When watching the configuration file directly, we have to add the file back to our watcher whenever the configuration
	// file is modified (The watcher cannot track the config file  when the config file is replaced).
	// Watching the directory can prevent us from above situation.
	if err = configWatcher.Add(filepath.Dir(configFile)); err != nil {
		return nil, fmt.Errorf("error when starting file watch on configuration dir: %v", err)
	}

	data, err := os.ReadFile(configFile)
	if err != nil {
		return nil, fmt.Errorf("cannot read FlowAggregator configuration file: %v", err)
	}
	opt, err := options.LoadConfig(data)
	if err != nil {
		return nil, err
	}

	clusterID := opt.Config.ClusterID
	if clusterID == "" {
		clusterID = clusterUUID.String()
	}

	fa := &flowAggregator{
		aggregatorMode:              opt.AggregatorMode,
		clusterUUID:                 clusterUUID,
		clusterID:                   clusterID,
		aggregatorTransportProtocol: opt.AggregatorTransportProtocol,
		activeFlowRecordTimeout:     opt.ActiveFlowRecordTimeout,
		inactiveFlowRecordTimeout:   opt.InactiveFlowRecordTimeout,
		registry:                    registry,
		flowAggregatorAddress:       opt.Config.FlowAggregatorAddress,
		includePodLabels:            opt.Config.RecordContents.PodLabels,
		k8sClient:                   k8sClient,
		podStore:                    podStore,
		updateCh:                    make(chan *options.Options),
		configFile:                  configFile,
		configWatcher:               configWatcher,
		configData:                  data,
		APIServer:                   opt.Config.APIServer,
		logTickerDuration:           time.Minute,
		// We support buffering a small amount of messages.
		preprocessorOutCh: make(chan *ipfixentities.Message, 16),
	}
	if err := fa.InitCollectingProcess(); err != nil {
		return nil, fmt.Errorf("error when creating collecting process: %w", err)
	}
	if err := fa.InitPreprocessor(); err != nil {
		return nil, fmt.Errorf("error when creating preprocessor: %w", err)
	}
	if opt.AggregatorMode == flowaggregatorconfig.AggregatorModeAggregate {
		if err := fa.InitAggregationProcess(); err != nil {
			return nil, fmt.Errorf("error when creating aggregation process: %w", err)
		}
	}
	if opt.Config.ClickHouse.Enable {
		var err error
		fa.clickHouseExporter, err = newClickHouseExporter(clusterUUID, opt)
		if err != nil {
			return nil, fmt.Errorf("error when creating ClickHouse export process: %v", err)
		}
	}
	if opt.Config.S3Uploader.Enable {
		var err error
		fa.s3Exporter, err = newS3Exporter(clusterUUID, opt)
		if err != nil {
			return nil, fmt.Errorf("error when creating S3 export process: %v", err)
		}
	}
	if opt.Config.FlowLogger.Enable {
		var err error
		fa.logExporter, err = newLogExporter(opt)
		if err != nil {
			return nil, fmt.Errorf("error when creating log export process: %v", err)
		}
	}
	if opt.Config.FlowCollector.Enable {
		fa.ipfixExporter = newIPFIXExporter(clusterUUID, opt, registry)
	}
	klog.InfoS("FlowAggregator initialized", "mode", opt.AggregatorMode, "clusterID", fa.clusterID)
	return fa, nil
}

func (fa *flowAggregator) InitCollectingProcess() error {
	var cpInput collector.CollectorInput
	switch fa.aggregatorTransportProtocol {
	case flowaggregatorconfig.AggregatorTransportProtocolTLS:
		parentCert, privateKey, caCert, err := generateCACertKey()
		if err != nil {
			return fmt.Errorf("error when generating CA certificate: %v", err)
		}
		serverCert, serverKey, err := generateCertKey(parentCert, privateKey, true, fa.flowAggregatorAddress)
		if err != nil {
			return fmt.Errorf("error when creating server certificate: %v", err)
		}

		clientCert, clientKey, err := generateCertKey(parentCert, privateKey, false, "")
		if err != nil {
			return fmt.Errorf("error when creating client certificate: %v", err)
		}
		err = syncCAAndClientCert(caCert, clientCert, clientKey, fa.k8sClient)
		if err != nil {
			return fmt.Errorf("error when synchronizing client certificate: %v", err)
		}
		cpInput = collector.CollectorInput{
			Address:       collectorAddress,
			Protocol:      tcpTransport,
			MaxBufferSize: 65535,
			TemplateTTL:   0, // use default value from go-ipfix library
			IsEncrypted:   true,
			CACert:        caCert,
			ServerKey:     serverKey,
			ServerCert:    serverCert,
		}
	case flowaggregatorconfig.AggregatorTransportProtocolTCP:
		cpInput = collector.CollectorInput{
			Address:       collectorAddress,
			Protocol:      tcpTransport,
			MaxBufferSize: 65535,
			TemplateTTL:   0, // use default value from go-ipfix library
			IsEncrypted:   false,
		}
	default:
		cpInput = collector.CollectorInput{
			Address:       collectorAddress,
			Protocol:      udpTransport,
			MaxBufferSize: 1024,
			TemplateTTL:   0, // use default value from go-ipfix library
			IsEncrypted:   false,
		}
	}
	cpInput.NumExtraElements = len(infoelements.AntreaLabelsElementList)
	// clusterId
	cpInput.NumExtraElements += 1
	if fa.aggregatorMode == flowaggregatorconfig.AggregatorModeAggregate {
		cpInput.NumExtraElements += len(infoelements.AntreaSourceStatsElementList) + len(infoelements.AntreaDestinationStatsElementList) +
			len(infoelements.AntreaFlowEndSecondsElementList) + len(infoelements.AntreaThroughputElementList) + len(infoelements.AntreaSourceThroughputElementList) + len(infoelements.AntreaDestinationThroughputElementList)
	} else {
		cpInput.NumExtraElements += len(infoelements.IANAProxyModeElementList)
	}
	// Tell the collector to accept IEs which are not part of the IPFIX registry (hardcoded in
	// the go-ipfix library). The preprocessor will take care of removing these elements.
	cpInput.DecodingMode = collector.DecodingModeLenientKeepUnknown
	var err error
	fa.collectingProcess, err = collector.InitCollectingProcess(cpInput)
	return err
}

func (fa *flowAggregator) InitPreprocessor() error {
	getInfoElementFromRegistry := func(ieName string, enterpriseID uint32) (*ipfixentities.InfoElement, error) {
		ie, err := fa.registry.GetInfoElement(ieName, enterpriseID)
		if err != nil {
			return nil, fmt.Errorf("error when looking up IE %q in registry: %w", ieName, err)
		}
		return ie, err
	}

	getInfoElements := func(isIPv4 bool) ([]*ipfixentities.InfoElement, error) {
		ianaInfoElements := infoelements.IANAInfoElementsIPv4
		ianaReverseInfoElements := infoelements.IANAReverseInfoElements
		antreaInfoElements := infoelements.AntreaInfoElementsIPv4
		if !isIPv4 {
			ianaInfoElements = infoelements.IANAInfoElementsIPv6
			antreaInfoElements = infoelements.AntreaInfoElementsIPv6
		}
		infoElements := make([]*ipfixentities.InfoElement, 0)
		for _, ieName := range ianaInfoElements {
			ie, err := getInfoElementFromRegistry(ieName, ipfixregistry.IANAEnterpriseID)
			if err != nil {
				return nil, err
			}
			infoElements = append(infoElements, ie)
		}
		for _, ieName := range ianaReverseInfoElements {
			ie, err := getInfoElementFromRegistry(ieName, ipfixregistry.IANAReversedEnterpriseID)
			if err != nil {
				return nil, err
			}
			infoElements = append(infoElements, ie)
		}
		for _, ieName := range antreaInfoElements {
			ie, err := getInfoElementFromRegistry(ieName, ipfixregistry.AntreaEnterpriseID)
			if err != nil {
				return nil, err
			}
			infoElements = append(infoElements, ie)
		}
		return infoElements, nil
	}

	infoElementsIPv4, err := getInfoElements(true)
	if err != nil {
		return err
	}
	infoElementsIPv6, err := getInfoElements(false)
	if err != nil {
		return err
	}
	fa.preprocessor, err = newPreprocessor(infoElementsIPv4, infoElementsIPv6, fa.collectingProcess.GetMsgChan(), fa.preprocessorOutCh)
	return err
}

func (fa *flowAggregator) InitAggregationProcess() error {
	var err error
	apInput := intermediate.AggregationInput{
		MessageChan:           fa.preprocessorOutCh,
		WorkerNum:             aggregationWorkerNum,
		CorrelateFields:       correlateFields,
		ActiveExpiryTimeout:   fa.activeFlowRecordTimeout,
		InactiveExpiryTimeout: fa.inactiveFlowRecordTimeout,
		AggregateElements:     aggregationElements,
	}
	fa.aggregationProcess, err = intermediate.InitAggregationProcess(apInput)
	return err
}

func (fa *flowAggregator) Run(stopCh <-chan struct{}) {
	var wg, ipfixProcessesWg sync.WaitGroup

	// We first wait for the PodStore to sync to avoid lookup failures when processing records.
	const podStoreSyncTimeout = 30 * time.Second
	klog.InfoS("Waiting for PodStore to sync", "timeout", podStoreSyncTimeout)
	if err := wait.PollUntilContextTimeout(wait.ContextForChannel(stopCh), 100*time.Millisecond, podStoreSyncTimeout, true, func(ctx context.Context) (done bool, err error) {
		return fa.podStore.HasSynced(), nil
	}); err != nil {
		// PodStore not synced within a reasonable time. We continue with the rest of the
		// function but there may be error logs when processing records.
		klog.ErrorS(err, "PodStore not synced", "timeout", podStoreSyncTimeout)
	} else {
		klog.InfoS("PodStore synced")
	}

	ipfixProcessesWg.Add(1)
	go func() {
		// Waiting for this function to return on stop makes it easier to set expectations
		// when testing. Without this, there is no guarantee that
		// fa.collectingProcess.Start() was called by the time Run() returns.
		defer ipfixProcessesWg.Done()
		// blocking function, will return when fa.collectingProcess.Stop() is called
		fa.collectingProcess.Start()
	}()
	ipfixProcessesWg.Add(1)
	go func() {
		defer ipfixProcessesWg.Done()
		fa.preprocessor.Run(stopCh)
	}()
	if fa.aggregationProcess != nil {
		ipfixProcessesWg.Add(1)
		go func() {
			// Same comment as above.
			defer ipfixProcessesWg.Done()
			// blocking function, will return when fa.aggregationProcess.Stop() is called
			fa.aggregationProcess.Start()
		}()
	}

	if fa.ipfixExporter != nil {
		fa.ipfixExporter.Start()
	}
	if fa.clickHouseExporter != nil {
		fa.clickHouseExporter.Start()
	}
	if fa.s3Exporter != nil {
		fa.s3Exporter.Start()
	}
	if fa.logExporter != nil {
		fa.logExporter.Start()
	}

	wg.Add(1)
	go func() {
		// Waiting for this function to return on stop makes it easier to set expectations
		// when testing.
		defer wg.Done()
		fa.podStore.Run(stopCh)
	}()

	wg.Add(1)
	go func() {
		// We want to make sure that flowExportLoop returns before
		// returning from this function. This is because flowExportLoop
		// is in charge of cleanly stopping the exporters.
		defer wg.Done()
		fa.flowExportLoop(stopCh)
	}()
	wg.Add(1)
	go func() {
		// there is no strong reason to wait for this function to return
		// on stop, but it does seem like the best thing to do.
		defer wg.Done()
		fa.watchConfiguration(stopCh)
		// the watcher should not be closed until watchConfiguration returns.
		// note that it is safe to close an fsnotify watcher multiple times,
		// for example:
		// https://github.com/fsnotify/fsnotify/blob/v1.6.0/backend_inotify.go#L184
		// in practice, this should only happen during unit tests.
		fa.configWatcher.Close()
	}()
	<-stopCh
	// Wait for fa.podStore.Run, fa.flowExportLoop and fa.watchConfiguration to return.
	wg.Wait()
	// Stop fa.collectingProcess and fa.aggregationProcess, and wait for their Start function to
	// return. There should be no strict requirement to stop these processes last, but we
	// preserve existing behavior from older code.
	if fa.aggregationProcess != nil {
		fa.aggregationProcess.Stop()
	}
	fa.collectingProcess.Stop()
	ipfixProcessesWg.Wait()
}

// flowExportLoop is the main loop for the FlowAggregator. It runs in a single
// goroutine. All calls to exporter.Interface methods happen within this
// function, hence preventing any concurrency issue as the exporter.Interface
// implementations are not safe for concurrent access.
func (fa *flowAggregator) flowExportLoop(stopCh <-chan struct{}) {
	defer func() {
		// We stop the exporters from flowExportLoop and not from Run,
		// to avoid any possible race condition.
		if fa.ipfixExporter != nil {
			fa.ipfixExporter.Stop()
		}
		if fa.clickHouseExporter != nil {
			fa.clickHouseExporter.Stop()
		}
		if fa.s3Exporter != nil {
			fa.s3Exporter.Stop()
		}
		if fa.logExporter != nil {
			fa.logExporter.Stop()
		}
	}()
	switch fa.aggregatorMode {
	case flowaggregatorconfig.AggregatorModeAggregate:
		fa.flowExportLoopAggregate(stopCh)
	case flowaggregatorconfig.AggregatorModeProxy:
		fa.flowExportLoopProxy(stopCh)
	}
}

func (fa *flowAggregator) proxyRecord(record ipfixentities.Record, obsDomainID uint32, exporterAddress string) error {
	getAddress := func(record ipfixentities.Record, name string) string {
		element, _, exist := record.GetInfoElementWithValue(name)
		if !exist {
			return ""
		}
		return element.GetIPAddressValue().String()
	}

	getFlowType := func(record ipfixentities.Record) uint8 {
		element, _, exist := record.GetInfoElementWithValue("flowType")
		if !exist {
			klog.ErrorS(nil, "Missing flowType")
			return 0
		}
		return element.GetUnsigned8Value()
	}

	sourceIPv4Address := getAddress(record, "sourceIPv4Address")
	sourceIPv6Address := getAddress(record, "sourceIPv6Address")
	destinationIPv4Address := getAddress(record, "destinationIPv4Address")
	destinationIPv6Address := getAddress(record, "destinationIPv6Address")
	var isIPv6 bool
	var sourceAddress, destinationAddress string
	switch {
	case sourceIPv4Address != "" && sourceIPv6Address == "" && destinationIPv4Address != "" && destinationIPv6Address == "":
		isIPv6 = false
		sourceAddress = sourceIPv4Address
		destinationAddress = destinationIPv4Address
	case sourceIPv4Address == "" && sourceIPv6Address != "" && destinationIPv4Address == "" && destinationIPv6Address != "":
		isIPv6 = true
		sourceAddress = sourceIPv6Address
		destinationAddress = destinationIPv6Address
	default:
		// All other cases are invalid.
		return fmt.Errorf("invalid format for record: source and destination must be present and IPv4 / IPv6 fields are mutually exclusive")
	}
	startTime, err := fa.getRecordStartTime(record)
	if err != nil {
		return fmt.Errorf("cannot find record start time: %w", err)
	}
	flowType := getFlowType(record)
	var withSource, withDestination bool
	if sourcePodName, _, exist := record.GetInfoElementWithValue("sourcePodName"); exist {
		withSource = sourcePodName.GetStringValue() != ""
	}
	if destinationPodName, _, exist := record.GetInfoElementWithValue("destinationPodName"); exist {
		withDestination = destinationPodName.GetStringValue() != ""
	}
	var direction uint8
	switch {
	// !withDestination should be redundant here
	case flowType == ipfixregistry.FlowTypeInterNode && withSource && !withDestination:
		// egress
		direction = 0x01
	// !withSource should be redundant here
	case flowType == ipfixregistry.FlowTypeInterNode && !withSource && withDestination:
		// ingress
		direction = 0x00
	case flowType == ipfixregistry.FlowTypeToExternal && withSource:
		// egress
		direction = 0x01
	case flowType == ipfixregistry.FlowTypeFromExternal && withDestination:
		// ingress
		direction = 0x00
	default:
		// not a valid value for the IE, we use it as a reserved value (unknown)
		// this covers the IntraNode case
		direction = 0xff
	}
	if flowType == ipfixregistry.FlowTypeInterNode {
		// This is the only case where K8s metadata could be missing
		fa.fillK8sMetadata(sourceAddress, destinationAddress, record, startTime)
	}
	fa.fillPodLabels(sourceAddress, destinationAddress, record, startTime)
	if err := fa.fillClusterID(record); err != nil {
		klog.ErrorS(err, "Failed to add clusterId")
	}
	if err := fa.addOriginalObservationDomainID(record, obsDomainID); err != nil {
		klog.ErrorS(err, "Failed to add originalObservationDomainId")
	}
	originalExporterAddress := net.ParseIP(exporterAddress)
	if err := fa.addOriginalExporterIPv4Address(record, originalExporterAddress); err != nil {
		klog.ErrorS(err, "Failed to add originalExporterIPv4Address")
	}
	if err := fa.addOriginalExporterIPv6Address(record, originalExporterAddress); err != nil {
		klog.ErrorS(err, "Failed to add originalExporterIPv6Address")
	}
	if err := fa.addFlowDirection(record, direction); err != nil {
		klog.ErrorS(err, "Failed to add flowDirection")
	}
	return fa.sendRecord(record, isIPv6)
}

func (fa *flowAggregator) flowExportLoopProxy(stopCh <-chan struct{}) {
	logTicker := time.NewTicker(fa.logTickerDuration)
	defer logTicker.Stop()
	const flushTickerDuration = 1 * time.Second
	flushTicker := time.NewTicker(flushTickerDuration)
	defer flushTicker.Stop()
	msgCh := fa.preprocessorOutCh

	proxyRecords := func(msg *ipfixentities.Message) {
		set := msg.GetSet()
		if set.GetSetType() != ipfixentities.Data { // only process data records
			return
		}

		obsDomainID := msg.GetObsDomainID()
		exporterAddress := msg.GetExportAddress()
		records := set.GetRecords()
		for _, record := range records {
			if err := fa.proxyRecord(record, obsDomainID, exporterAddress); err != nil {
				fa.numRecordsDropped.Add(1)
				if errors.Is(err, exporter.ErrIPFIXExporterBackoff) {
					continue
				}
				klog.ErrorS(err, "Failed to proxy record")
			}
		}
	}

	updateCh := fa.updateCh
	for {
		select {
		case <-stopCh:
			return
		case msg, ok := <-msgCh:
			if !ok {
				msgCh = nil
				break
			}
			proxyRecords(msg)
		case <-flushTicker.C:
			if err := fa.flushExporters(); err != nil {
				klog.ErrorS(err, "Error when flushing exporters")
			}
		case <-logTicker.C:
			// Add visibility of processing stats of Flow Aggregator
			klog.V(4).InfoS("Total number of records received", "count", fa.collectingProcess.GetNumRecordsReceived())
			klog.V(4).InfoS("Total number of records exported by each active exporter", "count", fa.numRecordsExported.Load())
			klog.V(4).InfoS("Total number of records dropped", "count", fa.numRecordsDropped.Load())
			klog.V(4).InfoS("Number of exporters connected with Flow Aggregator", "count", fa.collectingProcess.GetNumConnToCollector())
		case opt, ok := <-updateCh:
			if !ok {
				// set the channel to nil and essentially disable this select case.
				// we could also just return straightaway as this should only happen
				// when stopCh is closed, but maybe it's better to keep stopCh as
				// the only signal for stopping the event loop.
				updateCh = nil
				break
			}
			fa.updateFlowAggregator(opt)
		}
	}
}

func (fa *flowAggregator) flowExportLoopAggregate(stopCh <-chan struct{}) {
	expireTimer := time.NewTimer(fa.activeFlowRecordTimeout)
	defer expireTimer.Stop()
	logTicker := time.NewTicker(fa.logTickerDuration)
	defer logTicker.Stop()

	updateCh := fa.updateCh
	for {
		select {
		case <-stopCh:
			return
		case <-expireTimer.C:
			// Pop the flow record item from expire priority queue in the Aggregation
			// Process and send the flow records.
			if err := fa.aggregationProcess.ForAllExpiredFlowRecordsDo(fa.sendAggregatedRecord); err != nil {
				klog.ErrorS(err, "Error when sending expired flow records")
				expireTimer.Reset(fa.activeFlowRecordTimeout)
				continue
			}
			// Get the new expiry and reset the timer.
			expireTimer.Reset(fa.aggregationProcess.GetExpiryFromExpirePriorityQueue())
			if err := fa.flushExporters(); err != nil {
				klog.ErrorS(err, "Error when flushing exporters")
			}
		case <-logTicker.C:
			// Add visibility of processing stats of Flow Aggregator
			klog.V(4).InfoS("Total number of records received", "count", fa.collectingProcess.GetNumRecordsReceived())
			klog.V(4).InfoS("Total number of records exported by each active exporter", "count", fa.numRecordsExported.Load())
			klog.V(4).InfoS("Total number of flows stored in Flow Aggregator", "count", fa.aggregationProcess.GetNumFlows())
			klog.V(4).InfoS("Number of exporters connected with Flow Aggregator", "count", fa.collectingProcess.GetNumConnToCollector())
		case opt, ok := <-updateCh:
			if !ok {
				// set the channel to nil and essentially disable this select case.
				// we could also just return straightaway as this should only happen
				// when stopCh is closed, but maybe it's better to keep stopCh as
				// the only signal for stopping the event loop.
				updateCh = nil
				break
			}
			fa.updateFlowAggregator(opt)
		}
	}
}

func (fa *flowAggregator) sendRecord(record ipfixentities.Record, isRecordIPv6 bool) error {
	if fa.ipfixExporter != nil {
		if err := fa.ipfixExporter.AddRecord(record, isRecordIPv6); err != nil {
			return err
		}
	}
	if fa.clickHouseExporter != nil {
		if err := fa.clickHouseExporter.AddRecord(record, isRecordIPv6); err != nil {
			return err
		}
	}
	if fa.s3Exporter != nil {
		if err := fa.s3Exporter.AddRecord(record, isRecordIPv6); err != nil {
			return err
		}
	}
	if fa.logExporter != nil {
		if err := fa.logExporter.AddRecord(record, isRecordIPv6); err != nil {
			return err
		}
	}
	fa.numRecordsExported.Add(1)
	return nil
}

func (fa *flowAggregator) flushExporters() error {
	if fa.ipfixExporter != nil {
		if err := fa.ipfixExporter.Flush(); err != nil {
			return err
		}
	}
	// Other exporters don't leverage Flush for now, so we skip them.
	return nil
}

func (fa *flowAggregator) sendAggregatedRecord(key intermediate.FlowKey, record *intermediate.AggregationFlowRecord) error {
	isRecordIPv4 := fa.aggregationProcess.IsAggregatedRecordIPv4(*record)
	startTime, err := fa.getRecordStartTime(record.Record)
	if err != nil {
		return fmt.Errorf("cannot find record start time: %v", err)
	}
	if !fa.aggregationProcess.AreCorrelatedFieldsFilled(*record) {
		fa.fillK8sMetadata(key.SourceAddress, key.DestinationAddress, record.Record, startTime)
		fa.aggregationProcess.SetCorrelatedFieldsFilled(record, true)
	}
	// Even if fa.includePodLabels is false, we still need to add an empty IE to match the template.
	if !fa.aggregationProcess.AreExternalFieldsFilled(*record) {
		fa.fillPodLabels(key.SourceAddress, key.DestinationAddress, record.Record, startTime)
		if err := fa.fillClusterID(record.Record); err != nil {
			klog.ErrorS(err, "Failed to add clusterId")
		}
		fa.aggregationProcess.SetExternalFieldsFilled(record, true)
	}
	if err := fa.sendRecord(record.Record, !isRecordIPv4); err != nil {
		return err
	}
	if err := fa.aggregationProcess.ResetStatAndThroughputElementsInRecord(record.Record); err != nil {
		return err
	}
	return nil
}

// fillK8sMetadata fills Pod name, Pod namespace and Node name for inter-Node flows
// that have incomplete info due to deny network policy.
func (fa *flowAggregator) fillK8sMetadata(sourceAddress, destinationAddress string, record ipfixentities.Record, startTime time.Time) {
	// fill source Pod info when sourcePodName is empty
	if sourcePodName, _, exist := record.GetInfoElementWithValue("sourcePodName"); exist {
		if sourcePodName.GetStringValue() == "" {
			pod, exist := fa.podStore.GetPodByIPAndTime(sourceAddress, startTime)
			if exist {
				sourcePodName.SetStringValue(pod.Name)
				if sourcePodNamespace, _, exist := record.GetInfoElementWithValue("sourcePodNamespace"); exist {
					sourcePodNamespace.SetStringValue(pod.Namespace)
				}
				if sourceNodeName, _, exist := record.GetInfoElementWithValue("sourceNodeName"); exist {
					sourceNodeName.SetStringValue(pod.Spec.NodeName)
				}
			} else {
				klog.ErrorS(nil, "Cannot find Pod information", "sourceAddress", sourceAddress, "flowStartTime", startTime)
			}
		}
	}
	// fill destination Pod info when destinationPodName is empty
	if destinationPodName, _, exist := record.GetInfoElementWithValue("destinationPodName"); exist {
		if destinationPodName.GetStringValue() == "" {
			pod, exist := fa.podStore.GetPodByIPAndTime(destinationAddress, startTime)
			if exist {
				destinationPodName.SetStringValue(pod.Name)
				if destinationPodNamespace, _, exist := record.GetInfoElementWithValue("destinationPodNamespace"); exist {
					destinationPodNamespace.SetStringValue(pod.Namespace)
				}
				if destinationNodeName, _, exist := record.GetInfoElementWithValue("destinationNodeName"); exist {
					destinationNodeName.SetStringValue(pod.Spec.NodeName)
				}
			} else {
				klog.ErrorS(nil, "Cannot find Pod information", "destinationAddress", destinationAddress, "flowStartTime", startTime)
			}
		}
	}
}

func (fa *flowAggregator) getRecordStartTime(record ipfixentities.Record) (time.Time, error) {
	flowStartSeconds, _, exist := record.GetInfoElementWithValue("flowStartSeconds")
	if !exist {
		return time.Time{}, fmt.Errorf("flowStartSeconds filed is empty")
	}
	startTime := time.Unix(int64(flowStartSeconds.GetUnsigned32Value()), 0)
	return startTime, nil
}

func (fa *flowAggregator) fetchPodLabels(ip string, startTime time.Time) string {
	pod, exist := fa.podStore.GetPodByIPAndTime(ip, startTime)
	if !exist {
		klog.ErrorS(nil, "Error when getting Pod information from podInformer", "ip", ip, "startTime", startTime)
		return ""
	}
	labels := pod.GetLabels()
	if labels == nil {
		labels = map[string]string{}
	}
	labelsJSON, err := json.Marshal(labels)
	if err != nil {
		klog.ErrorS(err, "Error when JSON encoding of Pod labels")
		return ""
	}
	return string(labelsJSON)
}

func (fa *flowAggregator) fillPodLabelsForSide(ip string, record ipfixentities.Record, startTime time.Time, podNamespaceIEName, podNameIEName, podLabelsIEName string) error {
	podLabelsString := ""
	// If fa.includePodLabels is false, we always use an empty string.
	// If fa.includePodLabels is true, we use an empty string in case of error or if the
	// endpoint is not a Pod, and a valid JSON dictionary otherwise (which will be empty if the
	// Pod has no labels).
	if fa.includePodLabels {
		if podName, _, ok := record.GetInfoElementWithValue(podNameIEName); ok {
			podNameString := podName.GetStringValue()
			if podNamespace, _, ok := record.GetInfoElementWithValue(podNamespaceIEName); ok {
				podNamespaceString := podNamespace.GetStringValue()
				if podNameString != "" && podNamespaceString != "" {
					podLabelsString = fa.fetchPodLabels(ip, startTime)
				}
			}
		}
	}

	podLabelsElement, err := fa.registry.GetInfoElement(podLabelsIEName, ipfixregistry.AntreaEnterpriseID)
	if err == nil {
		podLabelsIE := ipfixentities.NewStringInfoElement(podLabelsElement, podLabelsString)
		if err := record.AddInfoElement(podLabelsIE); err != nil {
			return fmt.Errorf("error when adding podLabels InfoElementWithValue: %v", err)
		}
	} else {
		return fmt.Errorf("error when getting podLabels InfoElementWithValue: %v", err)
	}

	return nil
}

func (fa *flowAggregator) fillPodLabels(sourceAddress, destinationAddress string, record ipfixentities.Record, startTime time.Time) {
	if err := fa.fillPodLabelsForSide(sourceAddress, record, startTime, "sourcePodNamespace", "sourcePodName", "sourcePodLabels"); err != nil {
		klog.ErrorS(err, "Error when filling Pod labels", "side", "source")
	}
	if err := fa.fillPodLabelsForSide(destinationAddress, record, startTime, "destinationPodNamespace", "destinationPodName", "destinationPodLabels"); err != nil {
		klog.ErrorS(err, "Error when filling Pod labels", "side", "destination")
	}
}

func (fa *flowAggregator) fillClusterID(record ipfixentities.Record) error {
	ie, err := fa.registry.GetInfoElement("clusterId", ipfixregistry.AntreaEnterpriseID)
	if err != nil {
		return fmt.Errorf("error when getting clusterId InfoElement: %w", err)
	}
	if err := record.AddInfoElement(ipfixentities.NewStringInfoElement(ie, fa.clusterID)); err != nil {
		return fmt.Errorf("error when adding clusterId InfoElement with value: %w", err)
	}
	return nil
}

func (fa *flowAggregator) addOriginalObservationDomainID(record ipfixentities.Record, obsDomainID uint32) error {
	ie, err := fa.registry.GetInfoElement("originalObservationDomainId", ipfixregistry.IANAEnterpriseID)
	if err != nil {
		return fmt.Errorf("error when getting originalObservationDomainId InfoElement: %w", err)
	}
	if err := record.AddInfoElement(ipfixentities.NewUnsigned32InfoElement(ie, obsDomainID)); err != nil {
		return fmt.Errorf("error when adding originalObservationDomainId InfoElement with value: %w", err)
	}
	return nil
}

func (fa *flowAggregator) addOriginalExporterIPv4Address(record ipfixentities.Record, address net.IP) error {
	if address.To4() == nil {
		address = net.IPv4zero
	}
	ie, err := fa.registry.GetInfoElement("originalExporterIPv4Address", ipfixregistry.IANAEnterpriseID)
	if err != nil {
		return fmt.Errorf("error when getting originalExporterIPv4Address InfoElement: %w", err)
	}
	if err := record.AddInfoElement(ipfixentities.NewIPAddressInfoElement(ie, address)); err != nil {
		return fmt.Errorf("error when adding originalExporterIPv4Address InfoElement with value: %w", err)
	}
	return nil
}

func (fa *flowAggregator) addOriginalExporterIPv6Address(record ipfixentities.Record, address net.IP) error {
	if address.To4() != nil {
		address = net.IPv6zero
	}
	ie, err := fa.registry.GetInfoElement("originalExporterIPv6Address", ipfixregistry.IANAEnterpriseID)
	if err != nil {
		return fmt.Errorf("error when getting originalExporterIPv6Address InfoElement: %w", err)
	}
	if err := record.AddInfoElement(ipfixentities.NewIPAddressInfoElement(ie, address)); err != nil {
		return fmt.Errorf("error when adding originalExporterIPv6Address InfoElement with value: %w", err)
	}
	return nil
}

func (fa *flowAggregator) addFlowDirection(record ipfixentities.Record, direction uint8) error {
	ie, err := fa.registry.GetInfoElement("flowDirection", ipfixregistry.IANAEnterpriseID)
	if err != nil {
		return fmt.Errorf("error when getting flowDirection InfoElement: %w", err)
	}
	if err := record.AddInfoElement(ipfixentities.NewUnsigned8InfoElement(ie, direction)); err != nil {
		return fmt.Errorf("error when adding flowDirection InfoElement with value: %w", err)
	}
	return nil
}

func (fa *flowAggregator) GetFlowRecords(flowKey *intermediate.FlowKey) []map[string]interface{} {
	if fa.aggregationProcess != nil {
		return fa.aggregationProcess.GetRecords(flowKey)
	}
	return nil
}

func (fa *flowAggregator) getNumFlows() int64 {
	if fa.aggregationProcess != nil {
		return fa.aggregationProcess.GetNumFlows()
	}
	return 0
}

func (fa *flowAggregator) GetRecordMetrics() querier.Metrics {
	metrics := querier.Metrics{
		NumRecordsExported: fa.numRecordsExported.Load(),
		NumRecordsReceived: fa.collectingProcess.GetNumRecordsReceived(),
		NumRecordsDropped:  fa.numRecordsDropped.Load(),
		NumFlows:           fa.getNumFlows(),
		NumConnToCollector: fa.collectingProcess.GetNumConnToCollector(),
	}
	fa.exportersMutex.Lock()
	defer fa.exportersMutex.Unlock()
	metrics.WithClickHouseExporter = fa.clickHouseExporter != nil
	metrics.WithS3Exporter = fa.s3Exporter != nil
	metrics.WithLogExporter = fa.logExporter != nil
	metrics.WithIPFIXExporter = fa.ipfixExporter != nil
	return metrics
}

func (fa *flowAggregator) watchConfiguration(stopCh <-chan struct{}) {
	klog.InfoS("Watching for FlowAggregator configuration file")
	for {
		select {
		case <-stopCh:
			close(fa.updateCh)
			return
		case event, ok := <-fa.configWatcher.Events:
			klog.InfoS("Event happened", "event", event.String())
			if !ok {
				// If configWatcher event channel is closed, we kill the flow-aggregator Pod to restore
				// the channel.
				klog.Fatal("ConfigWatcher event channel closed")
			}
			if err := fa.handleWatcherEvent(); err != nil {
				// If the watcher cannot add mounted configuration file or the configuration file is not readable,
				// we kill the flow-aggregator Pod (serious error)
				klog.Fatalf("Cannot watch or read configMap: %v", err)
			}
		case err := <-fa.configWatcher.Errors:
			if err != nil {
				// If the error happens to watcher, we kill the flow-aggregator Pod.
				// watcher might be shut-down or broken in this situation.
				klog.Fatalf("configWatcher err: %v", err)
			}
		}
	}
}

func (fa *flowAggregator) handleWatcherEvent() error {
	data, err := os.ReadFile(fa.configFile)
	if err != nil {
		return fmt.Errorf("cannot read FlowAggregator configuration file: %v", err)
	}
	opt, err := options.LoadConfig(data)
	if err != nil {
		klog.ErrorS(err, "Error when loading configuration from config file")
		return nil
	}
	if bytes.Equal(data, fa.configData) {
		klog.InfoS("Flow-aggregator configuration didn't change")
		return nil
	}
	fa.configData = data
	klog.InfoS("Updating Flow Aggregator")
	// all updates must be performed within flowExportLoop
	fa.updateCh <- opt
	return nil
}

func (fa *flowAggregator) updateFlowAggregator(opt *options.Options) {
	// This function potentially modifies the exporter pointer fields (e.g.,
	// fa.ipfixExporter). We protect these writes by locking fa.exportersMutex, so that
	// GetRecordMetrics() can safely read the fields (by also locking the mutex).
	fa.exportersMutex.Lock()
	defer fa.exportersMutex.Unlock()
	// If user tries to change the mode dynamically, it makes sense to error out immediately and
	// ignore other updates, as this is such a major configuration parameter.
	// Unsupported "minor" updates are handled at the end of this function.
	if opt.AggregatorMode != fa.aggregatorMode {
		klog.ErrorS(nil, "FlowAggregator mode cannot be changed without restarting")
		return
	}
	if opt.Config.FlowCollector.Enable {
		if fa.ipfixExporter == nil {
			klog.InfoS("Enabling Flow-Collector")
			fa.ipfixExporter = newIPFIXExporter(fa.clusterUUID, opt, fa.registry)
			fa.ipfixExporter.Start()
			klog.InfoS("Enabled Flow-Collector")
		} else {
			fa.ipfixExporter.UpdateOptions(opt)
		}
	} else {
		if fa.ipfixExporter != nil {
			klog.InfoS("Disabling Flow-Collector")
			fa.ipfixExporter.Stop()
			fa.ipfixExporter = nil
			klog.InfoS("Disabled Flow-Collector")
		}
	}
	if opt.Config.ClickHouse.Enable {
		if fa.clickHouseExporter == nil {
			klog.InfoS("Enabling ClickHouse")
			var err error
			fa.clickHouseExporter, err = newClickHouseExporter(fa.clusterUUID, opt)
			if err != nil {
				klog.ErrorS(err, "Error when creating ClickHouse export process")
				return
			}
			fa.clickHouseExporter.Start()
			klog.InfoS("Enabled ClickHouse")
		} else {
			fa.clickHouseExporter.UpdateOptions(opt)
		}
	} else {
		if fa.clickHouseExporter != nil {
			klog.InfoS("Disabling ClickHouse")
			fa.clickHouseExporter.Stop()
			fa.clickHouseExporter = nil
			klog.InfoS("Disabled ClickHouse")
		}
	}
	if opt.Config.S3Uploader.Enable {
		if fa.s3Exporter == nil {
			klog.InfoS("Enabling S3Uploader")
			var err error
			fa.s3Exporter, err = newS3Exporter(fa.clusterUUID, opt)
			if err != nil {
				klog.ErrorS(err, "Error when creating S3 export process")
				return
			}
			fa.s3Exporter.Start()
			klog.InfoS("Enabled S3Uploader")
		} else {
			fa.s3Exporter.UpdateOptions(opt)
		}
	} else {
		if fa.s3Exporter != nil {
			klog.InfoS("Disabling S3Uploader")
			fa.s3Exporter.Stop()
			fa.s3Exporter = nil
			klog.InfoS("Disabled S3Uploader")
		}
	}
	if opt.Config.FlowLogger.Enable {
		if fa.logExporter == nil {
			klog.InfoS("Enabling FlowLogger")
			var err error
			fa.logExporter, err = newLogExporter(opt)
			if err != nil {
				klog.ErrorS(err, "Error when creating log export process")
				return
			}
			fa.logExporter.Start()
			klog.InfoS("Enabled FlowLogger")
		} else {
			fa.logExporter.UpdateOptions(opt)
		}
	} else {
		if fa.logExporter != nil {
			klog.InfoS("Disabling FlowLogger")
			fa.logExporter.Stop()
			fa.logExporter = nil
			klog.InfoS("Disabled FlowLogger")
		}
	}
	if opt.Config.RecordContents.PodLabels != fa.includePodLabels {
		fa.includePodLabels = opt.Config.RecordContents.PodLabels
		klog.InfoS("Updated recordContents.podLabels configuration", "value", fa.includePodLabels)
	}
	var unsupportedUpdates []string
	if opt.Config.APIServer != fa.APIServer {
		unsupportedUpdates = append(unsupportedUpdates, "apiServer")
	}
	if opt.ActiveFlowRecordTimeout != fa.activeFlowRecordTimeout {
		unsupportedUpdates = append(unsupportedUpdates, "activeFlowRecordTimeout")
	}
	if opt.InactiveFlowRecordTimeout != fa.inactiveFlowRecordTimeout {
		unsupportedUpdates = append(unsupportedUpdates, "inactiveFlowRecordTimeout")
	}
	if opt.AggregatorTransportProtocol != fa.aggregatorTransportProtocol {
		unsupportedUpdates = append(unsupportedUpdates, "aggregatorTransportProtocol")
	}
	if opt.Config.FlowAggregatorAddress != fa.flowAggregatorAddress {
		unsupportedUpdates = append(unsupportedUpdates, "flowAggregatorAddress")
	}
	if len(unsupportedUpdates) > 0 {
		klog.ErrorS(nil, "Ignoring unsupported configuration updates, please restart FlowAggregator", "keys", unsupportedUpdates)
	}
}
