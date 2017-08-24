package main

import (
	"encoding/json"
	"fmt"
	//"time"

	"github.com/golang/glog"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	client "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/pkg/api/v1"
	restclient "k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/apimachinery/pkg/api/resource"
)

const (
	// Set the grace period to 0 for deleting the pod immediately.
	DefaultPodGracePeriod int64 = 0
)

func printPods(pods *v1.PodList) {
	fmt.Printf("api version:%s, kind:%s, r.version:%s\n",
		pods.APIVersion,
		pods.Kind,
		pods.ResourceVersion)

	for _, pod := range pods.Items {
		fmt.Printf("%s/%s, phase:%s, node.Name:%s, host:%s\n",
			pod.Namespace,
			pod.Name,
			pod.Status.Phase,
			pod.Spec.NodeName,
			pod.Status.HostIP)
	}
}

func listPod(client *client.Clientset) {
	pods, err := client.CoreV1().Pods(v1.NamespaceAll).List(metav1.ListOptions{})
	if err != nil {
		panic(err.Error())
	}
	fmt.Printf("There are %d pods in the cluster\n", len(pods.Items))
	printPods(pods)

	glog.V(2).Info("test finish")
}

func copyPodInfoX(oldPod, newPod *v1.Pod) {
	//1. typeMeta
	newPod.TypeMeta = oldPod.TypeMeta

	//2. objectMeta
	newPod.ObjectMeta = oldPod.ObjectMeta
	newPod.SelfLink = ""
	newPod.ResourceVersion = ""
	newPod.Generation = 0
	newPod.CreationTimestamp = metav1.Time{}
	newPod.DeletionTimestamp = nil
	newPod.DeletionGracePeriodSeconds = nil

	//3. podSpec
	spec := oldPod.Spec
	spec.Hostname = ""
	spec.Subdomain = ""
	spec.NodeName = ""

	newPod.Spec = spec
	return
}

func copyPodInfo(oldPod, newPod *v1.Pod) {
	//1. typeMeta -- full copy
	newPod.Kind = oldPod.Kind
	newPod.APIVersion = oldPod.APIVersion

	//2. objectMeta -- partial copy
	newPod.Name = oldPod.Name
	newPod.GenerateName = oldPod.GenerateName
	newPod.Namespace = oldPod.Namespace
	//newPod.SelfLink = oldPod.SelfLink
	newPod.UID = oldPod.UID
	//newPod.ResourceVersion = oldPod.ResourceVersion
	//newPod.Generation = oldPod.Generation
	//newPod.CreationTimestamp = oldPod.CreationTimestamp

	//NOTE: Deletion timestamp and gracePeriod will be set by system when to delete it.
	//newPod.DeletionTimestamp = oldPod.DeletionTimestamp
	//newPod.DeletionGracePeriodSeconds = oldPod.DeletionGracePeriodSeconds

	newPod.Labels = oldPod.Labels
	newPod.Annotations = oldPod.Annotations
	newPod.OwnerReferences = oldPod.OwnerReferences
	newPod.Initializers = oldPod.Initializers
	newPod.Finalizers = oldPod.Finalizers
	newPod.ClusterName = oldPod.ClusterName

	//3. podSpec -- full copy with modifications
	spec := oldPod.Spec
	spec.Hostname = ""
	spec.Subdomain = ""
	spec.NodeName = ""

	newPod.Spec = spec

	//4. status: won't copy status
}

func getParentInfo(pod *v1.Pod) (string, string, error) {
	//1. check ownerReferences:
	if pod.OwnerReferences != nil && len(pod.OwnerReferences) > 0 {
		for _, owner := range pod.OwnerReferences {
			if *owner.Controller {
				return owner.Kind, owner.Name, nil
			}
		}
	}

	glog.V(3).Infof("cannot find pod-%v/%v parent by OwnerReferences.", pod.Namespace, pod.Name)

	//2. check annotations:
	if pod.Annotations != nil && len(pod.Annotations) > 0 {
		key := "kubernetes.io/created-by"
		if value, ok := pod.Annotations[key]; ok {
			var ref v1.SerializedReference

			if err := json.Unmarshal([]byte(value), &ref); err != nil {
				err = fmt.Errorf("failed to decode parent annoation:%v\n[%v]", err.Error(), value)
				glog.Error(err.Error())
				return "", "", err
			}

			return ref.Reference.Kind, ref.Reference.Name, nil
		}
	}

	glog.V(3).Infof("cannot find pod-%v/%v parent by Annotations.", pod.Namespace, pod.Name)

	return "", "", nil
}

func getKubeClient(masterUrl, kubeConfig *string) *client.Clientset {
	if *masterUrl == "" && *kubeConfig == "" {
		fmt.Println("must specify masterUrl or kubeConfig.")
		return nil
	}

	var err error
	var config *restclient.Config

	if *kubeConfig != "" {
		config, err = clientcmd.BuildConfigFromFlags("", *kubeConfig)
	} else {
		config, err = clientcmd.BuildConfigFromFlags(*masterUrl, "")
	}

	if err != nil {
		panic(err.Error())
	}
	// creates the clientset
	clientset, err := client.NewForConfig(config)
	if err != nil {
		panic(err.Error())
	}

	return clientset
}

