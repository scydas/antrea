#!/usr/bin/env bash

# Copyright 2020 Antrea Authors
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

set -eo pipefail

function echoerr {
    >&2 echo "$@"
}

CLUSTER=""
DEFAULT_WORKDIR="/var/lib/jenkins"
DEFAULT_KUBECONFIG_PATH=$DEFAULT_WORKDIR/.kube/config
WORKDIR=$DEFAULT_WORKDIR
KUBECONFIG_PATH=$DEFAULT_KUBECONFIG_PATH
MODE="report"
RUN_GARBAGE_COLLECTION=false
RUN_SETUP_ONLY=false
RUN_CLEANUP_ONLY=false
COVERAGE=false
RUN_TEST_ONLY=false
TESTCASE=""
CODECOV_TOKEN=""
SECRET_EXIST=false
TEST_FAILURE=false
CLUSTER_READY=false
DOCKER_REGISTRY=""
CONTROL_PLANE_NODE_ROLE="master|control-plane"
GOLANG_RELEASE_DIR=${WORKDIR}/golang-releases

_usage="Usage: $0 [--cluster-name <VMCClusterNameToUse>] [--kubeconfig <KubeconfigSavePath>] [--workdir <HomePath>]
                  [--log-mode <SonobuoyResultLogLevel>] [--testcase <e2e|conformance|all-features-conformance|whole-conformance|networkpolicy>]
                  [--garbage-collection] [--setup-only] [--cleanup-only] [--coverage] [--test-only] [--codecov-token] [--registry]

Setup a VMC cluster to run K8s e2e community tests (E2e, Conformance, all features Conformance, whole Conformance & Network Policy).

        --cluster-name           The cluster name to be used for the generated VMC cluster.
        --kubeconfig             Path to save kubeconfig of generated VMC cluster.
        --workdir                Home path for Go, vSphere information and antrea_logs during cluster setup. Default is $WORKDIR.
        --log-mode               Use the flag to set either 'report', 'detail', or 'dump' level data for sonobuoy results.
        --testcase               The testcase to run: e2e, conformance, all-features-conformance, whole-conformance or networkpolicy.
        --garbage-collection     Do garbage collection to clean up some unused testbeds.
        --setup-only             Only perform setting up the cluster and run test.
        --cleanup-only           Only perform cleaning up the cluster.
        --coverage               Run e2e with coverage.
        --test-only              Only run test on current cluster. Not set up/clean up the cluster.
        --codecov-token          Token used to upload coverage report(s) to Codecov.
        --registry               Using private registry to pull images."

function print_usage {
    echoerr "$_usage"
}

function print_help {
    echoerr "Try '$0 --help' for more information."
}

while [[ $# -gt 0 ]]
do
key="$1"

case $key in
    --cluster-name)
    CLUSTER="$2"
    shift 2
    ;;
    --kubeconfig)
    KUBECONFIG_PATH="$2"
    shift 2
    ;;
    --k8s-version)
    K8S_VERSION="$2"
    shift 2
    ;;
    --workdir)
    WORKDIR="$2"
    shift 2
    ;;
    --log-mode)
    MODE="$2"
    shift 2
    ;;
    --testcase)
    TESTCASE="$2"
    shift 2
    ;;
    --registry)
    DOCKER_REGISTRY="$2"
    shift 2
    ;;
    --username)
    CLUSTER_USERNAME="$2"
    shift 2
    ;;
    --password)
    CLUSTER_PASSWORD="$2"
    shift 2
    ;;
    --garbage-collection)
    RUN_GARBAGE_COLLECTION=true
    shift
    ;;
    --setup-only)
    RUN_SETUP_ONLY=true
    shift
    ;;
    --cleanup-only)
    RUN_CLEANUP_ONLY=true
    shift
    ;;
    --coverage)
    COVERAGE=true
    shift
    ;;
    --test-only)
    RUN_TEST_ONLY=true
    shift
    ;;
    --codecov-token)
    CODECOV_TOKEN="$2"
    shift 2
    ;;
    -h|--help)
    print_usage
    exit 0
    ;;
    *)    # unknown option
    echoerr "Unknown option $1"
    exit 1
    ;;
esac
done

if [[ "$WORKDIR" != "$DEFAULT_WORKDIR" && "$KUBECONFIG_PATH" == "$DEFAULT_KUBECONFIG_PATH" ]]; then
    KUBECONFIG_PATH=$WORKDIR/.kube/config
fi

# If DOCKER_REGISTRY is non null, we ensure that "make" commands never pull from docker.io.
NO_PULL=
if [[ ${DOCKER_REGISTRY} != "" ]]; then
    NO_PULL=1
fi
export NO_PULL

