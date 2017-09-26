#!/bin/bash
set -x

k8sconf="configs/aws.kubeconfig.yaml"

nameSpace="default"
podName=cpu-5-1
slave1="ip-172-23-1-92.us-west-2.compute.internal"
slave2="ip-172-23-1-12.us-west-2.compute.internal"
nodeName=$slave2

options="$options --kubeConfig $k8sconf "
options="$options --v 3 "
options="$options --nameSpace $nameSpace"
options="$options --podName $podName "
#options="$options --nodeName $nodeName "
options="$options --memLimit 100 "
options="$options --cpuLimit 200 "

# 1. build it
glide update --strip-vendor
make build

# 2. run it
./_output/kclient $options