func getSchedulerName(client *client.Clientset, kind, nameSpace, name string) (string, error) {
	rerr := fmt.Errorf("unsupported kind:[%v]", kind)

	option := metav1.GetOptions{}
	switch kind {
	case KindReplicationController:
		if rc, err := client.CoreV1().ReplicationControllers(nameSpace).Get(name, option); err == nil {
			return rc.Spec.Template.Spec.SchedulerName, nil
		} else {
			rerr = err
		}
	case KindReplicaSet:
		if rs, err := client.ExtensionsV1beta1().ReplicaSets(nameSpace).Get(name, option); err == nil {
			return rs.Spec.Template.Spec.SchedulerName, nil
		} else {
			rerr = err
		}
	}

	return "", rerr
}

func checkSchedulerName(client *client.Clientset, kind, nameSpace, name, expectedScheduler string) (bool, error) {
	currentName, err := getSchedulerName(client, kind, nameSpace, name)
	if err != nil {
		return false, err
	}

	if currentName == expectedScheduler {
		return true, nil
	}

	return false, nil
}

//update the schedulerName of a ReplicaSet to <schedulerName>
// return the previous schedulerName
func updateRSscheduler(client *client.Clientset, nameSpace, rsName, schedulerName string) (string, error) {
	currentName := ""

	rsClient := client.ExtensionsV1beta1().ReplicaSets(nameSpace)
	if rsClient == nil {
		return "", fmt.Errorf("failed to get ReplicaSet client in namespace: %v", nameSpace)
	}

	id := fmt.Sprintf("%v/%v", nameSpace, rsName)

	//1. get ReplicaSet
	option := metav1.GetOptions{}
	rs, err := rsClient.Get(rsName, option)
	if err != nil {
		err = fmt.Errorf("failed to get ReplicaSet-%v: %v", id, err.Error())
		glog.Error(err.Error())
		return currentName, err
	}

	if rs.Spec.Template.Spec.SchedulerName == schedulerName {
		glog.V(3).Infof("no need to update schedulerName for RS-[%v]", rsName)
		return "", nil
	}

	//2. update schedulerName
	rs.Spec.Template.Spec.SchedulerName = schedulerName
	_, err = rsClient.Update(rs)
	if err != nil {
		//TODO: check whether need to retry
		err = fmt.Errorf("failed to update RC-%v:%v\n", id, err.Error())
		glog.Error(err.Error())
		return currentName, err
	}

	return currentName, nil
}

//update the schedulerName of a ReplicationController
// if condName is not empty, then only current schedulerName is same to condName, then will do the update.
// return the previous schedulerName; or return "" if update failed.
func updateRCscheduler(client *client.Clientset, nameSpace, rcName, schedulerName string) (string, error) {
	currentName := ""

	id := fmt.Sprintf("%v/%v", nameSpace, rcName)
	rcClient := client.CoreV1().ReplicationControllers(nameSpace)

	//1. get
	option := metav1.GetOptions{}
	rc, err := rcClient.Get(rcName, option)
	if err != nil {
		err = fmt.Errorf("failed to get ReplicationController-%v: %v\n", id, err.Error())
		glog.Error(err.Error())
		return currentName, err
	}

	if rc.Spec.Template.Spec.SchedulerName == schedulerName {
		glog.V(3).Infof("no need to update schedulerName for RC-[%v]", rcName)
		return "", nil
	}

	//2. update
	rc.Spec.Template.Spec.SchedulerName = schedulerName
	rc, err = rcClient.Update(rc)
	if err != nil {
		//TODO: check whether need to retry
		err = fmt.Errorf("failed to update RC-%v:%v\n", id, err.Error())
		glog.Error(err.Error())
		return currentName, err
	}

	return currentName, nil
}