function saveLogs() {
    echo "=== Truncate old logs ==="
    mkdir -p $WORKDIR/antrea_logs
    LOG_DIR=$WORKDIR/antrea_logs
    find ${LOG_DIR}/* -type d -mmin +10080 | xargs -r rm -rf

    CLUSTER_LOG_DIR="${LOG_DIR}/${CLUSTER}"
    echo "=== Saving capi logs ==="
    mkdir -p ${CLUSTER_LOG_DIR}/capi
    kubectl get -n capi-system pods -o name | awk '{print $1}' | while read capi_pod; do
        capi_pod_name=$(echo ${capi_pod} | cut -d'/' -f 2)
        kubectl logs ${capi_pod_name} -c manager -n capi-system > ${CLUSTER_LOG_DIR}/capi/${capi_pod_name} || true
    done

    echo "=== Saving capv logs ==="
    mkdir -p ${CLUSTER_LOG_DIR}/capv
    kubectl get -n capv-system pods -o name | awk '{print $1}' | while read capv_pod; do
        capv_pod_name=$(echo ${capv_pod} | cut -d'/' -f 2)
        kubectl logs ${capv_pod_name} -c manager -n capv-system > ${CLUSTER_LOG_DIR}/capv/${capv_pod_name} || true
    done

    echo "=== Saving cluster_api.yaml ==="
    mkdir -p ${CLUSTER_LOG_DIR}/cluster_api
    kubectl get cluster-api -A -o yaml > ${CLUSTER_LOG_DIR}/cluster_api/cluster_api.yaml || true
}

function release_static_ip() {
    echo '=== Releasing IP ==='
    cat "$DEFAULT_WORKDIR/host-local.json" | CNI_COMMAND=DEL CNI_CONTAINERID="$CLUSTER" CNI_NETNS=/dev/null CNI_IFNAME=dummy0 CNI_PATH=. /usr/bin/host-local
    echo Released IP
}

function setup_cluster() {
    export KUBECONFIG=$KUBECONFIG_PATH
    if [ -z $K8S_VERSION ]; then
      export K8S_VERSION=v1.30.0
    fi
    if [ -z $TEST_OS ]; then
      export TEST_OS=ubuntu-2204
    fi
    export OVA_TEMPLATE_NAME=${TEST_OS}-kube-${K8S_VERSION}
    rm -rf ${GIT_CHECKOUT_DIR}/jenkins || true

    echo '=== Allocating IP ==='
    cat "$DEFAULT_WORKDIR/host-local.json" | CNI_COMMAND=ADD CNI_CONTAINERID="$CLUSTER" CNI_NETNS=/dev/null CNI_IFNAME=dummy0 CNI_PATH=. /usr/bin/host-local > ip-result.json
    CONTROL_VIP=$(cat ip-result.json | jq -r '.ips[0].address' | awk -F '/' '{print $1}')
    echo Allocated "$CONTROL_VIP"

    echo '=== Generate key pair ==='
    mkdir -p ${GIT_CHECKOUT_DIR}/jenkins/key
    ssh-keygen -b 2048 -t rsa -f  "${GIT_CHECKOUT_DIR}/jenkins/key/antrea-ci-key" -q -N ""
    publickey="$(cat ${GIT_CHECKOUT_DIR}/jenkins/key/antrea-ci-key.pub)"

    echo "=== namespace value substitution ==="
    mkdir -p ${GIT_CHECKOUT_DIR}/jenkins/out
    cp ${GIT_CHECKOUT_DIR}/ci/cluster-api/vsphere/templates/* ${GIT_CHECKOUT_DIR}/jenkins/out
    sed -i "s/CLUSTERNAMESPACE/${CLUSTER}/g" ${GIT_CHECKOUT_DIR}/jenkins/out/cluster.yaml
    sed -i "s/K8SVERSION/${K8S_VERSION}/g" ${GIT_CHECKOUT_DIR}/jenkins/out/cluster.yaml
    sed -i "s/OVATEMPLATENAME/${OVA_TEMPLATE_NAME}/g" ${GIT_CHECKOUT_DIR}/jenkins/out/cluster.yaml
    sed -i "s/CLUSTERNAME/${CLUSTER}/g" ${GIT_CHECKOUT_DIR}/jenkins/out/cluster.yaml
    sed -i "s|SSHAUTHORIZEDKEYS|${publickey}|g" ${GIT_CHECKOUT_DIR}/jenkins/out/cluster.yaml
    sed -i "s/CLUSTERUSERNAME/${CLUSTER_USERNAME}/g" ${GIT_CHECKOUT_DIR}/jenkins/out/cluster.yaml
    sed -i "s/CLUSTERPASSWORD/${CLUSTER_PASSWORD}/g" ${GIT_CHECKOUT_DIR}/jenkins/out/cluster.yaml
    sed -i "s/CONTROLVIP/${CONTROL_VIP}/g" ${GIT_CHECKOUT_DIR}/jenkins/out/cluster.yaml
    sed -i "s/CLUSTERNAMESPACE/${CLUSTER}/g" ${GIT_CHECKOUT_DIR}/jenkins/out/namespace.yaml

    echo "=== network spec value substitution==="
    index="$(($BUILD_NUMBER % 2))"
    cluster_defaults="${WORKDIR}/utils/CLUSTERDEFAULTS-${index}"
    while IFS= read -r line; do
        IFS='=' read -ra kv <<< "$line"
        sed -i "s|${kv[0]}|${kv[1]}|g" ${GIT_CHECKOUT_DIR}/jenkins/out/cluster.yaml
    done < "$cluster_defaults"

    echo '=== Create a cluster in management cluster ==='
    kubectl apply -f "${GIT_CHECKOUT_DIR}/jenkins/out/namespace.yaml"
    kubectl apply -f "${GIT_CHECKOUT_DIR}/jenkins/out/cluster.yaml"

    echo '=== Wait for 20 min to get workload cluster secret ==='
    for t in {1..20}
    do
        sleep 1m
        echo '=== Get kubeconfig (try for 1m) ==='
        if kubectl get secret/${CLUSTER}-kubeconfig -n${CLUSTER} ; then
            kubectl get secret/${CLUSTER}-kubeconfig -n${CLUSTER} -o json \
            | jq -r .data.value \
            | base64 --decode \
            > "${GIT_CHECKOUT_DIR}/jenkins/out/kubeconfig"
            SECRET_EXIST=true
            break
        fi
    done

    if [[ "$SECRET_EXIST" == false ]]; then
        echo "=== Failed to get secret ==="
        saveLogs
        kubectl delete ns ${CLUSTER}
        exit 1
    else
        export KUBECONFIG="${GIT_CHECKOUT_DIR}/jenkins/out/kubeconfig"
        echo "=== Waiting for 20 minutes for all nodes to be up ==="

        set +e
        for t in {1..20}
        do
            sleep 1m
            echo "=== Get node (try for 1m) ==="
            mdNum="$(kubectl get node | awk -v role="$CONTROL_PLANE_NODE_ROLE" '$3 !~ role {print $1}' | grep -c ${CLUSTER}-m)"
            if [ "${mdNum}" == "2" ]; then
                echo "=== Setup workload cluster succeeded ==="
                CLUSTER_READY=true
                break
            fi
        done
        set -e

        if [[ "$CLUSTER_READY" == false ]]; then
            echo "=== Failed to bring up all the nodes ==="
            saveLogs
            KUBECONFIG=$KUBECONFIG_PATH kubectl delete ns ${CLUSTER}
            exit 1
        fi
    fi
}

function copy_image {
  filename=$1
  image=$2
  IP=$3
  version=$4
  need_cleanup=$5
  ${SCP_WITH_ANTREA_CI_KEY} $filename capv@${IP}:/home/capv
  if [ $TEST_OS == 'centos-7' ]; then
      ${SSH_WITH_ANTREA_CI_KEY} -n capv@${IP} "sudo chmod 777 /run/containerd/containerd.sock"
      if [[ $need_cleanup == 'true' ]]; then
          ${SSH_WITH_ANTREA_CI_KEY} -n capv@${IP} "sudo crictl images | grep $image | awk '{print \$3}' | uniq | xargs -r crictl rmi"
      fi
      ${SSH_WITH_ANTREA_CI_KEY} -n capv@${IP} "ctr -n=k8s.io images import /home/capv/$filename ; ctr -n=k8s.io images tag $image:$version $image:latest --force"
  else
      if [[ $need_cleanup == 'true' ]]; then
          ${SSH_WITH_ANTREA_CI_KEY} -n capv@${IP} "sudo crictl images | grep $image | awk '{print \$3}' | uniq | xargs -r crictl rmi"
      fi
      ${SSH_WITH_ANTREA_CI_KEY} -n capv@${IP} "sudo ctr -n=k8s.io images import /home/capv/$filename ; sudo ctr -n=k8s.io images tag $image:$version $image:latest --force"
  fi
  ${SSH_WITH_ANTREA_CI_KEY} -n capv@${IP} "sudo crictl images | grep '<none>' | awk '{print \$3}' | xargs -r crictl rmi"
}

function copy_test_image {
  image=$1
  tag=$2

  docker pull ${image} && docker save -o image.tar ${image}

  for IP in "${IPs[@]}"; do
      echo "Processing image on node: $IP"
      ${SCP_WITH_ANTREA_CI_KEY} image.tar capv@${IP}:/home/capv
      ${SSH_WITH_ANTREA_CI_KEY} -n capv@${IP} "sudo ctr -n=k8s.io images import /home/capv/image.tar"
      if [ -n "$tag" ]; then
          ${SSH_WITH_ANTREA_CI_KEY} -n capv@${IP} "sudo ctr -n=k8s.io images tag $image $tag --force"
      fi
  done
}

# We run the function in a subshell with "set -e" to ensure that it exits in
# case of error (e.g. integrity check), no matter the context in which the
# function is called.
function run_codecov { (set -e
    flag=$1
    file=$2
    dir=$3

    rm -f trustedkeys.gpg codecov
    # This is supposed to be a one-time step, but there should be no harm in
    # getting the key every time. It does not come from the codecov.io
    # website. Anyway, this is needed when the VM is re-created for every test.
    curl https://keybase.io/codecovsecurity/pgp_keys.asc | gpg --no-default-keyring --keyring trustedkeys.gpg --import
    curl -Os https://uploader.codecov.io/latest/linux/codecov
    curl -Os https://uploader.codecov.io/latest/linux/codecov.SHA256SUM
    curl -Os https://uploader.codecov.io/latest/linux/codecov.SHA256SUM.sig

    # Check that the sha256 matches the signature
    gpgv codecov.SHA256SUM.sig codecov.SHA256SUM
    # Then check the integrity of the codecov binary
    shasum -a 256 -c codecov.SHA256SUM

    chmod +x codecov

    ./codecov -c -t "${CODECOV_TOKEN}" -F "${flag}" -f "${file}" -s "${dir}" -C "${GIT_COMMIT}" -r "antrea-io/antrea"
    rm -f trustedkeys.gpg codecov
)}

function deliver_antrea {
    echo "====== Building Antrea for the Following Commit ======"
    git show --numstat

    export GO111MODULE=on
    export GOPATH=$WORKDIR/go
    export GOROOT=${GOLANG_RELEASE_DIR}/go
    export GOCACHE=${GIT_CHECKOUT_DIR}/../gocache
    export PATH=$GOROOT/bin:$PATH

    make clean -C $GIT_CHECKOUT_DIR

    # The cleanup and stats are best-effort.
    set +e
    docker images --format "{{.Repository}}:{{.Tag}}" | grep "${JOB_NAME}" | xargs -r docker rmi -f > /dev/null
    # Clean up dangling and unused images generated in previous builds. Recent ones must be excluded
    # because they might be being used in other builds running simultaneously.
    docker image prune -af --filter "until=1h" > /dev/null
    docker system df -v
    check_and_cleanup_docker_build_cache
    set -e

    cd $GIT_CHECKOUT_DIR
    # Ensure that files in the Docker context have the correct permissions, or Docker caching cannot
    # be leveraged successfully
    chmod -R g-w build/images/ovs
    chmod -R g-w build/images/base
    # Pull images from Dockerhub first then try Harbor.
    for i in `seq 3`; do
        if [[ "$COVERAGE" == true ]]; then
            VERSION="$CLUSTER" ./hack/build-antrea-linux-all.sh --pull --coverage && break
        else
            VERSION="$CLUSTER" ./hack/build-antrea-linux-all.sh --pull && break
        fi
    done
    if [ $? -ne 0 ]; then
        echoerr "Failed to build antrea images with Dockerhub"
        for i in `seq 3`; do
            if [[ "$COVERAGE" == true ]]; then
                VERSION="$CLUSTER" DOCKER_REGISTRY="${DOCKER_REGISTRY}" ./hack/build-antrea-linux-all.sh --pull --coverage && break
            else
                VERSION="$CLUSTER" DOCKER_REGISTRY="${DOCKER_REGISTRY}" ./hack/build-antrea-linux-all.sh --pull && break
            fi
        done
        if [ $? -ne 0 ]; then
            echoerr "Failed to build antrea images with Harbor"
            exit 1
        fi
    fi
    if [[ "$COVERAGE" == true ]]; then
      VERSION="$CLUSTER" make flow-aggregator-ubuntu-coverage
    else
      VERSION="$CLUSTER" make flow-aggregator-image
    fi
    cd ci/jenkins

    if [ "$?" -ne "0" ]; then
        echo "=== Antrea Image or Flow Aggregator Image build failed ==="
        exit 1
    fi

    antrea_yml="antrea.yml"
    if [[ "$COVERAGE" == true ]]; then
        make manifest-coverage -C $GIT_CHECKOUT_DIR
        antrea_yml="antrea-coverage.yml"
    fi

    # Enable verbose log for troubleshooting.
    sed -i "s/--v=0/--v=4/g" $GIT_CHECKOUT_DIR/build/yamls/$antrea_yml

    DOCKER_IMG_VERSION=$CLUSTER
    if [[ -n $OLD_ANTREA_VERSION ]]; then
        if [[ $OLD_ANTREA_VERSION == 'LATEST' ]]; then
            OLD_ANTREA_VERSION="$(cd $GIT_CHECKOUT_DIR | git tag | sort -Vr | head -n 1)"
        fi
        # Let antrea controller use new Antrea image
        sed -i "0,/antrea-controller-ubuntu:latest/{s/antrea-controller-ubuntu:latest/antrea-controller-ubuntu:$DOCKER_IMG_VERSION/}" ${GIT_CHECKOUT_DIR}/build/yamls/$antrea_yml
    fi

    sed -i "s|#serviceCIDR: 10.96.0.0/12|serviceCIDR: 100.64.0.0/13|g" $GIT_CHECKOUT_DIR/build/yamls/$antrea_yml

    # Append antrea-prometheus.yml to antrea.yml
    echo "---" >> $GIT_CHECKOUT_DIR/build/yamls/$antrea_yml
    cat $GIT_CHECKOUT_DIR/build/yamls/antrea-prometheus.yml >> $GIT_CHECKOUT_DIR/build/yamls/$antrea_yml

    echo "====== Delivering Antrea to all the Nodes ======"
    export KUBECONFIG=${GIT_CHECKOUT_DIR}/jenkins/out/kubeconfig

    if [[ "$COVERAGE" == true ]]; then
        docker save -o antrea-agent-ubuntu-coverage.tar antrea/antrea-agent-ubuntu-coverage:${DOCKER_IMG_VERSION}
        docker save -o antrea-controller-ubuntu-coverage.tar antrea/antrea-controller-ubuntu-coverage:${DOCKER_IMG_VERSION}
        docker save -o flow-aggregator-coverage.tar antrea/flow-aggregator-coverage:${DOCKER_IMG_VERSION}
    else
        docker save -o antrea-agent-ubuntu.tar antrea/antrea-agent-ubuntu:${DOCKER_IMG_VERSION}
        docker save -o antrea-controller-ubuntu.tar antrea/antrea-controller-ubuntu:${DOCKER_IMG_VERSION}
        docker save -o flow-aggregator.tar antrea/flow-aggregator:${DOCKER_IMG_VERSION}
    fi

    control_plane_ip="$(kubectl get nodes -o wide --no-headers=true | awk -v role="$CONTROL_PLANE_NODE_ROLE" '$3 ~ role {print $6}')"
    ${SCP_WITH_ANTREA_CI_KEY} $GIT_CHECKOUT_DIR/build/yamls/*.yml capv@${control_plane_ip}:~

    IPs=($(kubectl get nodes -o wide --no-headers=true | awk '{print $6}' | xargs))
    antrea_images=("registry.k8s.io/e2e-test-images/agnhost:2.40" "antrea/nginx:1.21.6-alpine" "antrea/sonobuoy:v0.56.16" "antrea/toolbox:1.5-1" "antrea/systemd-logs:v0.4")
    k8s_images=("registry.k8s.io/e2e-test-images/agnhost:2.45" "registry.k8s.io/e2e-test-images/jessie-dnsutils:1.5" "registry.k8s.io/e2e-test-images/nginx:1.14-2")
    e2e_images=("k8sprow.azurecr.io/kubernetes-e2e-test-images/agnhost:2.45" "k8sprow.azurecr.io/kubernetes-e2e-test-images/jessie-dnsutils:1.5" "k8sprow.azurecr.io/kubernetes-e2e-test-images/nginx:1.14-2")
    for image in "${antrea_images[@]}"; do
        copy_test_image ${image}
    done
    for k in "${!k8s_images[@]}"; do
        copy_test_image ${k8s_images[$k]} ${e2e_images[$k]}
    done

    for i in "${!IPs[@]}"
    do
        ssh-keygen -f "/var/lib/jenkins/.ssh/known_hosts" -R ${IPs[$i]}
        if [[ "$COVERAGE" == true ]]; then
            copy_image antrea-agent-ubuntu-coverage.tar docker.io/antrea/antrea-agent-ubuntu-coverage ${IPs[$i]} ${DOCKER_IMG_VERSION} true
            copy_image antrea-controller-ubuntu-coverage.tar docker.io/antrea/antrea-controller-ubuntu-coverage ${IPs[$i]} ${DOCKER_IMG_VERSION} true
            copy_image flow-aggregator-coverage.tar docker.io/antrea/flow-aggregator-coverage ${IPs[$i]} ${DOCKER_IMG_VERSION} true
        else
            copy_image antrea-agent-ubuntu.tar docker.io/antrea/antrea-agent-ubuntu ${IPs[$i]} ${DOCKER_IMG_VERSION} true
            copy_image antrea-controller-ubuntu.tar docker.io/antrea/antrea-controller-ubuntu ${IPs[$i]} ${DOCKER_IMG_VERSION} true
            copy_image flow-aggregator.tar docker.io/antrea/flow-aggregator ${IPs[$i]} ${DOCKER_IMG_VERSION} true
        fi
    done

    if [[ -z $OLD_ANTREA_VERSION ]]; then
        return 0
    fi

    echo "====== Pulling old Antrea images ======"
    # Old Antrea versions can either use a unified image (pre v1.15) or split images.
    local old_agent_image=""
    if version_lt "$OLD_ANTREA_VERSION" v1.15; then
        if [[ ${DOCKER_REGISTRY} != "" ]]; then
            docker pull ${DOCKER_REGISTRY}/antrea/antrea-ubuntu:$OLD_ANTREA_VERSION
            docker tag ${DOCKER_REGISTRY}/antrea/antrea-ubuntu:$OLD_ANTREA_VERSION antrea/antrea-ubuntu:$OLD_ANTREA_VERSION
        else
            docker pull antrea/antrea-ubuntu:$OLD_ANTREA_VERSION
        fi
        old_agent_image="antrea/antrea-ubuntu:$OLD_ANTREA_VERSION"
    else
        if [[ ${DOCKER_REGISTRY} != "" ]]; then
            docker pull ${DOCKER_REGISTRY}/antrea/antrea-agent-ubuntu:$OLD_ANTREA_VERSION
            docker tag ${DOCKER_REGISTRY}/antrea/antrea-agent-ubuntu:$OLD_ANTREA_VERSION antrea/antrea-ubuntu:$OLD_ANTREA_VERSION
        else
            docker pull antrea/antrea-agent-ubuntu:$OLD_ANTREA_VERSION
        fi
        old_agent_image="antrea/antrea-agent-ubuntu:$OLD_ANTREA_VERSION"
    fi

    echo "====== Delivering old Antrea images to all the Nodes ======"
    docker save -o antrea-ubuntu-old.tar $old_agent_image
    node_num=$(kubectl get nodes --no-headers=true | wc -l)
    for i in "${!IPs[@]}"
    do
        # We want old-versioned Antrea agents to be more than half in cluster
        if [[ $i -ge $((${node_num}/2)) ]]; then
            # Tag old image to latest if we want Antrea agent to be old-versioned
            copy_image antrea-ubuntu-old.tar docker.io/antrea/antrea-agent-ubuntu ${IPs[$i]} $OLD_ANTREA_VERSION false
        fi
    done
}

function run_e2e {
    echo "====== Running Antrea E2E Tests ======"

    export GO111MODULE=on
    export GOPATH=$WORKDIR/go
    export GOROOT=${GOLANG_RELEASE_DIR}/go
    export GOCACHE=$WORKDIR/.cache/go-build
    export PATH=$GOROOT/bin:$PATH
    export KUBECONFIG=$GIT_CHECKOUT_DIR/jenkins/out/kubeconfig

    mkdir -p $GIT_CHECKOUT_DIR/test/e2e/infra/vagrant/playbook/kube
    CLUSTER_KUBECONFIG="${GIT_CHECKOUT_DIR}/jenkins/out/kubeconfig"
    CLUSTER_SSHCONFIG="${GIT_CHECKOUT_DIR}/jenkins/out/ssh-config"

    echo "=== Generate ssh-config ==="
    kubectl get nodes -o wide --no-headers=true | awk '{print $1}' | while read sshconfig_nodename; do
        echo "Generating ssh-config for Node ${sshconfig_nodename}"
        sshconfig_nodeip="$(kubectl get node "${sshconfig_nodename}" -o jsonpath='{.status.addresses[0].address}')"
        cp "${GIT_CHECKOUT_DIR}/ci/jenkins/ssh-config" "${CLUSTER_SSHCONFIG}.new"
        sed -i "s/SSHCONFIGNODEIP/${sshconfig_nodeip}/g" "${CLUSTER_SSHCONFIG}.new"
        sed -i "s/SSHCONFIGNODENAME/${sshconfig_nodename}/g" "${CLUSTER_SSHCONFIG}.new"
        echo "    IdentityFile ${GIT_CHECKOUT_DIR}/jenkins/key/antrea-ci-key" >> "${CLUSTER_SSHCONFIG}.new"
        cat "${CLUSTER_SSHCONFIG}.new" >> "${CLUSTER_SSHCONFIG}"
    done

    echo "=== Move kubeconfig to control-plane Node ==="
    control_plane_ip="$(kubectl get nodes -o wide --no-headers=true | awk -v role="$CONTROL_PLANE_NODE_ROLE" '$3 ~ role {print $6}')"
    ${SSH_WITH_ANTREA_CI_KEY} -n capv@${control_plane_ip} "if [ ! -d ".kube" ]; then mkdir .kube; fi"
    ${SCP_WITH_ANTREA_CI_KEY} $GIT_CHECKOUT_DIR/jenkins/out/kubeconfig capv@${control_plane_ip}:~/.kube/config

    set +e
    kubectl taint nodes --selector='!node-role.kubernetes.io/control-plane' node.cluster.x-k8s.io/uninitialized-
    mkdir -p ${GIT_CHECKOUT_DIR}/antrea-test-logs
    if [[ "$COVERAGE" == true ]]; then
        rm -rf ${GIT_CHECKOUT_DIR}/e2e-coverage
        mkdir -p ${GIT_CHECKOUT_DIR}/e2e-coverage
        # HACK: see https://github.com/antrea-io/antrea/issues/2292
        go mod edit -replace github.com/moby/spdystream=github.com/antoninbas/spdystream@v0.2.1 && go mod tidy
        go test -v -timeout=100m antrea.io/antrea/test/e2e --logs-export-dir ${GIT_CHECKOUT_DIR}/antrea-test-logs --prometheus --coverage --coverage-dir ${GIT_CHECKOUT_DIR}/e2e-coverage --provider remote --remote.sshconfig "${CLUSTER_SSHCONFIG}" --remote.kubeconfig "${CLUSTER_KUBECONFIG}"
    else
        # HACK: see https://github.com/antrea-io/antrea/issues/2292
        go mod edit -replace github.com/moby/spdystream=github.com/antoninbas/spdystream@v0.2.1 && go mod tidy
        go test -v -timeout=100m antrea.io/antrea/test/e2e --logs-export-dir ${GIT_CHECKOUT_DIR}/antrea-test-logs --prometheus --provider remote --remote.sshconfig "${CLUSTER_SSHCONFIG}" --remote.kubeconfig "${CLUSTER_KUBECONFIG}"
    fi

    test_rc=$?
    set -e

    if [[ "$test_rc" != "0" ]]; then
        echo "=== TEST FAILURE !!! ==="
        TEST_FAILURE=true
    else
        echo "=== TEST SUCCESS !!! ==="
    fi

    tar -zcf ${GIT_CHECKOUT_DIR}/antrea-test-logs.tar.gz ${GIT_CHECKOUT_DIR}/antrea-test-logs
    if [[ "$COVERAGE" == true ]]; then
        pushd ${GIT_CHECKOUT_DIR}/e2e-coverage
        for dir in */; do
            go tool covdata textfmt -i="${dir}" -o "${dir%?}.cov.out"
            rm -rf "${dir}";
        done
        popd
        tar -zcf ${GIT_CHECKOUT_DIR}/e2e-coverage.tar.gz ${GIT_CHECKOUT_DIR}/e2e-coverage
        run_codecov "e2e-tests" "*.cov.out*" "${GIT_CHECKOUT_DIR}/e2e-coverage"
    fi
}

