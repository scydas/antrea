// Copyright 2021 Antrea Authors
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

package cniserver

import (
	"fmt"
	"net"

	current "github.com/containernetworking/cni/pkg/types/100"
	"k8s.io/klog/v2"

	"antrea.io/antrea/pkg/agent/cniserver/ipam"
	"antrea.io/antrea/pkg/agent/interfacestore"
	"antrea.io/antrea/pkg/ovs/ovsconfig"
)

func NewSecondaryInterfaceConfigurator(ovsBridgeClient ovsconfig.OVSBridgeClient, interfaceStore interfacestore.InterfaceStore) (*podConfigurator, error) {
	pc, err := newPodConfigurator(nil, ovsBridgeClient, nil, nil, interfaceStore, nil, ovsconfig.OVSDatapathSystem, false, false, nil, nil, nil)
	if err == nil {
		pc.isSecondaryNetwork = true
	}
	return pc, err
}

// ConfigureSriovSecondaryInterface configures a SR-IOV secondary interface for a Pod.
func (pc *podConfigurator) ConfigureSriovSecondaryInterface(
	podName, podNamespace string,
	containerID, containerNetNS, containerInterfaceName string,
	mtu int,
	podSriovVFDeviceID string,
	result *current.Result) error {
	if podSriovVFDeviceID == "" {
		return fmt.Errorf("error getting the Pod SR-IOV VF device ID")
	}

	err := pc.ifConfigurator.configureContainerLink(podName, podNamespace, containerID, containerNetNS, containerInterfaceName, mtu, "", podSriovVFDeviceID, result, nil, nil)
	if err != nil {
		return err
	}
	containerIface := result.Interfaces[1]
	klog.InfoS("Configured SR-IOV interface", "Pod", klog.KRef(podNamespace, podName), "interface", containerInterfaceName)

	// Use podSriovVFDeviceID as the interface name in the interface store.
	hostInterfaceName := podSriovVFDeviceID
	containerConfig := buildContainerConfig(hostInterfaceName, containerID, podName, podNamespace,
		containerNetNS, containerIface, result.IPs, 0)
	pc.ifaceStore.AddInterface(containerConfig)
	containerIface.PciID = podSriovVFDeviceID
	if result.IPs != nil {
		if err = pc.ifConfigurator.advertiseContainerAddr(containerNetNS, containerIface.Name, result); err != nil {
			klog.ErrorS(err, "Failed to advertise IP address for SR-IOV interface",
				"container", containerID, "interface", containerInterfaceName)
		}
	}
	return nil
}

// DeleteSriovSecondaryInterface deletes a SRIOV secondary interface.
func (pc *podConfigurator) DeleteSriovSecondaryInterface(interfaceConfig *interfacestore.InterfaceConfig) error {
	if err := pc.ifConfigurator.recoverVFInterfaceName(interfaceConfig.IFDev, interfaceConfig.NetNS); err != nil {
		klog.ErrorS(err, "Failed to rename the container interface link to the original VF name",
			"Pod", klog.KRef(interfaceConfig.PodNamespace, interfaceConfig.PodName),
			"interface", interfaceConfig.IFDev)
		return err
	}

	pc.ifaceStore.DeleteInterface(interfaceConfig)
	klog.InfoS("Deleted SR-IOV interface", "Pod", klog.KRef(interfaceConfig.PodNamespace, interfaceConfig.PodName),
		"interface", interfaceConfig.IFDev)
	return nil
}

// ConfigureVLANSecondaryInterface configures a VLAN secondary interface on the secondary network
// OVS bridge, and returns the OVS port UUID.
func (pc *podConfigurator) ConfigureVLANSecondaryInterface(
	podName, podNamespace string,
	containerID, containerNetNS, containerInterfaceName string,
	mtu int, ipamResult *ipam.IPAMResult, mac net.HardwareAddr) error {
	return pc.configureInterfacesCommon(podName, podNamespace, containerID, containerNetNS,
		containerInterfaceName, mtu, "", ipamResult, nil, mac)
}

// DeleteVLANSecondaryInterface deletes a VLAN secondary interface.
func (pc *podConfigurator) DeleteVLANSecondaryInterface(interfaceConfig *interfacestore.InterfaceConfig) error {
	if err := pc.disconnectInterfaceFromOVS(interfaceConfig); err != nil {
		return err
	}
	if err := pc.ifConfigurator.removeContainerLink(interfaceConfig.ContainerID, interfaceConfig.InterfaceName); err != nil {
		klog.ErrorS(err, "Failed to delete container interface link",
			"Pod", klog.KRef(interfaceConfig.PodNamespace, interfaceConfig.PodName),
			"interface", interfaceConfig.IFDev)
		// No retry for interface deletion.
	}
	return nil
}