// move pod nameSpace/podName to node nodeName
func resizePod(client *client.Clientset, pod *v1.Pod, req v1.ResourceList) error {
	podClient := client.CoreV1().Pods(pod.Namespace)
	if podClient == nil {
		err := fmt.Errorf("cannot get Pod client for nameSpace:%v", pod.Namespace)
		glog.Errorf(err.Error())
		return err
	}

	//1. copy the original pod
	id := fmt.Sprintf("%v/%v", pod.Namespace, pod.Name)
	glog.V(2).Infof("resize-pod: begin to resize Pod %v", id)

	npod := &v1.Pod{}
	copyPodInfoX(pod, npod)
	npod.Spec.NodeName = pod.Spec.NodeName

	if flag, err := updateCapacity(pod, req, 0); err != nil || !flag {
	//if flag, err := updateResource(pod, req); err != nil || !flag {
		if err != nil {
			glog.Errorf("failed to updateResource: %v", err)
			return err
		}
		glog.V(2).Infof("no need to resize Pod-container:%s", id)
		return nil
	}
	container := &(npod.Spec.Containers[0])
	printResourceList("mid-limits", container.Resources.Limits)
	printResourceList("mid-request", container.Resources.Requests)

	//2. kill original pod
	var grace int64 = 0
	delOption := &metav1.DeleteOptions{GracePeriodSeconds: &grace}
	err := podClient.Delete(pod.Name, delOption)
	if err != nil {
		err = fmt.Errorf("resize-failed: failed to delete original pod-%v: %v",
			id, err.Error())
		glog.Error(err.Error())
		return err
	}
	//time.Sleep(time.Second * 20)

	//3. create (and bind) the new Pod
	//glog.V(2).Infof("sleep 10 seconds to test the behaivor of quicker ReplicationController")
	//time.Sleep(time.Second * 10) // this line is for experiments
	_, err = podClient.Create(npod)
	if err != nil {
		err = fmt.Errorf("resize-failed: failed to create new pod-%v: %v",
			id, err.Error())
		glog.Error(err.Error())
		return err
	}

	return nil
}

func checkPodHealth(kclient *client.Clientset, nameSpace, podName string) error {
	pod, err := GetPod(kclient, nameSpace, podName)
	if err != nil {
		glog.Errorf("failed to check Pod[%s] health: %v", podName, err)
		return err
	}

	if pod.Status.Phase != v1.PodRunning {
		err = fmt.Errorf("pod-%v is not running: %v", podName, pod.Status.Phase)
		glog.Error(err.Error())
		return err
	}

	return nil
}

func GetPod(kclient *client.Clientset, nameSpace, name string) (*v1.Pod, error) {
	podClient := kclient.CoreV1().Pods(nameSpace)
	id := fmt.Sprintf("%v/%v", nameSpace, name)

	getOption := metav1.GetOptions{}
	pod, err := podClient.Get(name, getOption)
	if err != nil {
		err = fmt.Errorf("failed ot get Pod-%v: %v", id, err)
		glog.Error(err.Error())
		return nil, err
	}

	return pod, nil
}


func printResourceList(prefix string, rlist v1.ResourceList) {
	//for k, v := range rlist {
	//	fmt.Printf("k=%s, v=[%++v], %v\n", k, v, v.Value())
	//}

	cpu := rlist.Cpu().MilliValue()
	mem := rlist.Memory().Value()
	glog.V(2).Infof("[%s] cpu: %v Mhz, mem: %v Bytes", prefix, cpu, mem)
}

func PrintPodResource(kclient *client.Clientset, nameSpace, podName string) {
	pod, err := GetPod(kclient, nameSpace, podName)
	if err != nil {
		glog.Errorf("failed to get Pod[%s]: %v", podName, err)
		return
	}

	container := &(pod.Spec.Containers[0])
	printResourceList("limits", container.Resources.Limits)
	printResourceList("requests", container.Resources.Requests)
	return
}


// update the Pod.Containers[index]'s Resources.Limits and Resources.Requests.
func updateCapacity(pod *v1.Pod, patchCapacity v1.ResourceList, index int) (bool, error) {
	glog.V(2).Infof("begin to update Capacity.")
	changed := false

	if index >= len(pod.Spec.Containers) {
		err := fmt.Errorf("Cannot find container[%d] in pod[%s]", index, pod.Name)
		glog.Error(err)
		return false, err
	}
	container := &(pod.Spec.Containers[index])

	//1. get the original capacities
	result := make(v1.ResourceList)
	for k, v := range container.Resources.Limits {
		result[k] = v
	}

	//2. apply the patch
	for k, v := range patchCapacity {
		oldv, exist := result[k]
		if !exist || oldv.Cmp(v) != 0 {
			result[k] = v
			changed = true
		}
	}

	if !changed {
		return false, nil
	}
	container.Resources.Limits = result

	//3. adjust the requirements: if new capacity is less than requirement, reduce the requirement
	// TODO 1: discuss reduce the requirement, or increase the limit?
	// TODO 2: If Requests is omitted for a container, it defaults to Limits if that is explicitly specified,
	//      we have to set a value for the requests; how to decide the value?

	updateRequests(container, result)
	return changed, nil
}

func updateRequests(container *v1.Container, limits v1.ResourceList) error {
	zero := resource.NewQuantity(0, resource.BinarySI)
	glog.V(2).Infof("zero=%++v", zero)

	if container.Resources.Requests == nil {
		container.Resources.Requests = make(v1.ResourceList)
	}
	requests := container.Resources.Requests

	for k, v := range limits {
		rv, exist := requests[k]
		if !exist {
			requests[k] = *zero
			continue
		}

		if rv.Cmp(v) > 0 {
			requests[k] = v
		}
	}

	return nil
}