function run_conformance {
    echo "====== Running Antrea Conformance Tests ======"

    export GO111MODULE=on
    export GOPATH=$WORKDIR/go
    export GOROOT=${GOLANG_RELEASE_DIR}/go
    export GOCACHE=$WORKDIR/.cache/go-build
    export PATH=$GOROOT/bin:$PATH
    export KUBECONFIG=$GIT_CHECKOUT_DIR/jenkins/out/kubeconfig

    antrea_yml="antrea.yml"
    if [[ "$COVERAGE" == true ]]; then
        antrea_yml="antrea-coverage.yml"
    fi

    if [[ "$TESTCASE" == "all-features-conformance" ]]; then
      if [[ "$COVERAGE" == true ]]; then
        $GIT_CHECKOUT_DIR/hack/generate-manifest.sh --mode dev --feature-gates AllAlpha=true,AllBeta=true --proxy-all --coverage > $GIT_CHECKOUT_DIR/build/yamls/antrea-all-coverage.yml
        antrea_yml="antrea-all-coverage.yml"
      else
        $GIT_CHECKOUT_DIR/hack/generate-manifest.sh --mode dev --feature-gates AllAlpha=true,AllBeta=true --proxy-all --verbose-log > $GIT_CHECKOUT_DIR/build/yamls/antrea-all.yml
        antrea_yml="antrea-all.yml"
      fi
    fi

    kubectl apply -f $GIT_CHECKOUT_DIR/build/yamls/$antrea_yml
    kubectl rollout restart deployment/coredns -n kube-system
    kubectl rollout status --timeout=5m deployment/coredns -n kube-system
    kubectl rollout status --timeout=5m deployment.apps/antrea-controller -n kube-system
    kubectl rollout status --timeout=5m daemonset/antrea-agent -n kube-system

    control_plane_ip="$(kubectl get nodes -o wide --no-headers=true | awk -v role="$CONTROL_PLANE_NODE_ROLE" '$3 ~ role {print $6}')"
    echo "=== Move kubeconfig to control-plane Node ==="
    ${SSH_WITH_ANTREA_CI_KEY} -n capv@${control_plane_ip} "if [ ! -d ".kube" ]; then mkdir .kube; fi"
    ${SCP_WITH_ANTREA_CI_KEY} $GIT_CHECKOUT_DIR/jenkins/out/kubeconfig capv@${control_plane_ip}:~/.kube/config

    set +e
    kubectl taint nodes --selector='!node-role.kubernetes.io/control-plane' node.cluster.x-k8s.io/uninitialized-

    if [[ "$TESTCASE" == "conformance" ]]; then
        ${GIT_CHECKOUT_DIR}/ci/run-k8s-e2e-tests.sh --e2e-conformance --e2e-sig-network --log-mode ${MODE} --kubeconfig ${GIT_CHECKOUT_DIR}/jenkins/out/kubeconfig > ${GIT_CHECKOUT_DIR}/vmc-test.log
    elif [[ "$TESTCASE" == "all-features-conformance" ]]; then
        ${GIT_CHECKOUT_DIR}/ci/run-k8s-e2e-tests.sh --e2e-conformance --log-mode ${MODE} --kubeconfig ${GIT_CHECKOUT_DIR}/jenkins/out/kubeconfig > ${GIT_CHECKOUT_DIR}/vmc-test.log
    elif [[ "$TESTCASE" == "whole-conformance" ]]; then
        ${GIT_CHECKOUT_DIR}/ci/run-k8s-e2e-tests.sh --e2e-whole-conformance --log-mode ${MODE} --kubeconfig ${GIT_CHECKOUT_DIR}/jenkins/out/kubeconfig > ${GIT_CHECKOUT_DIR}/vmc-test.log
    else
        ${GIT_CHECKOUT_DIR}/ci/run-k8s-e2e-tests.sh --e2e-network-policy --log-mode ${MODE} --kubeconfig ${GIT_CHECKOUT_DIR}/jenkins/out/kubeconfig > ${GIT_CHECKOUT_DIR}/vmc-test.log
    fi

    TEST_SCRIPT_RC=$?
    if [[ $TEST_SCRIPT_RC -eq 0 ]]; then
        echo "All tests passed."
        echo "=== SUCCESS !!! ==="
    elif [[ $TEST_SCRIPT_RC -eq 1 ]]; then
        echo "Failed test cases exist."
        echo "=== FAILURE !!! ==="
        TEST_FAILURE=true
    else
        echo "Unexpected error when running tests but not a test failure."
        echo "=== FAILURE !!! ==="
    fi
    set -e

    if [[ "$COVERAGE" == true ]]; then
        rm -rf ${GIT_CHECKOUT_DIR}/conformance-coverage
        mkdir -p ${GIT_CHECKOUT_DIR}/conformance-coverage
        collect_coverage_for_conformance
        tar -zcf ${GIT_CHECKOUT_DIR}/$TESTCASE-coverage.tar.gz ${GIT_CHECKOUT_DIR}/conformance-coverage
        run_codecov "e2e-tests" "*antrea*" "${GIT_CHECKOUT_DIR}/conformance-coverage"
    fi
}

