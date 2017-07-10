# resizePod
update container resources by kill-recreate Pod

## Introduction ##
We want to be able resize the resource request/limit for containers of a Pod. 

There was some discussion about this [request](https://github.com/kubernetes/kubernetes/issues/5774) in Kubernetes in March, 2015. The following [PR](https://github.com/kubernetes/kubernetes/pull/8157) which tried to enable live resource update, is closed, because many components and cases should be considered.

## Solution ##
We uses a brute force way to solve this problem: 
* First, delete the orginial Pod; 
* Second, create a new Pod with most of pod.Spec copied from the original Pod, but with new resoure settings.

For pod created/controlled by ReplicationController/ReplicaSet, we have to manipulate ReplicationController/ReplicaSet, 
so that we can create a Pod according to our requirement. The way to achieve this is the [same trick](https://github.com/songbinliu/movePod) when we want to move Pod 
to a specified node. It should be noted that we won't assign a node for the pod, but let the scheduler to assign a node for this Pod.

## drawbacks ##
It has to stop the Pod for a while.

## Run it ##
```console
./resizePod --kubeConfig configs/aws.kubeconfig.yaml --v 3 --nameSpace default --podName mem-deployment-4234284026-lgtkc --memLimit 400 --cpuLimit 100
```