function collect_coverage_for_conformance() {
        antrea_controller_pod_name="$(kubectl get pods --selector=app=antrea,component=antrea-controller -n kube-system --no-headers=true | awk '{ print $1 }')"
        controller_pid="$(kubectl exec -i $antrea_controller_pod_name -n kube-system -- pgrep antrea)"
        kubectl exec -i $antrea_controller_pod_name -n kube-system -- kill -SIGINT $controller_pid
        timestamp=$(date +%Y%m%d%H%M%S)
        cov_dir="${GIT_CHECKOUT_DIR}/conformance-coverage/$antrea_controller_pod_name-$timestamp"
        mkdir -p $cov_dir
        files=(`kubectl exec $antrea_controller_pod_name -n kube-system ${kubeconfig} -- ls /tmp/coverage/`)
        for file in "${files[@]}"; do
            kubectl cp kube-system/$antrea_controller_pod_name:/tmp/coverage/$file $cov_dir/$file ${kubeconfig}
        done
        go tool covdata textfmt -i="${cov_dir}" -o "${cov_dir}.cov.out"
        rm -rf "${cov_dir}"

        antrea_agent_pod_names="$(kubectl get pods --selector=app=antrea,component=antrea-agent -n kube-system --no-headers=true | awk '{ print $1 }')"
        for agent in ${antrea_agent_pod_names}
        do
            agent_pid="$(kubectl exec -i $agent -n kube-system -- pgrep antrea)"
            kubectl exec -i $agent -c antrea-agent -n kube-system -- kill -SIGINT $agent_pid
            timestamp=$(date +%Y%m%d%H%M%S)
            cov_dir="${GIT_CHECKOUT_DIR}/conformance-coverage/$agent-$timestamp"
            mkdir -p $cov_dir
            files=(`kubectl exec $agent -n kube-system ${kubeconfig} -c antrea-agent -- ls /tmp/coverage/`)
            for file in "${files[@]}"; do
                kubectl cp kube-system/$agent:/tmp/coverage/$file -c antrea-agent $cov_dir/$file ${kubeconfig}
            done
            go tool covdata textfmt -i="${cov_dir}" -o "${cov_dir}.cov.out"
            rm -rf "${cov_dir}"
        done
}

function cleanup_cluster() {
    release_static_ip
    echo "=== Cleaning up VMC cluster ${CLUSTER} ==="
    export KUBECONFIG=$KUBECONFIG_PATH

    kubectl delete ns ${CLUSTER}
    rm -rf "${GIT_CHECKOUT_DIR}/jenkins"
    echo "=== Cleanup cluster ${CLUSTER} succeeded ==="
}

function garbage_collection() {
    echo "=== Auto cleanup starts ==="
    export KUBECONFIG=$KUBECONFIG_PATH

    kubectl get ns -l antrea-ci -o custom-columns=Name:.metadata.name,DATE:.metadata.creationTimestamp --no-headers=true | awk '{cmd="echo $(( $(date +%s) - $(date -d "$2" +%s) ))"; cmd | getline t ; print $1, t}' | awk '$1 ~ "matrix" && $2 > 36000 {print $1}' | while read cluster; do
        # Matrix tests
        echo "=== Currently ${cluster} has been live for more than 10h ==="
        kubectl delete ns ${cluster}
        echo "=== Old namespace ${cluster} has been deleted !!! ==="
    done

    kubectl get ns -l antrea-ci -o custom-columns=Name:.metadata.name,DATE:.metadata.creationTimestamp --no-headers=true | awk '{cmd="echo $(( $(date +%s) - $(date -d "$2" +%s) ))"; cmd | getline t ; print $1, t}' | awk '$1 ~ "whole-conformance" && $2 > 14400 {print $1}' | while read cluster; do
        # Whole-conformance test
        echo "=== Currently ${cluster} has been live for more than 4h ==="
        kubectl delete ns ${cluster}
        echo "=== Old namespace ${cluster} has been deleted !!! ==="
    done

    kubectl get ns -l antrea-ci -o custom-columns=Name:.metadata.name,DATE:.metadata.creationTimestamp --no-headers=true | awk '{cmd="echo $(( $(date +%s) - $(date -d "$2" +%s) ))"; cmd | getline t ; print $1, t}' | awk '$1 !~ "matrix" && $1 !~ "whole-conformance" && $2 > 9000 {print $1}' | while read cluster; do
        # e2e, conformance, networkpolicy tests
        echo "=== Currently ${cluster} has been live for more than 2.5h ==="
        kubectl delete ns ${cluster}
        echo "=== Old namespace ${cluster} has been deleted !!! ==="
    done

    echo "=== Auto cleanup finished ==="
}

function clean_tmp() {
    echo "===== Clean up stale files & folders older than 7 days under /tmp ====="
    CLEAN_LIST=(
        "*codecov*"
        "kustomize-*"
        "*antrea*"
        "go-build*"
    )
    for item in "${CLEAN_LIST[@]}"; do
        find /tmp -name "${item}" -mtime +7 -exec rm -rf {} \; 2>&1 | grep -v "Permission denied" || true
    done
}

# ensures that the script can be run from anywhere
THIS_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" >/dev/null 2>&1 && pwd )"
GIT_CHECKOUT_DIR=${THIS_DIR}/../..
pushd "$THIS_DIR" > /dev/null

SCP_WITH_ANTREA_CI_KEY="scp -q -o UserKnownHostsFile=/dev/null -o StrictHostKeyChecking=no -i ${GIT_CHECKOUT_DIR}/jenkins/key/antrea-ci-key"
SSH_WITH_ANTREA_CI_KEY="ssh -q -o UserKnownHostsFile=/dev/null -o StrictHostKeyChecking=no -i ${GIT_CHECKOUT_DIR}/jenkins/key/antrea-ci-key"
SSH_WITH_UTILS_KEY="ssh -q -o UserKnownHostsFile=/dev/null -o StrictHostKeyChecking=no -i ${WORKDIR}/utils/key"
SCP_WITH_UTILS_KEY="scp -q -o UserKnownHostsFile=/dev/null -o StrictHostKeyChecking=no -i ${WORKDIR}/utils/key"

source $WORKSPACE/ci/jenkins/utils.sh
check_and_upgrade_golang
clean_tmp
if [[ "$RUN_GARBAGE_COLLECTION" == true ]]; then
    garbage_collection
    exit 0
fi

if [[ "$RUN_SETUP_ONLY" == true ]]; then
    setup_cluster
    deliver_antrea
    exit 0
fi

if [[ "$RUN_CLEANUP_ONLY" == true ]]; then
    cleanup_cluster
    exit 0
fi

if [[ "$TESTCASE" != "e2e" && "$TESTCASE" != "conformance" && "$TESTCASE" != "all-features-conformance" && "$TESTCASE" != "whole-conformance" && "$TESTCASE" != "networkpolicy" ]]; then
    echoerr "testcase should be e2e, conformance, all-features-conformance, whole-conformance or networkpolicy"
    exit 1
fi

if [[ "$RUN_TEST_ONLY" == true ]]; then
    if [[ "$TESTCASE" == "e2e" ]]; then
        run_e2e
    else
        run_conformance
    fi
    if [[ "$TEST_FAILURE" == true ]]; then
        exit 1
    fi
    exit 0
fi

trap cleanup_cluster EXIT
if [[ "$TESTCASE" == "e2e" ]]; then
    setup_cluster
    deliver_antrea
    run_e2e
else
    setup_cluster
    deliver_antrea
    run_conformance
fi

if [[ "$TEST_FAILURE" == true ]]; then
    exit 1
fi
